#!/usr/bin/env python3
"""Stages 1 to 3 of the installed-cluster check (#1750): drive a fresh Helm install.

It runs against a cluster where helm/restmail is already installed, with the
production images. Stage 1 reaches it through a kubectl port-forward to each
Service; stage 2 through the install's Ingress, the way a client outside the
cluster does. The same command runs in .github/workflows/cluster-e2e.yml and
locally as `chore cluster:e2e`. Stage 1 needs python3, kubectl, openssl and the
cluster, and nothing else: no Go toolchain, no repository build. Stage 2 adds PyYAML and
the pinned Vertrag (scripts/vertrag_pin.py installs it). The API description
both check is the one the running install serves.

Each step tests REST Mail's wiring, not the components' own behaviour:

  health        GET /api/health answers 200
  description   GET /api/docs/openapi.json is the install's OpenAPI description
  admin-sign-in the bootstrap admin (admin@ the mail domain, the password in the
                chart's API Secret) signs in through the API, chooses its own
                password, enrolls an authenticator and reaches a full session
  mailbox       that admin makes a mailbox through the API
  domain-live   that admin gives the bootstrap domain a certificate of its own
                and makes it live, as an operator does first: a fresh install's
                domain is not live, and refuses mail, until then (#289)
  smtp-rcpt     the installed SMTP gateway accepts RCPT for the hosted mailbox
                and refuses an address on a domain it does not host
  imap-sign-in  the installed IMAP gateway signs the mailbox in through protocol
                sign-in (LOGIN), then SELECT INBOX succeeds
  filter-validate
                the API validates a JavaScript filter, which only the js-filter
                sidecar can answer
  contract-examples
                stage 2: Vertrag's examples phase sent through the Ingress
                (--ingress-url) against the description the install serves
                there, which must be the one the pod served in `description`.
                It is narrowed to what a fresh install answers without fixture
                data or a credential: the operations the description marks
                public, read-only (GET) and under the Ingress's API path, less
                the gateways' machine routes, which are only on the internal
                mTLS listener. The rest of the examples need the E2E seed
                (fixture principals and data, tests/contract/fixtures), which a
                production image does not carry. Reports (HTML, JUnit, JSON,
                Markdown, HAR) go to <report-dir>/contract/.
  dkim-key      stage 3: the admin gives the mail domain a DKIM key through the API
                (PUT /api/v1/admin/dkim/{id}); its public key is published in the
                domain's zone in the cluster's DNS, and the API's DNS check
                (GET /api/v1/admin/domains/{id}/dns) reads the same record back.
                From here the install signs what it sends.
  mail-out      stage 3: the bootstrap admin sends a message through the API to
                the reference mail server (scripts/cluster_reference.py,
                --reference-namespace); it arrives in the reference mailbox,
                read over the reference's IMAP. REST Mail found the reference
                by its MX in the cluster's DNS and delivered it over SMTP.
  mail-in       stage 3: the reference submits a message (its submission port,
                STARTTLS, AUTH) to the mailbox made in `mailbox`; the reference
                delivers it to REST Mail's MX, and it is read back through
                REST Mail's IMAP gateway. REST Mail greylists the
                reference's first attempt (451), so the message arrives on
                the reference's retry, after the greylist delay
  pop3-retrieve stage 3: that message is retrieved through REST Mail's POP3
                gateway (USER/PASS, then RETR)
  dkim-receive  stage 3: the message the reference received in mail-out carries a
                DKIM signature by the mail domain, under dkim-key's selector, that
                verifies (the pinned dkimpy) against the key the cluster's DNS
                publishes, as the install's DNS check read it
  restmail-peer stage 3: a second install (--peer-namespace, values-ci-peer.yaml)
                signs its own bootstrap admin in and makes a mailbox; the first
                install's admin sends to it through the API, and it is read back
                over the second install's IMAP gateway. It went over RESTMAIL: the
                first install's queue logged the RESTMAIL delivery to the peer's
                /restmail/messages for that recipient, and nothing fell back to
                SMTP; the message carries no Received field the peer's SMTP
                gateway stamps. A peer identified as REST Mail is never
                downgraded to SMTP (#291).

Every step prints one receipt line: the commit, the images the pods run, the
step and its result. Receipts also go to <report-dir>/receipts.txt.

The bootstrap admin's password is spent by the first run (the admin must choose
its own), so a second run against the same install fails at admin-sign-in: run
it on a fresh install.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import hmac
import imaplib
import json
import os
import poplib
import re
import secrets
import smtplib
import ssl
import struct
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request
from pathlib import Path

STEPS = (
    "health",
    "description",
    "admin-sign-in",
    "mailbox",
    "domain-live",
    "smtp-rcpt",
    "imap-sign-in",
    "filter-validate",
    "contract-examples",
    "dkim-key",
    "mail-out",
    "mail-in",
    "pop3-retrieve",
    "dkim-receive",
    "restmail-peer",
)

ROOT = Path(__file__).resolve().parents[1]

# The path the chart's API Ingress routes to the API (api.ingress.path), and
# the prefix of every API path the description documents.
API_PATH = "/api"

# The Services the chart makes and the port each one is reached on.
SERVICES = {"api": 8080, "smtp-gateway": 25, "imap-gateway": 993, "pop3-gateway": 995}

# The reference mail server's Secret and Service (scripts/cluster_reference.py)
# and the ports reached on it: submission with STARTTLS, and IMAP over TLS.
REFERENCE_SECRET, REFERENCE_SERVICE = "reference-mail", "mail"
REFERENCE_PORTS = {"submission": 587, "imaps": 993}
# How long a fresh install greylists a new sender/recipient/IP triple
# (internal/pipeline/templates.go, delay_minutes). The reference's first
# delivery in mail-in is deferred with a 451 for this long before its retry
# is accepted, so mail-in waits this on top of --mail-timeout.
GREYLIST_DELAY = 300

# The first install's queue logs this line, with the url and the recipient,
# when a RESTMAIL delivery succeeds (internal/gateway/queue/worker.go); and
# this one when a RESTMAIL hop failed and the message goes over SMTP instead.
RESTMAIL_DELIVERED = "queue: RESTMAIL delivery succeeded"
RESTMAIL_FELL_BACK = "queue: RESTMAIL delivery failed, falling back to SMTP"

# A recipient on a domain no install hosts (RFC 2606 reserves .invalid).
UNHOSTED = "nobody@unhosted.invalid"

# The shape the sidecar's own tests use, and one it cannot parse: only the
# sidecar tells the two apart.
FILTER_SCRIPT = 'function filter(email) { return { type: "action", action: "continue", log: { detail: "ok" } }; }'
BROKEN_FILTER = "function filter(email) { return {"


class StepFailed(Exception):
    """A step's check did not hold."""


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        prog="cluster_e2e.py",
        description="Stages 1 to 3 of the installed-cluster check (#1750) against a running Helm install.",
    )
    parser.add_argument("--namespace", default=os.environ.get("NAMESPACE", "restmail"))
    parser.add_argument("--release", default=os.environ.get("RELEASE", "restmail"),
                        help="the chart's fullname: Services are <release>-api, <release>-smtp-gateway, ...")
    parser.add_argument("--report-dir", default=os.environ.get("REPORT_DIR", "cluster-report"))
    parser.add_argument("--commit", default=os.environ.get("GITHUB_SHA", ""),
                        help="the commit the images were built from (default: GITHUB_SHA, else git HEAD)")
    parser.add_argument("--kubectl", default=os.environ.get("KUBECTL", "kubectl"))
    parser.add_argument("--timeout", type=int, default=60,
                        help="seconds to wait for each port-forward and for the API to answer")
    parser.add_argument("--only", action="append", choices=STEPS, metavar="STEP",
                        help="run only these steps (repeatable); later steps need earlier ones' state")
    parser.add_argument("--ingress-url", default=os.environ.get("INGRESS_URL", ""),
                        help="the install's API Ingress, e.g. https://mail.restmail-ci.test (contract-examples)")
    parser.add_argument("--ingress-ca", default=os.environ.get("INGRESS_CA", ""),
                        help="PEM certificate to trust for the Ingress's TLS, besides the system roots")
    parser.add_argument("--vertrag", default=os.environ.get("CONTRACT_VERTRAG", ""),
                        help="the Vertrag binary (default: the pinned one, from scripts/vertrag_pin.py ensure)")
    parser.add_argument("--reference-namespace", default=os.environ.get("REFERENCE_NAMESPACE", ""),
                        help="where scripts/cluster_reference.py deployed the reference mail server (stage 3)")
    parser.add_argument("--peer-namespace", default=os.environ.get("PEER_NAMESPACE", ""),
                        help="where the second install runs (restmail-peer)")
    parser.add_argument("--peer-release", default=os.environ.get("PEER_RELEASE", "restmail"))
    parser.add_argument("--mail-timeout", type=int, default=240,
                        help="seconds to wait for a message to arrive on the other side (stage 3)")
    args = parser.parse_args(argv)
    if args.timeout <= 0 or args.mail_timeout <= 0:
        parser.error("--timeout and --mail-timeout must be positive")
    if not args.release or "/" in args.release:
        parser.error("--release must be a name")
    if args.ingress_url and not re.match(r"https?://[^/]+$", args.ingress_url.rstrip("/")):
        parser.error("--ingress-url must be a scheme and host, without a path")
    args.ingress_url = args.ingress_url.rstrip("/")
    if not args.commit:
        args.commit = git_head()
    return args


def find_domain(domains: list, name: str) -> int:
    """The id of the domain called name in the API's domain list."""
    for entry in domains or []:
        if isinstance(entry, dict) and str(entry.get("name", "")).lower() == name.lower():
            return int(entry["id"])
    raise StepFailed(f"the API lists no domain {name}")


def put_domain_live(api: "Api", token: str, cluster: "Cluster", domain: str) -> str:
    """Take a fresh install's domain live, as its operator would.

    The install creates its domain not live: it has no certificate of its own
    yet (issues #289, #290). Until it goes live, its mailboxes receive nothing
    (the inbound pipeline refuses them as unknown, 550 5.1.1). Upload the
    certificate the install already serves (the chart's TLS Secret), then turn
    the domain live, and read it back.
    """
    domain_id = find_domain(api.expect(200, "GET", "/api/v1/admin/domains", token=token).get("data"), domain)
    secret = cluster.tls_secret()
    cert = cluster.secret_value(cluster.namespace, secret, "tls\\.crt")
    key = cluster.secret_value(cluster.namespace, secret, "tls\\.key")
    api.expect(201, "POST", "/api/v1/admin/certificates",
               {"domain_id": domain_id, "cert_pem": cert, "key_pem": key, "auto_renew": False}, token)
    api.expect(200, "PATCH", f"/api/v1/admin/domains/{domain_id}", {"active": True}, token)
    live = api.expect(200, "GET", f"/api/v1/admin/domains/{domain_id}", token=token).get("data") or {}
    if live.get("active") is not True:
        raise StepFailed(f"{domain} is not live after PATCH active=true: {json.dumps(live)[:200]}")
    return f"domain={domain},id={domain_id},certificate={secret},active=true"


def git_head() -> str:
    try:
        out = subprocess.run(["git", "rev-parse", "HEAD"], capture_output=True, text=True, check=True)
        return out.stdout.strip() or "unknown"
    except (OSError, subprocess.CalledProcessError):
        return "unknown"


def receipt(commit: str, images: str, step: str, result: str, detail: str = "") -> str:
    """One receipt line. Values never contain spaces, so the line splits on them."""
    def clean(value: str) -> str:
        return re.sub(r"\s+", "_", value.strip()) or "-"
    line = f"cluster-e2e receipt commit={clean(commit)} images={clean(images)} step={clean(step)} result={clean(result)}"
    if detail:
        line += f" detail={clean(detail)}"
    return line


def image_summary(images: list[str]) -> str:
    """The pods' images as one token: name:tag, sorted, unique, comma-joined."""
    names = set()
    for ref in images:
        ref = ref.strip()
        if not ref:
            continue
        ref = ref.split("@", 1)[0]
        name = ref.rsplit("/", 1)[-1]
        if ":" not in name:
            name += ":latest"
        names.add(name)
    return ",".join(sorted(names)) or "unknown"


def totp(secret: str, at: float | None = None, step: int = 30, digits: int = 6) -> str:
    """RFC 6238 TOTP (SHA-1), as an authenticator app computes it."""
    key = base64.b32decode(secret.upper() + "=" * (-len(secret) % 8))
    counter = int((time.time() if at is None else at) // step)
    digest = hmac.new(key, struct.pack(">Q", counter), hashlib.sha1).digest()
    offset = digest[-1] & 0x0F
    code = (struct.unpack(">I", digest[offset:offset + 4])[0] & 0x7FFFFFFF) % (10 ** digits)
    return str(code).zfill(digits)


class Cluster:
    """kubectl against the install: what it runs, its Secret, and port-forwards."""

    def __init__(self, kubectl: str, namespace: str, release: str, report_dir: Path):
        self.kubectl, self.namespace, self.release = kubectl, namespace, release
        self.report_dir = report_dir
        self.forwards: list[subprocess.Popen] = []

    def run(self, *args: str) -> str:
        out = subprocess.run([self.kubectl, "-n", self.namespace, *args],
                             capture_output=True, text=True)
        if out.returncode != 0:
            raise StepFailed(f"kubectl {' '.join(args)}: {out.stderr.strip()}")
        return out.stdout

    def images(self) -> list[str]:
        return self.run("get", "pods", "-o", "jsonpath={.items[*].spec.containers[*].image}").split()

    def admin_address(self) -> str:
        return self.run("get", f"deployment/{self.release}-api", "-o",
                        "jsonpath={.spec.template.spec.containers[?(@.name==\"api\")].env"
                        "[?(@.name==\"RESTMAIL_BOOTSTRAP_ADMIN_ADDRESS\")].value}").strip()

    def admin_password(self) -> str:
        encoded = self.run("get", f"secret/{self.release}-api", "-o",
                           "jsonpath={.data.BOOTSTRAP_ADMIN_PASSWORD}").strip()
        if not encoded:
            raise StepFailed(f"secret/{self.release}-api holds no BOOTSTRAP_ADMIN_PASSWORD")
        return base64.b64decode(encoded).decode()

    def tls_secret(self) -> str:
        """The TLS Secret the install's SMTP gateway serves (the chart's tls.secretName)."""
        name = self.run("get", f"deployment/{self.release}-smtp-gateway", "-o",
                        "jsonpath={.spec.template.spec.volumes[?(@.name==\"tls\")].secret.secretName}").strip()
        if not name:
            raise StepFailed(f"deployment/{self.release}-smtp-gateway mounts no tls Secret")
        return name

    def secret_value(self, namespace: str, name: str, key: str) -> str:
        out = subprocess.run([self.kubectl, "-n", namespace, "get", f"secret/{name}", "-o",
                              f"jsonpath={{.data.{key}}}"], capture_output=True, text=True)
        if out.returncode != 0 or not out.stdout.strip():
            raise StepFailed(f"secret {namespace}/{name} holds no {key}: {out.stderr.strip()}")
        return base64.b64decode(out.stdout.strip()).decode()

    def forward(self, service: str, port: int, timeout: int, namespace: str = "", full_name: str = "") -> int:
        """Port-forward svc/<release>-<service>:<port> to a free local port; return it.

        With namespace and full_name, svc/<full_name> in that namespace instead.
        """
        namespace, target = namespace or self.namespace, full_name or f"{self.release}-{service}"
        log = open(self.report_dir / f"port-forward-{service}.log", "w")
        proc = subprocess.Popen(
            [self.kubectl, "-n", namespace, "port-forward", f"svc/{target}",
             f":{port}", "--address", "127.0.0.1"],
            stdout=subprocess.PIPE, stderr=log, text=True)
        self.forwards.append(proc)
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            line = proc.stdout.readline()
            if not line:
                break
            log.write(line)
            log.flush()
            local = forwarded_port(line)
            if local:
                # kubectl writes a line per connection from now on: keep
                # reading, so a full pipe never stalls the forward.
                threading.Thread(target=drain, args=(proc.stdout, log), daemon=True).start()
                return local
        raise StepFailed(f"port-forward to svc/{target}:{port} in {namespace} did not start "
                         f"(see port-forward-{service}.log)")

    def close(self) -> None:
        for proc in self.forwards:
            proc.terminate()
        for proc in self.forwards:
            try:
                proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                proc.kill()


def drain(stream, log) -> None:
    for line in stream:
        log.write(line)
        log.flush()


def forwarded_port(line: str) -> int | None:
    """The local port in kubectl's "Forwarding from 127.0.0.1:NNNN -> PPP" line."""
    match = re.match(r"Forwarding from 127\.0\.0\.1:(\d+) -> \d+", line.strip())
    return int(match.group(1)) if match else None


def self_signed_certificate(domain: str) -> tuple[str, str]:
    """A short-lived certificate naming domain, and its key, in PEM."""
    with tempfile.TemporaryDirectory() as directory:
        cert, key = Path(directory) / "tls.crt", Path(directory) / "tls.key"
        try:
            subprocess.run(
                ["openssl", "req", "-x509", "-nodes", "-newkey", "rsa:2048", "-days", "2",
                 "-subj", f"/CN={domain}", "-addext", f"subjectAltName=DNS:{domain}",
                 "-keyout", str(key), "-out", str(cert)],
                check=True, capture_output=True, text=True,
            )
        except (OSError, subprocess.CalledProcessError) as error:
            raise StepFailed(f"openssl could not make a certificate for {domain}: {error}") from error
        return cert.read_text(), key.read_text()


class Api:
    def __init__(self, base: str, ca: str = ""):
        self.base = base.rstrip("/")
        self.context = ssl.create_default_context(cafile=ca or None) if base.startswith("https:") else None

    def call(self, method: str, path: str, body: dict | None = None, token: str = "") -> tuple[int, dict]:
        data = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(self.base + path, data=data, method=method)
        if data is not None:
            request.add_header("Content-Type", "application/json")
        if token:
            request.add_header("Authorization", "Bearer " + token)
        try:
            with urllib.request.urlopen(request, timeout=30, context=self.context) as response:
                status, raw = response.status, response.read()
        except urllib.error.HTTPError as error:
            status, raw = error.code, error.read()
        try:
            return status, json.loads(raw) if raw else {}
        except ValueError:
            return status, {"raw": raw.decode(errors="replace")[:500]}

    def expect(self, want, method: str, path: str, body: dict | None = None, token: str = "") -> dict:
        """Call and require the status: an int, or "2xx" for any success."""
        status, answer = self.call(method, path, body, token)
        if not (status // 100 == 2 if want == "2xx" else status == want):
            raise StepFailed(f"{method} {path} answered {status}, want {want}: {json.dumps(answer)[:300]}")
        return answer

    def login(self, address: str, password: str, code: str = "") -> tuple[int, dict]:
        body = {"email": address, "password": password, "client": "native"}
        if code:
            body["totp_code"] = code
        return self.call("POST", "/api/v1/auth/login", body)


def contract_operations(description: dict, internal: set[str], api_path: str = API_PATH) -> list[str]:
    """The operations a fresh install answers through its Ingress with no data.

    Public (the operation's security, else the description's, is empty or
    allows no credential), read-only (GET), and under the Ingress's API path.
    The gateways' machine routes (internal) are left out: production serves
    them only on the internal mTLS listener, which no Ingress reaches.
    """
    default = description.get("security") or []
    chosen = set()
    for path, item in (description.get("paths") or {}).items():
        if path != api_path and not path.startswith(api_path + "/"):
            continue
        operation = item.get("get") if isinstance(item, dict) else None
        if not isinstance(operation, dict) or not operation.get("operationId"):
            continue
        security = operation.get("security", default)
        if security and {} not in security:
            continue
        if operation["operationId"] not in internal:
            chosen.add(operation["operationId"])
    return sorted(chosen)


def internal_operations(config: dict) -> set[str]:
    """The operations vertrag.yml sends to the internal listener (its endpoints)."""
    names = set()
    for entry in config.get("endpoints") or []:
        ids = entry.get("operation-id") or []
        names.update([ids] if isinstance(ids, str) else ids)
    return names


def contract_config(config: dict, configure, endpoint: str, spec: str, base: Path) -> dict:
    """vertrag.yml as stage 2 runs it, through tests/contract/reports.py's configure.

    The endpoint, the saved description and the report paths are set the way
    `CONTRACT_ENDPOINT=... chore contract` sets them, narrowed (no surface gate:
    it is about the whole API). What only the E2E topology has is taken out:
    the fixture admin's sign-in and the hooks, which make and read fixture data
    over docker exec. With no credential, there is nothing for the
    ignored-auth check to re-send.
    """
    config = configure(dict(config), ROOT, base, narrowed=True, endpoint=endpoint, spec=spec)
    for key in ("auth", "hookfiles", "language", "hooks-worker-timeout", "endpoints"):
        config.pop(key, None)
    config["checks"] = {**(config.get("checks") or {}), "ignored-auth": False}
    config["unproven"] = str(Path(base).resolve().parent / "unproven.json")
    return config


def vertrag_command(binary: str, config: Path, operations: list[str], ca: str = "") -> list[str]:
    """The examples phase, in the links' order, of the chosen operations only."""
    command = [binary, "run", "--config", str(config), "--sequence", "--phases", "examples"]
    if ca:
        command += ["--ca-cert", ca]
    for name in operations:
        command += ["--operation-id", name]
    return command


def junit_counts(path: Path) -> tuple[int, int, int]:
    """Transactions, failures (with errors) and skips in a JUnit report."""
    import xml.etree.ElementTree as ElementTree
    try:
        root = ElementTree.parse(path).getroot()
    except ElementTree.ParseError as error:
        raise StepFailed(f"{path.name} is not a JUnit report: {error}") from error
    suites = [root] if root.tag == "testsuite" else root.findall("testsuite")
    total = failed = skipped = 0
    for suite in suites:
        total += int(suite.get("tests", 0))
        failed += int(suite.get("failures", 0)) + int(suite.get("errors", 0))
        skipped += int(suite.get("skipped", 0))
    return total, failed, skipped


def load_reference_module():
    """scripts/cluster_reference.py, which serves the zones in the cluster's DNS."""
    import importlib.util
    spec = importlib.util.spec_from_file_location("cluster_reference", ROOT / "scripts/cluster_reference.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def dkimpy_version() -> str:
    """The dkimpy release verifying: the workflow's pin (DKIMPY_VERSION), else
    the installed distribution's."""
    if os.environ.get("DKIMPY_VERSION"):
        return os.environ["DKIMPY_VERSION"]
    try:
        from importlib.metadata import version
        return version("dkimpy")
    except Exception:  # an unpacked source tree has no metadata
        return "unknown"


def load_reports_module():
    """tests/contract/reports.py, the contract run's own vertrag.yml writer."""
    import importlib.util
    spec = importlib.util.spec_from_file_location("contract_reports", ROOT / "tests/contract/reports.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def mail_marker(direction: str) -> str:
    """A subject no other message has, so the receiving side finds this one."""
    return f"cluster-e2e {direction} {secrets.token_hex(8)}"


def submission_message(sender: str, recipient: str, subject: str) -> bytes:
    """The message the reference submits to REST Mail (mail-in)."""
    from email.message import EmailMessage
    from email.utils import formatdate, make_msgid
    message = EmailMessage()
    message["From"], message["To"], message["Subject"] = sender, recipient, subject
    message["Date"] = formatdate(localtime=False)
    message["Message-ID"] = make_msgid(domain=sender.split("@", 1)[1])
    message.set_content("Sent by the reference mail server to REST Mail (#1750 stage 3).\n")
    return message.as_bytes()


def received_with(raw: bytes, subject: str, sender: str) -> dict[str, str]:
    """The header fields that show raw is the message sent: its Subject and From.

    Raises StepFailed when raw is another message. The Received fields are
    returned (folded to one line each) for the receipt's evidence.
    """
    from email import message_from_bytes, policy
    message = message_from_bytes(raw, policy=policy.default)
    if str(message.get("Subject", "")).strip() != subject:
        raise StepFailed(f"the message found has Subject {message.get('Subject')!r}, want {subject!r}")
    if sender.lower() not in str(message.get("From", "")).lower():
        raise StepFailed(f"the message found is From {message.get('From')!r}, want {sender}")
    return {"received": " | ".join(" ".join(str(v).split()) for v in message.get_all("Received", []))}


def mail_in_wait(mail_timeout: int) -> int:
    """Seconds mail-in waits: the greylist delay the first attempt meets, then mail_timeout."""
    return GREYLIST_DELAY + mail_timeout


def bootstrap_session(api: "Api", cluster: "Cluster") -> tuple[str, str]:
    """Sign the install's bootstrap admin in as a person does the first time: its
    own password, an authenticator, then a full session. Returns the session's
    token and the admin's address."""
    address = cluster.admin_address()
    if "@" not in address:
        raise StepFailed("the API Deployment names no RESTMAIL_BOOTSTRAP_ADMIN_ADDRESS")
    boot = cluster.admin_password()
    status, answer = api.login(address, boot)
    if status != 200:
        raise StepFailed(f"bootstrap sign-in answered {status} (a second run on one install "
                         f"finds the bootstrap password spent): {json.dumps(answer)[:200]}")
    data = answer.get("data", {})
    if data.get("setup_required") != ["password", "two_factor"]:
        raise StepFailed(f"setup_required is {data.get('setup_required')!r}, want ['password', 'two_factor']")
    chosen = secrets.token_urlsafe(24)
    api.expect("2xx", "POST", "/api/v1/auth/password",
               {"current_password": boot, "new_password": chosen}, data["access_token"])
    status, answer = api.login(address, chosen)
    if status != 200:
        raise StepFailed(f"sign-in with the chosen password answered {status}")
    token = answer["data"]["access_token"]
    secret = api.expect("2xx", "POST", "/api/v1/auth/2fa/enroll", token=token)["data"]["secret"]
    api.expect(204, "POST", "/api/v1/auth/2fa/confirm", {"totp_code": totp(secret)}, token)
    # The confirming code may not be used twice: sign in with the next one.
    time.sleep(30 - time.time() % 30 + 1)
    status, answer = api.login(address, chosen, totp(secret))
    if status != 200 or answer.get("data", {}).get("setup_required"):
        raise StepFailed(f"full sign-in answered {status}: setup_required "
                         f"{answer.get('data', {}).get('setup_required')!r}")
    return answer["data"]["access_token"], address


def imap_read(port: int, address: str, password: str, subject: str, what: str) -> bytes | None:
    """The message with this Subject in address's INBOX over IMAP, else None."""
    imap = imaplib.IMAP4_SSL("127.0.0.1", port, ssl_context=insecure_tls(), timeout=60)
    try:
        imap.login(address, password)
        kind, data = imap.select("INBOX", readonly=True)
        if kind != "OK":
            raise StepFailed(f"{what}: SELECT INBOX answered {kind} {data!r}")
        return imap_find(imap, subject)
    except imaplib.IMAP4.error as error:
        raise StepFailed(f"{what}: {error}") from error
    finally:
        try:
            imap.logout()
        except (imaplib.IMAP4.error, OSError):
            pass


def wait_for(probe, timeout: float, interval: float = 5, clock=time.monotonic, sleep=time.sleep):
    """Call probe until it returns something other than None, or the timeout passes.

    Returns (result, seconds waited); (None, seconds) on a timeout. An error
    the probe raises ends the wait: only "not there yet" is retried.
    """
    start = clock()
    while True:
        result = probe()
        waited = clock() - start
        if result is not None or waited >= timeout:
            return result, waited
        sleep(interval)


def insecure_tls() -> ssl.SSLContext:
    """TLS without verification: the certificates here are self-signed for names
    only the cluster's DNS knows, and only the wiring is under test."""
    context = ssl.create_default_context()
    context.check_hostname = False
    context.verify_mode = ssl.CERT_NONE
    return context


def imap_find(imap: imaplib.IMAP4, subject: str) -> bytes | None:
    """The newest message in the selected mailbox with this Subject, else None."""
    kind, data = imap.uid("SEARCH", None, "SUBJECT", f'"{subject}"')
    if kind != "OK":
        raise StepFailed(f"UID SEARCH answered {kind} {data!r}")
    uids = (data[0] or b"").split() if data else []
    if not uids:
        return None
    kind, data = imap.uid("FETCH", uids[-1], "(BODY.PEEK[])")
    if kind != "OK":
        raise StepFailed(f"UID FETCH answered {kind} {data!r}")
    for part in data:
        if isinstance(part, tuple) and len(part) == 2:
            return part[1]
    raise StepFailed(f"UID FETCH {uids[-1].decode()} returned no message")


def pop3_find(messages: list[bytes], subject: str) -> int | None:
    """The 1-based number of the last POP3 message (its TOP lines) with this Subject."""
    from email.parser import BytesHeaderParser
    from email import policy
    found = None
    for number, head in enumerate(messages, 1):
        if str(BytesHeaderParser(policy=policy.default).parsebytes(head).get("Subject", "")).strip() == subject:
            found = number
    return found


def dkim_keypair(bits: int = 2048) -> tuple[str, str]:
    """A new RSA key from openssl: its PEM, and the DKIM TXT record of its
    public key (RFC 6376 3.6.1)."""
    key = subprocess.run(["openssl", "genpkey", "-algorithm", "RSA", "-pkeyopt", f"rsa_keygen_bits:{bits}"],
                         capture_output=True, text=True, check=True).stdout
    public = subprocess.run(["openssl", "pkey", "-pubout", "-outform", "DER"], input=key.encode(),
                            capture_output=True, check=True).stdout
    return key, "v=DKIM1; k=rsa; p=" + base64.b64encode(public).decode()


def dkim_record_check(check: dict, name: str) -> dict | None:
    """The DNS check's DKIM entry for this owner name, if it found the record."""
    for record in check.get("records") or []:
        if (record.get("type") == "DKIM" and record.get("name", "").rstrip(".").lower() == name.lower()
                and record.get("status") == "ok"):
            return record
    return None


def dkim_tags(raw: bytes) -> dict[str, str]:
    """The tags of the message's first DKIM-Signature field."""
    from email import message_from_bytes, policy
    field = message_from_bytes(raw, policy=policy.compat32).get("DKIM-Signature")
    if not field:
        raise StepFailed("the message has no DKIM-Signature field")
    tags = {}
    for part in re.sub(r"\s+", "", str(field)).split(";"):
        if "=" in part:
            key, value = part.split("=", 1)
            tags[key] = value
    return tags


def dkim_verify(raw: bytes, owner: str, record: str, verify=None) -> bool:
    """Whether raw's DKIM signature verifies with record as owner's TXT record.

    The record is given, not looked up: the runner cannot ask the cluster's
    DNS, so the record is the one the install's DNS check read from it. Any
    other name gets no record. verify defaults to the pinned dkimpy's.
    """
    if verify is None:
        try:
            import dkim
        except ImportError as error:
            raise StepFailed("needs dkimpy (the workflow pins it): " + str(error)) from error
        verify = dkim.verify
    wanted = owner.rstrip(".").lower()

    def lookup(name, timeout=5):
        text = name.decode() if isinstance(name, bytes) else name
        return record.encode() if text.rstrip(".").lower() == wanted else None

    return bool(verify(raw, dnsfunc=lookup))


def restmail_hops(log: str, recipient: str) -> tuple[list[str], list[str]]:
    """The RESTMAIL deliveries to recipient (their urls) and the SMTP fallbacks
    (their hosts) in the queue's JSON log lines."""
    delivered, fell_back = [], []
    for line in log.splitlines():
        try:
            entry = json.loads(line)
        except ValueError:
            continue
        if not isinstance(entry, dict):
            continue
        if entry.get("msg") == RESTMAIL_DELIVERED and str(entry.get("recipient", "")).lower() == recipient.lower():
            delivered.append(str(entry.get("url", "")))
        elif entry.get("msg") == RESTMAIL_FELL_BACK:
            fell_back.append(str(entry.get("host", "")))
    return delivered, fell_back


def smtp_stamps(raw: bytes, host: str) -> list[str]:
    """The Received fields that host's SMTP gateway stamped (by <host> ... with *SMTP*)."""
    from email import message_from_bytes, policy
    stamps = []
    for value in message_from_bytes(raw, policy=policy.compat32).get_all("Received", []):
        flat = " ".join(str(value).split())
        if re.search(rf"\bby {re.escape(host)}\b", flat, flags=re.I) and re.search(r"\bwith \S*SMTP", flat, flags=re.I):
            stamps.append(flat)
    return stamps


class Stages:
    def __init__(self, args: argparse.Namespace, cluster: Cluster):
        self.args, self.cluster = args, cluster
        self.report_dir = Path(args.report_dir)
        self.state: dict[str, object] = {}
        self.api: Api | None = None

    # ── steps ────────────────────────────────────────────────────────────
    def health(self) -> str:
        api = self.need_api()
        api.expect(200, "GET", "/api/health")
        return "200"

    def description(self) -> str:
        status, answer = self.need_api().call("GET", "/api/docs/openapi.json")
        if status != 200 or "openapi" not in answer:
            raise StepFailed(f"GET /api/docs/openapi.json answered {status} without an openapi field")
        (self.report_dir / "openapi.json").write_text(json.dumps(answer, indent=1))
        self.state["description"] = answer
        return f"openapi={answer['openapi']},paths={len(answer.get('paths', {}))}"

    def admin_sign_in(self) -> str:
        token, address = bootstrap_session(self.need_api(), self.cluster)
        self.state["token"] = token
        self.state["domain"] = address.split("@", 1)[1]
        self.state["admin"] = address
        return f"admin={address},session=full"

    def mailbox(self) -> str:
        token, domain = self.need("token"), self.need("domain")
        address, password = f"cluster-e2e@{domain}", secrets.token_urlsafe(18)
        self.need_api().expect(201, "POST", "/api/v1/accounts",
                               {"address": address, "password": password, "display_name": "Cluster E2E"}, token)
        self.state["mailbox"], self.state["mailbox_password"] = address, password
        return f"mailbox={address}"

    def domain_live(self) -> str:
        api, token, domain = self.need_api(), self.need("token"), self.need("domain")
        found = api.expect(200, "GET", "/api/v1/admin/domains", token=token)
        ids = [row["id"] for row in found["data"] if row["name"] == domain]
        if not ids:
            raise StepFailed(f"the install lists no domain named {domain}")
        cert_pem, key_pem = self_signed_certificate(domain)
        # The chart's install has no ACME account here, so nothing could renew it.
        api.expect(201, "POST", "/api/v1/admin/certificates",
                   {"domain_id": ids[0], "cert_pem": cert_pem, "key_pem": key_pem, "auto_renew": False}, token)
        answer = api.expect(200, "PATCH", f"/api/v1/admin/domains/{ids[0]}", {"active": True}, token)
        if answer["data"].get("active") is not True:
            raise StepFailed(f"making {domain} live answered active={answer['data'].get('active')!r}")
        self.state["domain_live"] = domain
        return f"domain={domain},active=true"

    def smtp_rcpt(self) -> str:
        # A mailbox on a domain that is not live is refused as unknown (#1848), so a
        # 550 without this says nothing about the gateway's wiring.
        self.need("domain_live")
        hosted = self.need("mailbox")
        port = self.cluster.forward("smtp-gateway", SERVICES["smtp-gateway"], self.args.timeout)
        with smtplib.SMTP("127.0.0.1", port, timeout=60) as smtp:
            smtp.ehlo("cluster-e2e.invalid")
            code, reply = smtp.mail("probe@cluster-e2e.invalid")
            if code != 250:
                raise StepFailed(f"MAIL FROM answered {code} {reply!r}")
            code, reply = smtp.rcpt(hosted)
            if code != 250:
                raise StepFailed(f"RCPT TO:<{hosted}> answered {code} {reply!r}, want 250")
            refused, reply = smtp.rcpt(UNHOSTED)
            if refused // 100 != 5:
                raise StepFailed(f"RCPT TO:<{UNHOSTED}> answered {refused} {reply!r}, want 5xx")
            smtp.rset()
        return f"hosted=250,unhosted={refused}"

    def imap_sign_in(self) -> str:
        address, password = self.need("mailbox"), self.need("mailbox_password")
        port = self.cluster.forward("imap-gateway", SERVICES["imap-gateway"], self.args.timeout)
        # The install's certificate is self-signed for a name nothing resolves;
        # stage 3 brings a testbed CA. Here only the gateway's wiring is tested.
        context = ssl.create_default_context()
        context.check_hostname = False
        context.verify_mode = ssl.CERT_NONE
        imap = imaplib.IMAP4_SSL("127.0.0.1", port, ssl_context=context, timeout=60)
        try:
            kind, _ = imap.login(address, password)
            if kind != "OK":
                raise StepFailed(f"LOGIN answered {kind}")
            kind, data = imap.select("INBOX")
            if kind != "OK":
                raise StepFailed(f"SELECT INBOX answered {kind} {data!r}")
            exists = data[0].decode() if data and data[0] else "?"
            imap.logout()
        except imaplib.IMAP4.error as error:
            raise StepFailed(f"IMAP: {error}") from error
        return f"login=OK,select=OK,exists={exists}"

    def filter_validate(self) -> str:
        token = self.need("token")
        verdicts = {}
        for name, script in (("valid", FILTER_SCRIPT), ("broken", BROKEN_FILTER)):
            answer = self.need_api().expect(200, "POST", "/api/v1/admin/custom-filters/validate",
                                            {"script": script}, token)
            verdicts[name] = answer.get("data", answer).get("valid") is True
        if verdicts != {"valid": True, "broken": False}:
            raise StepFailed(f"the sidecar's verdicts were {verdicts}, want valid=True, broken=False")
        return "valid=true,broken=false"

    def contract_examples(self) -> str:
        if not self.args.ingress_url:
            raise StepFailed("needs --ingress-url (INGRESS_URL), the install's API Ingress")
        import yaml  # stage 2 only: stage 1 needs nothing past the standard library
        out = self.report_dir / "contract"
        out.mkdir(parents=True, exist_ok=True)
        ingress = Api(self.args.ingress_url, self.args.ingress_ca)
        deadline = time.monotonic() + self.args.timeout
        while (status := ingress.call("GET", "/api/health")[0]) != 200:
            if time.monotonic() > deadline:
                raise StepFailed(f"GET {self.args.ingress_url}/api/health through the Ingress answered {status}")
            time.sleep(2)
        status, served = ingress.call("GET", "/api/docs/openapi.json")
        if status != 200 or "openapi" not in served:
            raise StepFailed(f"the Ingress answered {status} for /api/docs/openapi.json")
        if "description" in self.state and served != self.state["description"]:
            raise StepFailed("the Ingress serves a different description from the one the API pod served")
        spec = out / "openapi.json"
        spec.write_text(json.dumps(served, indent=1))
        base = yaml.safe_load((ROOT / "vertrag.yml").read_text())
        operations = contract_operations(served, internal_operations(base))
        if not operations:
            raise StepFailed("the description has no public read-only operation under " + API_PATH)
        config = contract_config(base, load_reports_module().configure, self.args.ingress_url, str(spec),
                                 out / "examples")
        config_path = out / "vertrag-examples.yml"
        config_path.write_text(yaml.safe_dump(config, sort_keys=False, allow_unicode=True))
        binary = self.args.vertrag or self.pinned_vertrag()
        command = vertrag_command(binary, config_path, operations, self.args.ingress_ca)
        with open(out / "examples.log", "w") as log:
            log.write("$ " + " ".join(command) + "\n")
            log.flush()
            code = subprocess.run(command, cwd=ROOT, stdout=log, stderr=subprocess.STDOUT).returncode
        total, failed, skipped = junit_counts(out / "examples.xml") if (out / "examples.xml").exists() else (0, 0, 0)
        detail = (f"operations={len(operations)},transactions={total},failed={failed},skipped={skipped},"
                  f"report=contract/examples.html")
        if code != 0 or failed or total == 0:
            raise StepFailed(f"Vertrag exited {code}: {detail} (see contract/examples.log)")
        return detail

    def dkim_key(self) -> str:
        token, domain = self.need("token"), self.need("domain")
        api = self.need_api()
        domain_id = find_domain(api.expect(200, "GET", "/api/v1/admin/domains", token=token).get("data"), domain)
        selector = "cluster" + secrets.token_hex(3)
        key, record = dkim_keypair()
        api.expect("2xx", "PUT", f"/api/v1/admin/dkim/{domain_id}", {"selector": selector, "private_key": key}, token)
        owner = f"{selector}._domainkey.{domain}"
        reference = load_reference_module()
        reference.publish_records(reference.Kubectl(self.args.kubectl, StepFailed), domain,
                                  {f"{selector}._domainkey": record}, f"{self.args.timeout * 5}s")

        def probe():
            check = api.expect(200, "GET", f"/api/v1/admin/domains/{domain_id}/dns", token=token).get("data", {})
            return dkim_record_check(check, owner)

        found, waited = wait_for(probe, self.args.mail_timeout)
        if found is None:
            raise StepFailed(f"the API's DNS check did not find {owner} within {self.args.mail_timeout}s")
        if "".join(str(found.get("value", "")).split()) != "".join(record.split()):
            raise StepFailed(f"the API's DNS check read {owner} as {found.get('value')!r}, not the published key")
        self.state["dkim"] = {"selector": selector, "owner": owner, "record": found["value"]}
        return f"domain={domain},selector={selector},record={owner},dns-check=ok,after={int(waited)}s"

    def dkim_receive(self) -> str:
        dkim = self.need("dkim")
        raw = self.need("mail_out_raw")
        domain = self.need("domain")
        tags = dkim_tags(raw)
        if tags.get("d", "").lower() != domain.lower() or tags.get("s") != dkim["selector"]:
            raise StepFailed(f"the signature is d={tags.get('d')} s={tags.get('s')}, "
                             f"want d={domain} s={dkim['selector']}")
        if not dkim_verify(raw, dkim["owner"], dkim["record"]):
            raise StepFailed(f"the DKIM signature on mail-out.eml does not verify against {dkim['owner']}")
        return (f"message=mail-out.eml,d={tags['d']},s={tags['s']},a={tags.get('a', '?')},"
                f"c={tags.get('c', 'simple/simple')},verified=dkimpy-{dkimpy_version()}")

    def restmail_peer(self) -> str:
        token, admin = self.need("token"), self.need("admin")
        if not self.args.peer_namespace:
            raise StepFailed("needs --peer-namespace (PEER_NAMESPACE), the second install")
        ns, release = self.args.peer_namespace, self.args.peer_release
        peer = Cluster(self.args.kubectl, ns, release, self.report_dir)
        port = self.cluster.forward("peer-api", SERVICES["api"], self.args.timeout, ns, f"{release}-api")
        peer_api = Api(f"http://127.0.0.1:{port}")
        wait_for(lambda: True if peer_api.call("GET", "/api/health")[0] == 200 else None, self.args.timeout, 2)
        peer_token, peer_admin = bootstrap_session(peer_api, peer)
        peer_domain = peer_admin.split("@", 1)[1]
        # Like the first install's (domain-live), the peer's domain starts not
        # live, and it would refuse the mailbox below as unknown.
        put_domain_live(peer_api, peer_token, peer, peer_domain)
        mailbox, password = f"cluster-e2e@{peer_domain}", secrets.token_urlsafe(18)
        peer_api.expect(201, "POST", "/api/v1/accounts",
                        {"address": mailbox, "password": password, "display_name": "Cluster E2E peer"}, peer_token)
        peer_host = peer.run("get", f"configmap/{release}-api", "-o", "jsonpath={.data.GATEWAY_HOSTNAME}").strip()
        if not peer_host:
            raise StepFailed(f"configmap {ns}/{release}-api names no GATEWAY_HOSTNAME")
        subject = mail_marker("peer")
        since = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() - 5))
        self.need_api().expect("2xx", "POST", "/api/v1/messages/send",
                               {"from": admin, "to": [mailbox], "subject": subject,
                                "body_text": "Sent by one REST Mail install to another (#1750 stage 3)."}, token)
        imap_port = self.cluster.forward("peer-imap-gateway", SERVICES["imap-gateway"], self.args.timeout,
                                         ns, f"{release}-imap-gateway")
        raw, waited = wait_for(lambda: imap_read(imap_port, mailbox, password, subject, "the peer's IMAP"),
                               self.args.mail_timeout)
        if raw is None:
            raise StepFailed(f"{mailbox} did not receive {subject!r} within {self.args.mail_timeout}s "
                             f"(see the first install's smtp-gateway log)")
        (self.report_dir / "restmail-peer.eml").write_bytes(raw)
        received_with(raw, subject, admin)
        log = self.cluster.run("logs", f"deployment/{self.args.release}-smtp-gateway", "--all-containers=true",
                               f"--since-time={since}")
        (self.report_dir / "restmail-peer-queue.log").write_text(log)
        delivered, fell_back = restmail_hops(log, mailbox)
        want = f"https://{peer_host}/restmail/messages"
        if fell_back:
            raise StepFailed(f"a RESTMAIL delivery fell back to SMTP (hosts {fell_back}): see restmail-peer-queue.log")
        if want not in delivered:
            raise StepFailed(f"the queue logged no RESTMAIL delivery to {want} for {mailbox} "
                             f"(found {delivered}): see restmail-peer-queue.log")
        stamps = smtp_stamps(raw, peer_host)
        if stamps:
            raise StepFailed(f"the peer's SMTP gateway stamped the message: {stamps[0][:200]}")
        return (f"from={admin},to={mailbox},after={int(waited)}s,via=RESTMAIL,url={want},smtp-fallback=none,"
                f"peer-smtp-received=none,read=peer-imap,message=restmail-peer.eml")

    def mail_out(self) -> str:
        token, admin = self.need("token"), self.need("admin")
        reference, password = self.reference_account()
        subject = mail_marker("out")
        self.need_api().expect("2xx", "POST", "/api/v1/messages/send",
                               {"from": admin, "to": [reference], "subject": subject,
                                "body_text": "Sent by REST Mail to the reference mail server (#1750 stage 3)."},
                               token)
        port = self.reference_forward("imaps")
        raw, waited = wait_for(lambda: self.reference_inbox(port, reference, password, subject),
                               self.args.mail_timeout)
        if raw is None:
            raise StepFailed(f"{reference} did not receive {subject!r} within {self.args.mail_timeout}s "
                             f"(see the smtp-gateway's log for the queue)")
        evidence = received_with(raw, subject, admin)
        (self.report_dir / "mail-out.eml").write_bytes(raw)
        self.state["mail_out_raw"] = raw
        return f"from={admin},to={reference},after={int(waited)}s,message=mail-out.eml,received={evidence['received'][:200]}"

    def mail_in(self) -> str:
        mailbox, mailbox_password = self.need("mailbox"), self.need("mailbox_password")
        reference, password = self.reference_account()
        subject = mail_marker("in")
        port = self.reference_forward("submission")
        with smtplib.SMTP("127.0.0.1", port, timeout=60) as smtp:
            smtp.ehlo("cluster-e2e.invalid")
            smtp.starttls(context=insecure_tls())
            smtp.ehlo("cluster-e2e.invalid")
            smtp.login(reference, password)
            refused = smtp.sendmail(reference, [mailbox], submission_message(reference, mailbox, subject))
            if refused:
                raise StepFailed(f"the reference refused {mailbox}: {refused}")
        imap_port = self.cluster.forward("imap-gateway", SERVICES["imap-gateway"], self.args.timeout)

        def probe():
            imap = imaplib.IMAP4_SSL("127.0.0.1", imap_port, ssl_context=insecure_tls(), timeout=60)
            try:
                imap.login(mailbox, mailbox_password)
                kind, data = imap.select("INBOX", readonly=True)
                if kind != "OK":
                    raise StepFailed(f"SELECT INBOX answered {kind} {data!r}")
                return imap_find(imap, subject)
            finally:
                try:
                    imap.logout()
                except (imaplib.IMAP4.error, OSError):
                    pass

        wait = mail_in_wait(self.args.mail_timeout)
        try:
            raw, waited = wait_for(probe, wait)
        except imaplib.IMAP4.error as error:
            raise StepFailed(f"REST Mail's IMAP gateway: {error}") from error
        if raw is None:
            raise StepFailed(f"{mailbox} did not receive {subject!r} over IMAP within {wait}s "
                             f"(the reference's log has each attempt; the first is greylisted)")
        evidence = received_with(raw, subject, reference)
        (self.report_dir / "mail-in.eml").write_bytes(raw)
        self.state["inbound_subject"] = subject
        return f"from={reference},to={mailbox},after={int(waited)}s,read=imap,message=mail-in.eml,received={evidence['received'][:200]}"

    def pop3_retrieve(self) -> str:
        mailbox, password = self.need("mailbox"), self.need("mailbox_password")
        subject = self.need("inbound_subject")
        port = self.cluster.forward("pop3-gateway", SERVICES["pop3-gateway"], self.args.timeout)
        try:
            pop = poplib.POP3_SSL("127.0.0.1", port, context=insecure_tls(), timeout=60)
            try:
                pop.user(mailbox)
                pop.pass_(password)
                count, _ = pop.stat()
                heads = [b"\r\n".join(pop.top(n, 0)[1]) for n in range(1, count + 1)]
                number = pop3_find(heads, subject)
                if number is None:
                    raise StepFailed(f"none of the {count} POP3 messages has Subject {subject!r}")
                raw = b"\r\n".join(pop.retr(number)[1])
            finally:
                pop.quit()
        except poplib.error_proto as error:
            raise StepFailed(f"REST Mail's POP3 gateway: {error}") from error
        received_with(raw, subject, self.reference_account()[0])
        return f"login=OK,stat={count},retr={number},bytes={len(raw)}"

    def reference_account(self) -> tuple[str, str]:
        if not self.args.reference_namespace:
            raise StepFailed("needs --reference-namespace (REFERENCE_NAMESPACE): run scripts/cluster_reference.py first")
        if "reference" not in self.state:
            ns = self.args.reference_namespace
            self.state["reference"] = (self.cluster.secret_value(ns, REFERENCE_SECRET, "address"),
                                       self.cluster.secret_value(ns, REFERENCE_SECRET, "password"))
        return self.state["reference"]

    def reference_forward(self, port_name: str) -> int:
        key = "reference_port_" + port_name
        if key not in self.state:
            self.state[key] = self.cluster.forward(f"reference-{port_name}", REFERENCE_PORTS[port_name],
                                                   self.args.timeout, self.args.reference_namespace,
                                                   REFERENCE_SERVICE)
        return self.state[key]

    def reference_inbox(self, port: int, address: str, password: str, subject: str) -> bytes | None:
        return imap_read(port, address, password, subject, "the reference's IMAP")

    def pinned_vertrag(self) -> str:
        out = subprocess.run([sys.executable, "-B", str(ROOT / "scripts/vertrag_pin.py"), "ensure",
                              str(ROOT / ".cache/bin")], capture_output=True, text=True)
        if out.returncode != 0 or not out.stdout.strip():
            raise StepFailed(f"the pinned Vertrag could not be installed: {out.stderr.strip()[-300:]}")
        return out.stdout.strip().splitlines()[-1]

    # ── plumbing ─────────────────────────────────────────────────────────
    def need_api(self) -> Api:
        if self.api is None:
            port = self.cluster.forward("api", SERVICES["api"], self.args.timeout)
            api = Api(f"http://127.0.0.1:{port}")
            deadline = time.monotonic() + self.args.timeout
            while api.call("GET", "/api/health")[0] != 200:
                if time.monotonic() > deadline:
                    raise StepFailed("the API did not answer /api/health through the port-forward")
                time.sleep(2)
            self.api = api
        return self.api

    def need(self, key: str):
        if key not in self.state:
            raise StepFailed(f"needs {key} from an earlier step")
        return self.state[key]


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    report_dir = Path(args.report_dir)
    report_dir.mkdir(parents=True, exist_ok=True)
    cluster = Cluster(args.kubectl, args.namespace, args.release, report_dir)
    stage = Stages(args, cluster)
    try:
        images = image_summary(cluster.images())
    except StepFailed as error:
        print(f"::error::{error}", file=sys.stderr)
        images = "unknown"
    selected = args.only or list(STEPS)
    failed = False
    with open(report_dir / "receipts.txt", "a") as receipts:
        try:
            for step in STEPS:
                if step not in selected:
                    continue
                if failed:
                    line = receipt(args.commit, images, step, "skipped", "an earlier step failed")
                else:
                    try:
                        detail = getattr(stage, step.replace("-", "_"))()
                        line = receipt(args.commit, images, step, "pass", detail)
                    except (StepFailed, OSError, smtplib.SMTPException, imaplib.IMAP4.error, poplib.error_proto,
                            KeyError, ImportError) as error:
                        failed = True
                        line = receipt(args.commit, images, step, "fail", str(error))
                        print(f"::error::{step}: {error}", file=sys.stderr)
                print(line, flush=True)
                receipts.write(line + "\n")
        finally:
            cluster.close()
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
