#!/usr/bin/env python3
"""Stages 1 and 2 of the installed-cluster check (#1750): drive a fresh Helm install.

It runs against a cluster where helm/restmail is already installed, with the
production images. Stage 1 reaches it through a kubectl port-forward to each
Service; stage 2 through the install's Ingress, the way a client outside the
cluster does. The same command runs in .github/workflows/cluster-e2e.yml and
locally as `chore cluster:e2e`. Stage 1 needs python3, kubectl and the cluster,
and nothing else: no Go toolchain, no repository build. Stage 2 adds PyYAML and
the pinned Vertrag (scripts/vertrag_pin.py installs it). The API description
both check is the one the running install serves.

Each step tests REST Mail's wiring, not the components' own behaviour:

  health        GET /api/health answers 200
  description   GET /api/docs/openapi.json is the install's OpenAPI description
  admin-sign-in the bootstrap admin (admin@ the mail domain, the password in the
                chart's API Secret) signs in through the API, chooses its own
                password, enrolls an authenticator and reaches a full session
  mailbox       that admin makes a mailbox through the API
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
import re
import secrets
import smtplib
import ssl
import struct
import subprocess
import sys
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
    "smtp-rcpt",
    "imap-sign-in",
    "filter-validate",
    "contract-examples",
)

ROOT = Path(__file__).resolve().parents[1]

# The path the chart's API Ingress routes to the API (api.ingress.path), and
# the prefix of every API path the description documents.
API_PATH = "/api"

# The Services the chart makes and the port each one is reached on.
SERVICES = {"api": 8080, "smtp-gateway": 25, "imap-gateway": 993}

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
        description="Stages 1 and 2 of the installed-cluster check (#1750) against a running Helm install.",
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
    args = parser.parse_args(argv)
    if args.timeout <= 0:
        parser.error("--timeout must be positive")
    if not args.release or "/" in args.release:
        parser.error("--release must be a name")
    if args.ingress_url and not re.match(r"https?://[^/]+$", args.ingress_url.rstrip("/")):
        parser.error("--ingress-url must be a scheme and host, without a path")
    args.ingress_url = args.ingress_url.rstrip("/")
    if not args.commit:
        args.commit = git_head()
    return args


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

    def forward(self, service: str, port: int, timeout: int) -> int:
        """Port-forward svc/<release>-<service>:<port> to a free local port; return it."""
        log = open(self.report_dir / f"port-forward-{service}.log", "w")
        proc = subprocess.Popen(
            [self.kubectl, "-n", self.namespace, "port-forward", f"svc/{self.release}-{service}",
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
        raise StepFailed(f"port-forward to svc/{self.release}-{service}:{port} did not start "
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


def load_reports_module():
    """tests/contract/reports.py, the contract run's own vertrag.yml writer."""
    import importlib.util
    spec = importlib.util.spec_from_file_location("contract_reports", ROOT / "tests/contract/reports.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


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
        api = self.need_api()
        address = self.cluster.admin_address()
        if "@" not in address:
            raise StepFailed("the API Deployment names no RESTMAIL_BOOTSTRAP_ADMIN_ADDRESS")
        boot = self.cluster.admin_password()
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
        self.state["token"] = answer["data"]["access_token"]
        self.state["domain"] = address.split("@", 1)[1]
        return f"admin={address},session=full"

    def mailbox(self) -> str:
        token, domain = self.need("token"), self.need("domain")
        address, password = f"cluster-e2e@{domain}", secrets.token_urlsafe(18)
        self.need_api().expect(201, "POST", "/api/v1/accounts",
                               {"address": address, "password": password, "display_name": "Cluster E2E"}, token)
        self.state["mailbox"], self.state["mailbox_password"] = address, password
        return f"mailbox={address}"

    def smtp_rcpt(self) -> str:
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
                    except (StepFailed, OSError, smtplib.SMTPException, KeyError, ImportError) as error:
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
