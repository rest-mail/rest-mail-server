"""Run an isolated API and SMTP worker against the existing desktop dev database."""

import argparse
import base64
import fcntl
import hashlib
import hmac
import json
import os
from pathlib import Path
import platform
import shlex
import socket
import subprocess
import struct
import time
import urllib.error
import urllib.request
from urllib.parse import urlparse


ROOT = Path(__file__).resolve().parents[2]
RUNTIME = "alpine:3.24.4@sha256:93b41ef40b1677fce8fcc64ff4af09daddfacf5040afde618bc46c1e2d1cc7d4"
FRONT = "nginxinc/nginx-unprivileged:1.30-alpine-slim"


def run(*args, **kwargs):
    return subprocess.run(args, check=True, text=True, capture_output=True, **kwargs).stdout.strip()


def saved_secrets(path):
    values = {}
    for line in path.read_text().splitlines():
        words = shlex.split(line, comments=True)
        for word in words:
            key, separator, value = word.partition("=")
            if separator and key in ("JWT_SECRET", "MASTER_KEY"):
                values[key] = value
    if set(values) != {"JWT_SECRET", "MASTER_KEY"} or not all(values.values()):
        raise RuntimeError(f"{path} must contain JWT_SECRET and MASTER_KEY")
    return values


def totp(secret, now):
    key = base64.b32decode(secret + "=" * (-len(secret) % 8))
    digest = hmac.new(key, struct.pack(">Q", int(now) // 30), hashlib.sha1).digest()
    offset = digest[-1] & 15
    value = struct.unpack(">I", digest[offset:offset + 4])[0] & 0x7fffffff
    return f"{value % 1000000:06d}"


def request(url, method="GET", body=None, token=None):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    data = json.dumps(body).encode() if body is not None else None
    try:
        with urllib.request.urlopen(urllib.request.Request(url, data=data, headers=headers, method=method), timeout=15) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        raise RuntimeError(f"dev API request failed: {method} {urlparse(url).path} HTTP {error.code}") from None


def mounts(directory, *names):
    return [argument for name in names for argument in ("--mount", f"type=bind,source={directory / name},target=/{name},readonly")]


def environment(source, certs="/certs"):
    return {
        **saved_secrets(source / "secrets.env"),
        "ENVIRONMENT": "development",
        "DB_HOST": "host.docker.internal", "DB_PORT": "55480",
        "DB_NAME": "restmail", "DB_USER": "restmail", "DB_PASS": "restmail",
        "DB_SSLMODE": "disable", "API_HOST": "0.0.0.0", "API_PORT": "8080",
        "GATEWAY_HOSTNAME": "desktop.test", "API_BASE_URL": "http://api:8080",
        "API_INTERNAL_BASE_URL": "https://api:8443",
        "INTERNAL_MTLS_ENABLED": "true", "INTERNAL_MTLS_PORT": "8443",
        "INTERNAL_MTLS_CA_CERT": f"{certs}/internal-ca.crt",
        "INTERNAL_MTLS_SERVER_CERT": f"{certs}/internal-server.crt",
        "INTERNAL_MTLS_SERVER_KEY": f"{certs}/internal-server.key",
        "INTERNAL_MTLS_CLIENT_CERT": f"{certs}/internal-smtp-delivery.crt",
        "INTERNAL_MTLS_CLIENT_KEY": f"{certs}/internal-smtp-delivery.key",
        "INTERNAL_MTLS_SUBMISSION_CERT": f"{certs}/internal-smtp-submission.crt",
        "INTERNAL_MTLS_SUBMISSION_KEY": f"{certs}/internal-smtp-submission.key",
        "TRUSTED_CA_CERT": f"{certs}/internal-ca.crt",
        "TLS_CERT_PATH": f"{certs}/internal-server.crt",
        "TLS_KEY_PATH": f"{certs}/internal-server.key",
        "SMTP_PORT_INBOUND": "25", "SMTP_PORT_SUBMISSION_TLS": "0",
        "SMTP_METRICS_PORT": "0", "QUEUE_POLL_INTERVAL": "250ms",
        "QUEUE_WORKERS": "1", "RESTMAIL_DELIVER_STRICT": "true",
    }


class Stack:
    def __init__(self, state, source, port):
        self.state = state.resolve()
        self.source = source.resolve()
        self.port = port
        self.label = "restmail-desktop-devstack=" + hashlib.sha256(str(self.state).encode()).hexdigest()[:12]
        self.manifest = self.state / "resources.json"

    def save(self, resources):
        temporary = self.state / "resources.tmp"
        temporary.write_text(json.dumps(resources))
        temporary.replace(self.manifest)

    def owned(self, kind, identifier):
        result = subprocess.run(["docker", kind, "inspect", identifier], text=True, capture_output=True)
        if result.returncode:
            return None
        item = json.loads(result.stdout)[0]
        labels = item.get("Config", {}).get("Labels", {}) if kind == "container" else item.get("Labels", {})
        key, value = self.label.split("=", 1)
        if labels.get(key) != value:
            raise RuntimeError(f"refusing to touch unowned {kind} {identifier}")
        return item

    def down(self):
        if not self.manifest.exists():
            return
        resources = json.loads(self.manifest.read_text())
        for identifier in reversed(resources["containers"]):
            if self.owned("container", identifier):
                run("docker", "container", "stop", "--time", "15", identifier)
                run("docker", "container", "rm", identifier)
        if resources.get("network") and self.owned("network", resources["network"]):
            run("docker", "network", "rm", resources["network"])
        self.save({"containers": []})

    def container(self, resources, name, *args):
        identifier = run("docker", "run", "-d", "--label", self.label,
                         "--name", resources["name"] + "-" + name, *args)
        resources["containers"].append(identifier)
        self.save(resources)
        return identifier

    def up(self):
        if self.manifest.exists():
            resources = json.loads(self.manifest.read_text())
            if resources["containers"]:
                running = [self.owned("container", identifier) for identifier in resources["containers"]]
                if len(running) == 3 and all(item and item["State"]["Running"] for item in running):
                    print(f"Desktop dev stack already up: {resources['url']}")
                    return
                raise RuntimeError("previous stack stopped or incomplete; run desktop:devstack:down first")
        with socket.create_connection(("127.0.0.1", 55480), timeout=3):
            pass
        with socket.socket() as listener:
            listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            listener.bind(("127.0.0.1", self.port))
        values = environment(self.source)
        arch = "arm64" if platform.machine() in ("arm64", "aarch64") else "amd64"
        build_env = {**os.environ, "CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": arch}
        for name, package in (("api", "./cmd/api"), ("smtp-gateway", "./cmd/smtp-gateway")):
            print(f"Building {name}", flush=True)
            run("go", "build", "-o", str(self.state / name), package, cwd=ROOT, env=build_env)
        run("go", "run", "./cmd/instance", "mtls-issue", "-out", str(self.state / "certs"),
            "-server-dns", "api,localhost,desktop.test", "-server-ip", "127.0.0.1", cwd=ROOT)
        for image in (RUNTIME, FRONT):
            if subprocess.run(["docker", "image", "inspect", image], capture_output=True).returncode:
                print(f"Pulling {image.split('@')[0]}", flush=True)
                run("docker", "pull", image)
        name = "restmail-desktop-" + self.label.split("=", 1)[1]
        resources = {"containers": [], "name": name, "url": f"http://127.0.0.1:{self.port}"}
        self.save(resources)
        try:
            resources["network"] = run("docker", "network", "create", "--label", self.label, name)
            self.save(resources)
            network = json.loads(run("docker", "network", "inspect", resources["network"]))[0]
            values["RESTMAIL_DELIVER_TRUSTED_CIDRS"] = network["IPAM"]["Config"][0]["Subnet"]
            env_file = self.state / "stack.env"
            env_file.write_text("".join(f"{key}={value}\n" for key, value in values.items()))
            env_file.chmod(0o600)
            common = ["--network", name, "--env-file", str(env_file), "--security-opt", "no-new-privileges:true", "--cap-drop", "ALL"]
            server_certs = ("certs/internal-ca.crt", "certs/internal-server.crt", "certs/internal-server.key")
            self.container(resources, "api", *common, *mounts(self.state, "api", *server_certs), "--network-alias", "api",
                           "-p", f"127.0.0.1:{self.port}:8080", "--entrypoint", "/api", RUNTIME)
            self.wait(resources)
            self.activate_domain(resources["url"])
            client_certs = ("certs/internal-smtp-delivery.crt", "certs/internal-smtp-delivery.key", "certs/internal-smtp-submission.crt", "certs/internal-smtp-submission.key")
            gateway = self.container(resources, "smtp", *common, *mounts(self.state, "smtp-gateway", *server_certs, *client_certs), "--network-alias", "desktop.test",
                                     "--entrypoint", "/smtp-gateway", RUNTIME)
            config = self.state / "https.conf"
            config.write_text("events {}\nstream { server { listen 443 ssl; "
                              "ssl_certificate /certs/internal-server.crt; "
                              "ssl_certificate_key /certs/internal-server.key; "
                              "proxy_pass api:8080; } }\n")
            self.container(resources, "https", "--network", f"container:{gateway}",
                           *mounts(self.state, "https.conf", *server_certs[1:]),
                           "--entrypoint", "nginx", FRONT, "-e", "/dev/stderr", "-c", "/https.conf",
                           "-g", "daemon off; pid /tmp/desktop-nginx.pid;")
            time.sleep(1)
            if not all(self.owned("container", identifier)["State"]["Running"] for identifier in resources["containers"]):
                raise RuntimeError("a stack container exited; inspect its recorded ID in resources.json")
        except BaseException:
            self.down()
            raise
        print(f"Desktop dev stack up: {resources['url']}")
        print(f"Resource IDs: {self.manifest}; existing database preserved, original API untouched")

    def activate_domain(self, url):
        login = request(url + "/api/v1/auth/login", "POST", {
            "email": "admin@desktop.test", "client": "native",
            "password": (self.source / "admin.pw").read_text().strip(),
            "totp_code": totp((self.source / "totp.secret").read_text().strip(), time.time()),
        })
        token = login["data"]["access_token"]
        domains = request(url + "/api/v1/admin/domains", token=token)["data"]
        domain = next((item for item in domains if item["name"] == "desktop.test"), None)
        if domain is None:
            raise RuntimeError("desktop.test is absent; seed the existing dev database first")
        if not domain["active"]:
            request(url + "/api/v1/admin/certificates", "POST", {
                "domain_id": domain["id"], "auto_renew": False,
                "cert_pem": (self.state / "certs/internal-server.crt").read_text(),
                "key_pem": (self.state / "certs/internal-server.key").read_text(),
            }, token)
            request(url + f"/api/v1/admin/domains/{domain['id']}", "PATCH", {"active": True}, token)
            print("Activated existing desktop.test with its local development certificate; credentials unchanged")
        pipelines = request(url + f"/api/v1/admin/pipelines?domain_id={domain['id']}", token=token)["data"]
        for pipeline in pipelines:
            if pipeline["domain_id"] != domain["id"] or pipeline["direction"] != "inbound":
                continue
            filters = pipeline["filters"]
            changed = False
            for item in filters:
                if item["name"] == "greylist" and item.get("enabled", False):
                    item["enabled"] = False
                    changed = True
            if changed:
                request(url + f"/api/v1/admin/pipelines/{pipeline['id']}", "PATCH", {"filters": filters}, token)
                print("Disabled only desktop.test inbound greylisting for immediate local test delivery")

    def wait(self, resources):
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            try:
                with urllib.request.urlopen(resources["url"] + "/api/health", timeout=2) as response:
                    if response.status == 200:
                        return
            except (OSError, urllib.error.URLError):
                time.sleep(0.25)
        raise RuntimeError("API did not become healthy within 90 seconds")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("up", "down"))
    parser.add_argument("--state", type=Path, default=Path("/tmp/restmail-desktop-devstack"))
    parser.add_argument("--source", type=Path, default=Path("/tmp/restmail-devserver"))
    parser.add_argument("--port", type=int, default=18081)
    args = parser.parse_args()
    if not 1024 <= args.port <= 65535 or args.port == 5432:
        parser.error("use an unprivileged port other than 5432")
    args.state.mkdir(mode=0o700, parents=True, exist_ok=True)
    args.state.chmod(0o700)
    with (args.state / "lock").open("w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        stack = Stack(args.state, args.source, args.port)
        try:
            getattr(stack, args.action)()
        except subprocess.CalledProcessError as error:
            raise SystemExit(f"devstack command failed ({error.returncode}): {error.cmd[0]}; output withheld to protect secrets") from None
        except (OSError, RuntimeError) as error:
            raise SystemExit(f"devstack: {error}") from None


if __name__ == "__main__":
    main()
