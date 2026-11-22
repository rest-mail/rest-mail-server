#!/usr/bin/env python3
"""AC02: replace real TLS material on an owned disposable chore topology."""

import argparse
import base64
import hashlib
import http.client
import json
import os
import secrets
import socket
import ssl
import subprocess
import sys
import tempfile
import time
from pathlib import Path

HELPER = "python:3.13-slim@sha256:e48f59cdba69a3cbd9ed2cf0f348d90d45a8d350d98134cf1070bc106ee9ed90"


def dialogue(connection, command, until):
    if command:
        connection.sendall(command.encode() + b"\r\n")
    with connection.makefile("rb") as reader:
        while True:
            line = reader.readline()
            if not line:
                raise ValueError(f"connection ended waiting for {until!r}")
            if line.startswith(until.encode()):
                return


def connect(listener):
    connection = socket.create_connection((listener["address"], listener["port"]), timeout=15)
    try:
        if listener["protocol"] == "starttls":
            dialogue(connection, "", "220 ")
            dialogue(connection, "EHLO reload.test", "250 ")
            dialogue(connection, "STARTTLS", "220 ")
        context = ssl.create_default_context(cafile=listener["ca"])
        if listener.get("client_cert"):
            context.load_cert_chain(listener["client_cert"], listener["client_key"])
        connection = context.wrap_socket(connection, server_hostname=listener["hostname"])
        if listener["protocol"] == "imap":
            dialogue(connection, "", "* OK")
        elif listener["protocol"] == "pop3":
            dialogue(connection, "", "+OK")
        elif listener["protocol"] == "smtp":
            dialogue(connection, "", "220 ")
        return connection
    except BaseException:
        connection.close()
        raise


def ping(connection, protocol):
    if protocol == "imap":
        dialogue(connection, "a2 NOOP", "a2 OK")
    elif protocol == "pop3":
        dialogue(connection, "CAPA", ".")
    elif protocol in ("smtp", "starttls"):
        dialogue(connection, "NOOP", "250 ")
    else:
        connection.sendall(b"GET /api/health HTTP/1.1\r\nHost: restmail.test\r\n\r\n")
        response = http.client.HTTPResponse(connection)
        response.begin()
        response.read()
        if response.status not in (200, 404):
            raise ValueError(f"HTTPS listener answered {response.status}")
        response.close()


def publish(path, data, metadata):
    replacement = path.with_name(path.name + ".reload-new")
    replacement.write_bytes(data)
    replacement.chmod(metadata.st_mode & 0o777)
    os.chown(replacement, metadata.st_uid, metadata.st_gid)
    replacement.replace(path)


def inside(plan):
    originals, established = {}, []
    try:
        for pair in plan["pairs"]:
            for key in ("cert", "key"):
                path = Path(pair[key])
                originals[str(path)] = (path.read_bytes(), path.stat())
        for listener in plan["listeners"]:
            connection = connect(listener)
            ping(connection, listener["protocol"])
            established.append((listener, connection))
        for pair in plan["pairs"]:
            for key in ("key", "cert"):
                publish(Path(pair[key]), base64.b64decode(pair["new_" + key]), originals[pair[key]][1])
        for phase in ("renewed", "missing", "repaired", "invalid", "recovered"):
            if phase in ("missing", "invalid"):
                for pair in plan["pairs"]:
                    path = Path(pair["cert"])
                    if phase == "missing":
                        path.rename(path.with_name(path.name + ".reload-held"))
                    else:
                        publish(path, b"invalid replacement", originals[str(path)][1])
            elif phase in ("repaired", "recovered"):
                for pair in plan["pairs"]:
                    publish(Path(pair["cert"]), base64.b64decode(pair["new_cert"]), originals[pair["cert"]][1])
            time.sleep(6)
            for listener, old in established:
                with connect(listener) as connection:
                    got = hashlib.sha256(connection.getpeercert(binary_form=True)).hexdigest()
                    if got != listener["expected_sha256"]:
                        raise ValueError(f"{listener['name']} {phase}: fingerprint {got}, want {listener['expected_sha256']}")
                    ping(connection, listener["protocol"])
                if listener["name"] != "API HTTPS front":
                    ping(old, listener["protocol"])
                print(f"tls-reload pass listener={listener['name']} phase={phase} name={listener['hostname']} leaf_sha256={got}", flush=True)
    finally:
        for _, connection in established:
            connection.close()
        for path, (data, metadata) in originals.items():
            held = Path(path).with_name(Path(path).name + ".reload-held")
            if held.exists():
                held.replace(path)
            publish(Path(path), data, metadata)


def run(command, **kwargs):
    return subprocess.run(command, text=True, capture_output=True, check=True, **kwargs)


def host(topology):
    import topology_plan
    topology_plan.validate(topology)
    names = topology_plan.names_for(topology)
    project, network = names["instance_project"], names["network"]
    label = f"rest-mail-loop={topology}"
    services, starts, mounts = {}, {}, []
    for service in ("api", "smtp-gateway", "imap-gateway", "pop3-gateway"):
        inspected = json.loads(run(["docker", "inspect", f"{project}-{service}"]).stdout)[0]
        if inspected["Config"]["Labels"].get("rest-mail-loop") != topology or not inspected["State"]["Running"]:
            raise ValueError(f"{service}: not a running container owned by {topology}")
        starts[service] = inspected["State"]["StartedAt"]
        services[service] = {"address": inspected["NetworkSettings"]["Networks"][network]["IPAddress"],
                             "environment": dict(value.split("=", 1) for value in inspected["Config"]["Env"] if "=" in value)}
        source = next(mount["Name"] for mount in inspected["Mounts"] if mount["Destination"] == "/certs")
        mounts += ["-v", f"{source}:/pairs/{service}"]
    plan = {"pairs": [], "listeners": []}
    with tempfile.TemporaryDirectory(prefix="tls-reload-" + topology) as temporary:
        directory = Path(temporary)
        for kind, volume, prefix in (("public", names["certs_volume"], "ca"), ("internal", project + "-internal-mtls", "internal-ca")):
            for extension in ("crt", "key"):
                material = run(["docker", "run", "--rm", "--name", project + "-tls-read", "--label", label,
                                "--network", "none", "-v", f"{volume}:/source:ro", "alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc", "cat", f"/source/{prefix}.{extension}"]).stdout
                (directory / f"{kind}.{extension}").write_text(material)
                (directory / f"{kind}.{extension}").chmod(0o600)
        for service, config in services.items():
            environment = config["environment"]
            internal = service == "api"
            kind = "internal" if internal else "public"
            hostname = config["address"] if internal else environment["GATEWAY_HOSTNAME"]
            cert = f"/pairs/{service}/" + Path(environment["INTERNAL_MTLS_SERVER_CERT" if internal else "TLS_CERT_PATH"]).name
            key = f"/pairs/{service}/" + Path(environment["INTERNAL_MTLS_SERVER_KEY" if internal else "TLS_KEY_PATH"]).name
            stem = directory / service
            extension = directory / "extensions"
            extension.write_text("basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nauthorityKeyIdentifier=keyid,issuer\nextendedKeyUsage=serverAuth\nsubjectAltName=" + ("IP:" if internal else "DNS:") + hostname + "\n")
            run(["openssl", "req", "-new", "-newkey", "rsa:2048", "-nodes", "-subj", "/CN=" + hostname,
                 "-keyout", str(stem) + ".key", "-out", str(stem) + ".csr"])
            run(["openssl", "x509", "-req", "-in", str(stem) + ".csr", "-CA", str(directory / f"{kind}.crt"),
                 "-CAkey", str(directory / f"{kind}.key"), "-set_serial", "0x" + secrets.token_hex(16), "-days", "1",
                 "-extfile", str(extension), "-out", str(stem) + ".crt"])
            certificate = Path(str(stem) + ".crt").read_text()
            fingerprint = hashlib.sha256(ssl.PEM_cert_to_DER_cert(certificate)).hexdigest()
            plan["pairs"].append({"cert": cert, "key": key, "new_cert": base64.b64encode(certificate.encode()).decode(),
                                  "new_key": base64.b64encode(Path(str(stem) + ".key").read_bytes()).decode()})
            listeners = {"api": [("API internal mTLS", int(environment.get("INTERNAL_MTLS_PORT", "8443")), "https")],
                         "imap-gateway": [("IMAP implicit TLS", int(environment.get("IMAP_TLS_PORT", "993")), "imap")],
                         "pop3-gateway": [("POP3 implicit TLS", int(environment.get("POP3_TLS_PORT", "995")), "pop3")],
                         "smtp-gateway": [("SMTP submission TLS", int(environment.get("SMTP_PORT_SUBMISSION_TLS", "465")), "smtp"),
                                          ("SMTP STARTTLS", int(environment.get("SMTP_PORT_INBOUND", "25")), "starttls"),
                                          ("API HTTPS front", 443, "https")]}
            for name, port, protocol in listeners[service]:
                listener = {"name": name, "address": config["address"], "port": port, "protocol": protocol,
                            "hostname": hostname, "expected_sha256": fingerprint,
                            "ca": f"/pairs/{service}/" + ("internal-ca.crt" if internal else "ca.crt")}
                if internal:
                    listener.update(client_cert="/pairs/imap-gateway/internal-imap.crt", client_key="/pairs/imap-gateway/internal-imap.key")
                plan["listeners"].append(listener)
        completed = subprocess.run(["docker", "run", "--rm", "-i", "--name", project + "-tls-reload", "--label", label,
                                    "--network", network, *mounts, HELPER, "python", "-c", Path(__file__).read_text(), "--inside"],
                                   input=json.dumps(plan), text=True)
        if completed.returncode:
            return completed.returncode
    for service in services:
        inspected = json.loads(run(["docker", "inspect", f"{project}-{service}"]).stdout)[0]
        if inspected["State"]["StartedAt"] != starts[service]:
            raise ValueError(f"{service}: restarted during the reload proof")
        logs = run(["docker", "logs", f"{project}-{service}"])
        if (logs.stdout + logs.stderr).count("certificate reload failed") < 2:
            raise ValueError(f"{service}: missing and invalid failures were not both reported")
    front_logs = run(["docker", "logs", project + "-smtp-gateway-https"])
    if (front_logs.stdout + front_logs.stderr).count("tls-front: certificate reload failed") < 2:
        raise ValueError("HTTPS front did not report both reload failures")
    print(f"tls-reload authorized topology={topology} listeners=6 phases=5 services_restarted=0")
    return 0


def main():
    if "--inside" in sys.argv:
        inside(json.load(sys.stdin))
        return 0
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--topology", required=True)
    args = parser.parse_args()
    try:
        return host(args.topology)
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print(f"tls-reload refused: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
