#!/usr/bin/env python3
"""Start the seven exact release images on a disposable network, then run AC04's baseline."""

import argparse
import base64
import hashlib
import json
import re
import secrets
import shutil
import socket
import ssl
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path
from types import SimpleNamespace

import release_evidence as evidence
import tls_reload_probe

FRONT_IMAGE = "nginxinc/nginx-unprivileged:1.30-alpine-slim@sha256:5d8bb9437d74a600ec5dbf90c035451c724dc33eac8d3a71078f6f8612c345f6"


def wait_http(endpoint):
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(endpoint, timeout=5) as response:
                if response.status == 200:
                    return
        except (OSError, urllib.error.HTTPError):
            pass
        time.sleep(1)
    raise ValueError(f"fresh image did not become ready: {endpoint}")


def endpoint(container, port):
    published = evidence.run(["docker", "port", container, f"{port}/tcp"]).stdout.strip()
    if not published.startswith("127.0.0.1:"):
        raise ValueError(f"container did not publish a private loopback port: {container}")
    return "http://" + published


def reload_tls(directory, running, front):
    plan = {"pairs": [], "listeners": []}
    for service in ("api", "smtp-gateway", "imap-gateway", "pop3-gateway"):
        internal = service == "api"
        purpose = "internal" if internal else "public"
        hostname = "api" if internal else "mail.release.test"
        stem = directory / ("renewed-" + service)
        extensions = directory / "renewal.extensions"
        extensions.write_text("basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nauthorityKeyIdentifier=keyid,issuer\nextendedKeyUsage=serverAuth\nsubjectAltName=DNS:" + hostname + ",IP:127.0.0.1\n")
        evidence.run(["openssl", "req", "-new", "-newkey", "rsa:2048", "-nodes", "-subj", "/CN=" + hostname,
                      "-keyout", str(stem) + ".key", "-out", str(stem) + ".csr"])
        evidence.run(["openssl", "x509", "-req", "-in", str(stem) + ".csr",
                      "-CA", str(directory / purpose / "internal-ca.crt"), "-CAkey", str(directory / purpose / "internal-ca.key"),
                      "-set_serial", "0x" + secrets.token_hex(16), "-days", "1", "-extfile", str(extensions), "-out", str(stem) + ".crt"])
        certificate = Path(str(stem) + ".crt").read_text()
        certificate_stem = "internal-server" if internal else "public"
        plan["pairs"].append({"cert": str(directory / service / (certificate_stem + ".crt")),
                              "key": str(directory / service / (certificate_stem + ".key")),
                              "new_cert": base64.b64encode(certificate.encode()).decode(),
                              "new_key": base64.b64encode(Path(str(stem) + ".key").read_bytes()).decode()})
        cases = {"api": [("API internal mTLS", 8443, "https")],
                 "imap-gateway": [("IMAP implicit TLS", 993, "imap")],
                 "pop3-gateway": [("POP3 implicit TLS", 995, "pop3")],
                 "smtp-gateway": [("SMTP submission TLS", 465, "smtp"), ("SMTP STARTTLS", 25, "starttls"),
                                  ("API HTTPS front", 443, "https")]}
        for name, port, protocol in cases[service]:
            published = endpoint(front if port == 443 else running[service], port)
            listener = {"name": name, "address": "127.0.0.1", "port": int(published.rsplit(":", 1)[1]),
                        "protocol": protocol, "hostname": hostname, "ca": str(directory / purpose / "internal-ca.crt"),
                        "expected_sha256": hashlib.sha256(ssl.PEM_cert_to_DER_cert(certificate)).hexdigest()}
            if internal:
                listener.update(client_cert=str(directory / "imap-gateway/internal-imap.crt"),
                                client_key=str(directory / "imap-gateway/internal-imap.key"))
            plan["listeners"].append(listener)
    tls_reload_probe.inside(plan)
    for container in (*running.values(), front):
        if container.endswith(("-api", "-smtp-gateway", "-imap-gateway", "-pop3-gateway", "-https-front")):
            logs = evidence.run(["docker", "logs", container])
            if (logs.stdout + logs.stderr).count("certificate reload failed") < 2:
                raise ValueError(f"{container} did not report both missing and invalid replacement")
    print("release-evidence tls-reload listeners=6 phases=5 missing=reported invalid=reported", flush=True)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--directory", type=Path, required=True)
    parser.add_argument("--project", required=True)
    args = parser.parse_args(argv)
    if not re.fullmatch(r"restmail-[a-z0-9-]+", args.project):
        parser.error("--project must be a unique restmail-<run> name")
    builds = json.loads((args.directory / "builds.json").read_text())
    if set(builds["images"]) != set(evidence.RELEASE_IMAGES):
        parser.error("all seven release images must have built successfully")
    evidence.invalidate_fresh_start(args.directory, builds["commit"])
    network = args.project + "-network"
    containers = []
    network_created = False
    label = "restmail-release-evidence=" + args.project
    result = 1
    with tempfile.TemporaryDirectory(prefix=args.project + "-") as temporary:
        directory = Path(temporary)
        directory.chmod(0o755)
        try:
            evidence.run(["docker", "network", "create", "--label", label, network])
            network_created = True
            for purpose, hostname in (("internal", "api"), ("public", "mail.release.test")):
                evidence.run(["go", "run", "./cmd/instance", "mtls-issue", "-out", str(directory / purpose),
                              "-server-dns", hostname], cwd=evidence.ROOT)
            secret = secrets.token_hex(32)
            common = {"ENVIRONMENT": "production", "DB_HOST": "postgres", "DB_NAME": "restmail_test",
                      "DB_USER": "restmail", "DB_PASS": secret, "DB_ALLOW_INSECURE": "true",
                      "JWT_SECRET": secret, "MASTER_KEY": secret, "SRS_SECRET": secret,
                      "JS_FILTER_TOKEN": secret, "GATEWAY_HOSTNAME": "mail.release.test",
                      "RESTMAIL_BOOTSTRAP_ADMIN_ADDRESS": "admin@mail.release.test",
                      "RESTMAIL_BOOTSTRAP_ADMIN_PASSWORD": secret,
                      "INTERNAL_MTLS_ENABLED": "true", "API_BASE_URL": "http://api:8080",
                      "API_INTERNAL_BASE_URL": "https://api:8443", "INTERNAL_MTLS_CA_CERT": "/certs/internal-ca.crt",
                      "TLS_CERT_PATH": "/certs/public.crt", "TLS_KEY_PATH": "/certs/public.key"}

            def start(name, image, environment, ports=(), extra=(), command=()):
                container = args.project + "-" + name
                options = ["docker", "run", "-d", "--name", container, "--label", label,
                           "--network", network, "--network-alias", name]
                for port in ports:
                    options += ["-p", f"127.0.0.1::{port}"]
                for key, value in environment.items():
                    options += ["-e", f"{key}={value}"]
                evidence.run([*options, *extra, image, *command])
                containers.append(container)
                return container

            postgres = start("postgres", "postgres:16-alpine", {
                "POSTGRES_USER": "restmail", "POSTGRES_PASSWORD": secret, "POSTGRES_DB": "restmail_test"},
                extra=["--tmpfs", "/var/lib/postgresql/data"], command=["-c", "shared_preload_libraries=pg_stat_statements",
                                                                       "-c", "pg_stat_statements.max=100000"])
            for attempt in range(120):
                ready = subprocess.run(["docker", "exec", postgres, "pg_isready", "-U", "restmail"], capture_output=True)
                if ready.returncode == 0:
                    break
                time.sleep(1)
            else:
                raise ValueError("disposable PostgreSQL did not become ready")

            def certs(name, identities):
                target = directory / name
                target.mkdir(mode=0o755)
                for filename in ("internal-ca.crt", *identities):
                    shutil.copyfile(directory / "internal" / filename, target / filename)
                shutil.copyfile(directory / "public/internal-server.crt", target / "public.crt")
                shutil.copyfile(directory / "public/internal-server.key", target / "public.key")
                for path in target.iterdir():
                    path.chmod(0o644)
                return ["-v", f"{target}:/certs:ro"]

            running = {}
            running["js-filter"] = start("js-filter", builds["images"]["js-filter"], {"JS_FILTER_TOKEN": secret})
            running["api"] = start("api", builds["images"]["api"], {
                **common, "API_TLS_TERMINATED_BY_PROXY": "true", "JS_FILTER_URL": "http://js-filter:3100",
                "INTERNAL_MTLS_SERVER_CERT": "/certs/internal-server.crt", "INTERNAL_MTLS_SERVER_KEY": "/certs/internal-server.key"},
                ports=[8080, 8443], extra=certs("api", ["internal-server.crt", "internal-server.key"]))
            api_endpoint = endpoint(running["api"], 8080)
            wait_http(api_endpoint + "/api/health")
            for name, identity, port in (("smtp-gateway", "smtp-delivery", 465), ("imap-gateway", "imap", 993), ("pop3-gateway", "pop3", 995)):
                identities = [f"internal-{identity}.{extension}" for extension in ("crt", "key")]
                environment = {**common, "INTERNAL_MTLS_CLIENT_CERT": f"/certs/internal-{identity}.crt",
                               "INTERNAL_MTLS_CLIENT_KEY": f"/certs/internal-{identity}.key"}
                if name == "smtp-gateway":
                    identities += ["internal-smtp-submission.crt", "internal-smtp-submission.key"]
                    environment.update(INTERNAL_MTLS_SUBMISSION_CERT="/certs/internal-smtp-submission.crt",
                                       INTERNAL_MTLS_SUBMISSION_KEY="/certs/internal-smtp-submission.key")
                running[name] = start(name, builds["images"][name], environment, ports=[port, 25] if name == "smtp-gateway" else [port], extra=certs(name, identities))
                context = ssl.create_default_context(cafile=str(directory / "public/internal-ca.crt"))
                address = endpoint(running[name], port).removeprefix("http://")
                host, port_number = address.rsplit(":", 1)
                for attempt in range(120):
                    try:
                        with socket.create_connection((host, int(port_number)), timeout=5) as connection:
                            with context.wrap_socket(connection, server_hostname="mail.release.test"):
                                break
                    except OSError:
                        time.sleep(1)
                else:
                    raise ValueError(f"{name} never completed a verified TLS handshake")
            for name, path in (("webmail", "/webmail/"), ("admin", "/admin/")):
                running[name] = start(name, builds["images"][name], {}, ports=[8080])
                wait_http(endpoint(running[name], 8080) + path)
            front = start("https-front", FRONT_IMAGE, {}, ports=[443],
                          extra=["-v", f"{directory / 'smtp-gateway'}:/certs:ro", "--entrypoint", "sh"],
                          command=["-c", (evidence.ROOT / "tasks/tls-front-reload.sh").read_text(), "tls-front",
                                   "events {}\nstream { server { listen 443 ssl; ssl_certificate /certs/public.crt; ssl_certificate_key /certs/public.key; proxy_pass api:8080; } }\n",
                                   "/certs/public.crt", "/certs/public.key"])
            reload_tls(directory, running, front)
            result = evidence.fresh_start(SimpleNamespace(directory=args.directory, endpoint=api_endpoint,
                                                          container=[f"{name}={container}" for name, container in running.items()]))
        except (OSError, ValueError, subprocess.CalledProcessError, evidence.cluster_e2e.StepFailed) as error:
            print(f"release-fresh-start failed: {error}", file=sys.stderr)
        finally:
            for container in containers:
                logs = subprocess.run(["docker", "logs", container], text=True, capture_output=True)
                (args.directory / f"{container}.log").write_text(logs.stdout + logs.stderr)
            for container in reversed(containers):
                subprocess.run(["docker", "rm", "-fv", container], capture_output=True)
            if network_created:
                subprocess.run(["docker", "network", "rm", network], capture_output=True)
    return result


if __name__ == "__main__":
    sys.exit(main())
