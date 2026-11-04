"""Stages 1 to 3 of the installed-cluster check (#1750), without a cluster.

The steps themselves need an install (cluster-e2e.yml runs them); these test
what does not: the arguments, the receipt lines, the TOTP the admin signs in
with, reading kubectl's port-forward line, what a failed step does to the rest,
stage 2's selection, Vertrag configuration and verdict (against a local HTTP
server standing in for the Ingress and a stand-in Vertrag), stage 3's
message matching and waiting, and that the workflow and `chore cluster:e2e`
run the same script.
"""

import http.server
import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import tempfile
import textwrap
import threading
import unittest
from contextlib import redirect_stderr, redirect_stdout
from types import SimpleNamespace
import unittest.mock
from unittest.mock import patch

import yaml

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts/cluster_e2e.py"

spec = importlib.util.spec_from_file_location("cluster_e2e", SCRIPT)
cluster_e2e = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cluster_e2e)


class ArgumentTests(unittest.TestCase):
    def parse(self, *argv, env=None):
        with patch.dict(os.environ, env or {}, clear=True):
            return cluster_e2e.parse_args(list(argv))

    def test_defaults_match_the_workflow(self):
        args = self.parse("--commit", "abc")
        self.assertEqual((args.namespace, args.release, args.report_dir, args.kubectl, args.timeout),
                         ("restmail", "restmail", "cluster-report", "kubectl", 60))
        self.assertIsNone(args.only)
        self.assertEqual((args.ingress_url, args.ingress_ca, args.vertrag), ("", "", ""))
        self.assertEqual((args.reference_namespace, args.mail_timeout), ("", 240))

    def test_reference_namespace_from_the_environment(self):
        self.assertEqual(self.parse("--commit", "c", env={"REFERENCE_NAMESPACE": "ref"}).reference_namespace, "ref")
        with redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
            self.parse("--commit", "c", "--mail-timeout", "0")

    def test_environment_supplies_what_the_workflow_sets(self):
        args = self.parse(env={"NAMESPACE": "ns", "RELEASE": "rel", "REPORT_DIR": "/r", "GITHUB_SHA": "deadbeef"})
        self.assertEqual((args.namespace, args.release, args.report_dir, args.commit), ("ns", "rel", "/r", "deadbeef"))
        args = self.parse("--commit", "c", env={"INGRESS_URL": "https://mail.example.test/", "INGRESS_CA": "/ca.pem",
                                                "CONTRACT_VERTRAG": "/bin/vertrag"})
        self.assertEqual((args.ingress_url, args.ingress_ca, args.vertrag),
                         ("https://mail.example.test", "/ca.pem", "/bin/vertrag"))

    def test_commit_falls_back_to_git_head(self):
        with patch.object(cluster_e2e, "git_head", return_value="fromgit"):
            self.assertEqual(self.parse().commit, "fromgit")

    def test_only_takes_known_steps(self):
        args = self.parse("--commit", "c", "--only", "health", "--only", "smtp-rcpt")
        self.assertEqual(args.only, ["health", "smtp-rcpt"])
        for bad in (["--only", "nope"], ["--timeout", "0"], ["--release", "a/b"],
                    ["--ingress-url", "https://mail.example.test/api"], ["--ingress-url", "mail.example.test"]):
            with self.subTest(bad=bad), redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                self.parse("--commit", "c", *bad)

    def test_every_check_is_a_step_and_stage_3_comes_last(self):
        self.assertEqual(cluster_e2e.STEPS, ("health", "description", "admin-sign-in", "mailbox",
                                             "domain-live", "smtp-rcpt", "imap-sign-in", "filter-validate",
                                             "contract-examples", "mail-out", "mail-in", "pop3-retrieve"))
        for step in cluster_e2e.STEPS:
            self.assertTrue(callable(getattr(cluster_e2e.Stages, step.replace("-", "_"))), step)


class ReceiptTests(unittest.TestCase):
    def test_receipt_line_names_commit_images_step_and_result(self):
        self.assertEqual(cluster_e2e.receipt("abc123", "api:ci,js-filter:ci", "health", "pass", "200"),
                         "cluster-e2e receipt commit=abc123 images=api:ci,js-filter:ci step=health result=pass detail=200")

    def test_receipt_keeps_one_token_per_field(self):
        line = cluster_e2e.receipt("", "x", "smtp-rcpt", "fail", "RCPT answered 451  try\nlater")
        self.assertEqual(line, "cluster-e2e receipt commit=- images=x step=smtp-rcpt result=fail "
                               "detail=RCPT_answered_451_try_later")
        self.assertNotIn("detail=", cluster_e2e.receipt("c", "i", "s", "pass"))

    def test_image_summary_is_name_and_tag_sorted_and_unique(self):
        self.assertEqual(cluster_e2e.image_summary([
            "ghcr.io/rest-mail/smtp-gateway:ci", "ghcr.io/rest-mail/api:ci", "ghcr.io/rest-mail/api:ci",
            "postgres:16-alpine@sha256:" + "0" * 64, "busybox", "",
        ]), "api:ci,busybox:latest,postgres:16-alpine,smtp-gateway:ci")
        self.assertEqual(cluster_e2e.image_summary([]), "unknown")


class TotpTests(unittest.TestCase):
    # RFC 6238 Appendix B, SHA-1 seed "12345678901234567890", last six digits.
    SECRET = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

    def test_rfc6238_vectors(self):
        for at, code in ((59, "287082"), (1111111109, "081804"), (1234567890, "005924"), (2000000000, "279037")):
            with self.subTest(at=at):
                self.assertEqual(cluster_e2e.totp(self.SECRET, at), code)

    def test_unpadded_lower_case_secret(self):
        self.assertEqual(cluster_e2e.totp("gezdgnbvgy3tqojq", 59), cluster_e2e.totp("GEZDGNBVGY3TQOJQ", 59))


class PortForwardTests(unittest.TestCase):
    def test_reads_the_local_port(self):
        self.assertEqual(cluster_e2e.forwarded_port("Forwarding from 127.0.0.1:40213 -> 8080\n"), 40213)
        self.assertIsNone(cluster_e2e.forwarded_port("Forwarding from [::1]:40213 -> 8080"))
        self.assertIsNone(cluster_e2e.forwarded_port("Handling connection for 40213"))


FAKE_KUBECTL = textwrap.dedent('''\
    #!/usr/bin/env python3
    import os, sys
    args = sys.argv[1:]
    with open(os.environ["CALLS"], "a") as calls:
        calls.write(" ".join(args) + "\\n")
    if "port-forward" in args:
        sys.stderr.write("error: services \\"restmail-api\\" not found\\n")
        sys.exit(1)
    if args[2:4] == ["get", "pods"]:
        print("ghcr.io/rest-mail/api:ci ghcr.io/rest-mail/js-filter:ci")
''')


class MainTests(unittest.TestCase):
    def test_a_failed_step_fails_the_run_and_skips_the_rest(self):
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory)
            kubectl = work / "kubectl"
            kubectl.write_text(FAKE_KUBECTL)
            kubectl.chmod(kubectl.stat().st_mode | stat.S_IEXEC)
            report = work / "report"
            out, err = io.StringIO(), io.StringIO()
            with patch.dict(os.environ, {"CALLS": str(work / "calls")}), redirect_stdout(out), redirect_stderr(err):
                code = cluster_e2e.main(["--kubectl", str(kubectl), "--namespace", "ns", "--release", "restmail",
                                         "--report-dir", str(report), "--commit", "c0ffee", "--timeout", "5",
                                         "--only", "health", "--only", "description"])
            self.assertEqual(code, 1)
            lines = out.getvalue().splitlines()
            self.assertEqual(len(lines), 2)
            self.assertTrue(lines[0].startswith(
                "cluster-e2e receipt commit=c0ffee images=api:ci,js-filter:ci step=health result=fail detail="))
            self.assertEqual(lines[1], "cluster-e2e receipt commit=c0ffee images=api:ci,js-filter:ci "
                                       "step=description result=skipped detail=an_earlier_step_failed")
            self.assertEqual((report / "receipts.txt").read_text().splitlines(), lines)
            self.assertIn("::error::health:", err.getvalue())
            calls = (work / "calls").read_text().splitlines()
            self.assertIn("-n ns port-forward svc/restmail-api :8080 --address 127.0.0.1", calls)
            self.assertIn("not found", (report / "port-forward-api.log").read_text())


class FreshInstall:
    """The rules a fresh install answers by, for the steps from mailbox to smtp-rcpt.

    Its domain is not live, going live is refused without a certificate naming it
    (#289), and a mailbox on a domain that is not live is unknown to RCPT (#1848).
    """

    DOMAIN = "restmail-ci.test"

    def __init__(self):
        self.active = False
        self.certificate = None
        self.mailboxes = set()

    def call(self, method, path, body=None, token=""):
        if (method, path) == ("POST", "/api/v1/accounts"):
            self.mailboxes.add(body["address"])
            return 201, {"data": {}}
        if (method, path) == ("GET", "/api/v1/admin/domains"):
            return 200, {"data": [{"id": 7, "name": self.DOMAIN, "active": self.active}]}
        if (method, path) == ("POST", "/api/v1/admin/certificates"):
            self.certificate = body
            return 201, {"data": {}}
        if (method, path) == ("PATCH", "/api/v1/admin/domains/7"):
            if body.get("active") and not self.covered():
                return 409, {"error": {"code": "certificate_required"}}
            self.active = bool(body.get("active"))
            return 200, {"data": {"active": self.active}}
        return 404, {}

    def expect(self, want, method, path, body=None, token=""):
        status, answer = self.call(method, path, body, token)
        if status != want:
            raise cluster_e2e.StepFailed(f"{method} {path} answered {status}, want {want}")
        return answer

    def covered(self):
        if not self.certificate or self.certificate["domain_id"] != 7:
            return False
        text = subprocess.run(["openssl", "x509", "-noout", "-ext", "subjectAltName"],
                              input=self.certificate["cert_pem"], capture_output=True, text=True, check=True).stdout
        return f"DNS:{self.DOMAIN}" in text and "PRIVATE KEY" in self.certificate["key_pem"]


class FreshInstallSmtp:
    install = None

    def __init__(self, *_, **__):
        pass

    def __enter__(self):
        return self

    def __exit__(self, *_):
        return False

    def ehlo(self, _):
        return 250, b"ok"

    def mail(self, _):
        return 250, b"ok"

    def rset(self):
        return 250, b"ok"

    def rcpt(self, address):
        install = self.install
        if address in install.mailboxes and install.active:
            return 250, b"ok"
        return 550, b"5.1.1 No such user"


class DomainLiveTests(unittest.TestCase):
    """The order of stage 1 against a fresh install's rules, without kind."""

    def setUp(self):
        self.install = FreshInstall()
        FreshInstallSmtp.install = self.install
        cluster = SimpleNamespace(forward=lambda *_: 2525)
        self.stage = cluster_e2e.Stages(SimpleNamespace(timeout=5, report_dir="unused"), cluster)
        self.stage.api = self.install
        self.stage.state.update(token="t", domain=self.install.DOMAIN)
        patcher = patch.object(cluster_e2e.smtplib, "SMTP", FreshInstallSmtp)
        patcher.start()
        self.addCleanup(patcher.stop)

    def run_steps(self, steps):
        for step in steps:
            getattr(self.stage, step.replace("-", "_"))()

    def test_stage_1_makes_the_domain_live_before_rcpt_and_gets_250(self):
        steps = cluster_e2e.STEPS
        self.assertLess(steps.index("domain-live"), steps.index("smtp-rcpt"))
        between = steps[steps.index("mailbox"):steps.index("smtp-rcpt") + 1]
        self.run_steps(between)
        self.assertTrue(self.install.active)
        self.assertEqual(self.stage.smtp_rcpt(), "hosted=250,unhosted=550")

    def test_smtp_rcpt_before_the_domain_is_live_fails(self):
        self.run_steps(["mailbox"])
        with self.assertRaisesRegex(cluster_e2e.StepFailed, "needs domain_live"):
            self.run_steps(["smtp-rcpt"])

    def test_without_the_guard_the_install_answers_what_stopped_ci(self):
        self.run_steps(["mailbox"])
        self.stage.state["domain_live"] = "claimed, but never made live"
        with self.assertRaisesRegex(cluster_e2e.StepFailed, "answered 550 .*want 250"):
            self.run_steps(["smtp-rcpt"])

    def test_going_live_needs_a_certificate_for_the_domain_itself(self):
        real = cluster_e2e.self_signed_certificate
        with patch.object(cluster_e2e, "self_signed_certificate", lambda _: real("other.test")):
            with self.assertRaisesRegex(cluster_e2e.StepFailed, "answered 409"):
                self.run_steps(["domain-live"])
        self.assertFalse(self.install.active)

    def test_a_domain_the_install_does_not_list_fails(self):
        self.stage.state["domain"] = "elsewhere.test"
        with self.assertRaisesRegex(cluster_e2e.StepFailed, "lists no domain"):
            self.run_steps(["domain-live"])

    def test_the_certificate_comes_with_its_own_key(self):
        cert, key = cluster_e2e.self_signed_certificate(self.install.DOMAIN)
        self.assertIn("BEGIN CERTIFICATE", cert)
        self.assertIn("PRIVATE KEY", key)


def operation(operation_id, **extra):
    return {"operationId": operation_id, "responses": {"200": {"description": "ok"}}, **extra}


DESCRIPTION = {
    "openapi": "3.1.0",
    "security": [{"BearerAuth": []}],
    "paths": {
        "/api/health": {"get": operation("getHealth", security=[])},
        "/api": {"get": operation("apiRoot", security=[])},
        "/api/v1/server": {"get": operation("describeServer", security=[{}, {"BearerAuth": []}])},
        "/api/v1/domains": {"get": operation("listDomains"), "post": operation("createDomain")},
        "/api/v1/auth/login": {"post": operation("login", security=[])},
        "/api/mailboxes": {"get": operation("checkMailboxAddress", security=[])},
        "/apiary": {"get": operation("notTheApi", security=[])},
        "/.well-known/restmail": {"get": operation("restmailDiscover", security=[])},
        "/api/docs": {"parameters": [], "get": {"responses": {}}},
    },
}


class SelectionTests(unittest.TestCase):
    def test_public_reads_under_the_api_path_less_the_internal_routes(self):
        self.assertEqual(cluster_e2e.contract_operations(DESCRIPTION, {"checkMailboxAddress"}),
                         ["apiRoot", "describeServer", "getHealth"])

    def test_no_default_security_makes_every_read_public(self):
        description = {**DESCRIPTION, "security": []}
        self.assertIn("listDomains", cluster_e2e.contract_operations(description, set()))

    def test_the_internal_routes_are_vertrag_yml_endpoints(self):
        config = yaml.safe_load((ROOT / "vertrag.yml").read_text())
        self.assertEqual(cluster_e2e.internal_operations(config), {"checkMailboxAddress", "deliverMessage"})
        self.assertEqual(cluster_e2e.internal_operations({"endpoints": [{"operation-id": "one"}]}), {"one"})
        self.assertEqual(cluster_e2e.internal_operations({}), set())


class ContractConfigTests(unittest.TestCase):
    def test_the_contract_runs_configuration_without_the_topologys_fixtures(self):
        base = yaml.safe_load((ROOT / "vertrag.yml").read_text())
        configure = cluster_e2e.load_reports_module().configure
        with tempfile.TemporaryDirectory() as directory:
            out = Path(directory) / "contract"
            config = cluster_e2e.contract_config(base, configure, "https://mail.example.test",
                                                 str(out / "openapi.json"), out / "examples")
        for gone in ("auth", "hookfiles", "language", "hooks-worker-timeout", "endpoints", "surface"):
            self.assertNotIn(gone, config)
        self.assertEqual(config["endpoint"], "https://mail.example.test")
        self.assertEqual(config["spec"], str((out / "openapi.json").resolve()))
        self.assertEqual(config["checks"], {"header-schema": True, "ignored-auth": False})
        self.assertIn(str((out / "examples.html").resolve()), config["output"])
        self.assertIn("html", config["reporter"])
        self.assertEqual(config["unproven"], str((out / "unproven.json").resolve()))
        # What is never sent stays never sent.
        self.assertEqual(config["exclude-operation-id"], base["exclude-operation-id"])
        # vertrag.yml itself is untouched.
        self.assertIn("auth", base)

    def test_vertrag_runs_the_examples_phase_of_the_chosen_operations(self):
        self.assertEqual(
            cluster_e2e.vertrag_command("/v", Path("/c.yml"), ["a", "b"], "/ca.pem"),
            ["/v", "run", "--config", "/c.yml", "--sequence", "--phases", "examples",
             "--ca-cert", "/ca.pem", "--operation-id", "a", "--operation-id", "b"])
        self.assertNotIn("--ca-cert", cluster_e2e.vertrag_command("/v", Path("/c.yml"), ["a"]))


class JunitTests(unittest.TestCase):
    def counts(self, xml):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "examples.xml"
            path.write_text(xml)
            return cluster_e2e.junit_counts(path)

    def test_counts_every_suite(self):
        self.assertEqual(self.counts(
            '<testsuites><testsuite tests="5" failures="1" errors="1" skipped="2"/>'
            '<testsuite tests="3" failures="0" skipped="1"/></testsuites>'), (8, 2, 3))
        self.assertEqual(self.counts('<testsuite tests="4" failures="0"/>'), (4, 0, 0))

    def test_a_broken_report_fails_the_step(self):
        with self.assertRaises(cluster_e2e.StepFailed):
            self.counts("<testsuites")


# Writes the JUnit report named in its --config, as Vertrag would, with the
# verdict FAKE_FAILURES asks for, and records its arguments.
FAKE_VERTRAG = textwrap.dedent('''\
    #!/usr/bin/env python3
    import os, sys, yaml
    args = sys.argv[1:]
    with open(os.environ["CALLS"], "a") as calls:
        calls.write(" ".join(args) + "\\n")
    config = yaml.safe_load(open(args[args.index("--config") + 1]))
    junit = [path for path in config["output"] if path.endswith(".xml")][0]
    failures = int(os.environ.get("FAKE_FAILURES", "0"))
    open(junit, "w").write(f'<testsuites><testsuite tests="3" failures="{failures}" skipped="0"/></testsuites>')
    sys.exit(1 if failures else 0)
''')


class Ingress(http.server.BaseHTTPRequestHandler):
    description = DESCRIPTION

    def do_GET(self):
        body = {"/api/health": b'{"status":"ok"}',
                "/api/docs/openapi.json": json.dumps(self.description).encode()}.get(self.path)
        self.send_response(200 if body else 404)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(body or b"{}")

    def log_message(self, *args):
        pass


class ContractStepTests(unittest.TestCase):
    def setUp(self):
        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Ingress)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.work = Path(directory.name)
        self.vertrag = self.work / "vertrag"
        self.vertrag.write_text(FAKE_VERTRAG)
        self.vertrag.chmod(self.vertrag.stat().st_mode | stat.S_IEXEC)

    def stage(self, **state):
        args = cluster_e2e.parse_args([
            "--commit", "c", "--report-dir", str(self.work / "report"), "--timeout", "5",
            "--ingress-url", f"http://127.0.0.1:{self.server.server_address[1]}", "--vertrag", str(self.vertrag)])
        stage = cluster_e2e.Stages(args, cluster=None)
        stage.state.update(state)
        return stage

    def run_step(self, failures=0, **state):
        env = {"CALLS": str(self.work / "calls"), "FAKE_FAILURES": str(failures)}
        with patch.dict(os.environ, env):
            return self.stage(**state).contract_examples()

    def test_sends_the_public_reads_through_the_ingress_and_reports_them(self):
        detail = self.run_step(description=DESCRIPTION)
        self.assertEqual(detail, "operations=3,transactions=3,failed=0,skipped=0,report=contract/examples.html")
        call = (self.work / "calls").read_text().split()
        self.assertEqual([call[i + 1] for i, a in enumerate(call) if a == "--operation-id"],
                         ["apiRoot", "describeServer", "getHealth"])
        self.assertEqual(call[call.index("--phases") + 1], "examples")
        out = self.work / "report/contract"
        config = yaml.safe_load((out / "vertrag-examples.yml").read_text())
        self.assertEqual(config["endpoint"], f"http://127.0.0.1:{self.server.server_address[1]}")
        self.assertEqual(json.loads((out / "openapi.json").read_text()), DESCRIPTION)
        self.assertTrue((out / "examples.log").read_text().startswith("$ " + str(self.vertrag)))

    def test_a_failed_transaction_fails_the_step(self):
        with self.assertRaisesRegex(cluster_e2e.StepFailed, "Vertrag exited 1: .*failed=1"):
            self.run_step(failures=1)

    def test_the_ingress_must_serve_the_pods_description(self):
        with self.assertRaisesRegex(cluster_e2e.StepFailed, "different description"):
            self.run_step(description={**DESCRIPTION, "openapi": "3.0.0"})
        self.assertFalse((self.work / "calls").exists())

    def test_without_an_ingress_url_the_step_fails_and_says_what_it_needs(self):
        stage = self.stage()
        stage.args.ingress_url = ""
        with self.assertRaisesRegex(cluster_e2e.StepFailed, "--ingress-url"):
            stage.contract_examples()


class FakeImap:
    """UID SEARCH and FETCH over a fixed set of messages."""

    def __init__(self, messages):
        self.messages = messages  # uid -> raw

    def uid(self, command, *args):
        if command == "SEARCH":
            subject = args[-1].strip('"')
            hits = [str(uid).encode() for uid, raw in sorted(self.messages.items())
                    if f"Subject: {subject}\r\n".encode() in raw]
            return "OK", [b" ".join(hits)]
        uid = int(args[0])
        return "OK", [(f"{uid} (UID {uid} BODY[] {{n}}".encode(), self.messages[uid]), b")"]


class Stage3Tests(unittest.TestCase):
    SENDER, RECIPIENT = "reference@reference.test", "cluster-e2e@restmail-ci.test"

    def message(self, subject, sender=None):
        return cluster_e2e.submission_message(sender or self.SENDER, self.RECIPIENT, subject).replace(b"\n", b"\r\n")

    def test_markers_differ_and_name_the_direction(self):
        first, second = cluster_e2e.mail_marker("in"), cluster_e2e.mail_marker("in")
        self.assertNotEqual(first, second)
        self.assertTrue(first.startswith("cluster-e2e in "))

    def test_the_sent_message_is_recognised_by_subject_and_sender(self):
        raw = b"Received: from mail.reference.test\r\n  by mail.restmail-ci.test\r\n" + self.message("s 1")
        evidence = cluster_e2e.received_with(raw, "s 1", self.SENDER)
        self.assertEqual(evidence["received"], "from mail.reference.test by mail.restmail-ci.test")

    def test_another_message_is_not_the_one_sent(self):
        with self.assertRaisesRegex(cluster_e2e.StepFailed, "Subject"):
            cluster_e2e.received_with(self.message("other"), "s 1", self.SENDER)
        with self.assertRaisesRegex(cluster_e2e.StepFailed, "From"):
            cluster_e2e.received_with(self.message("s 1", "someone@else.test"), "s 1", self.SENDER)

    def test_imap_find_returns_the_newest_match_or_none(self):
        imap = FakeImap({3: self.message("a"), 7: self.message("b"), 9: self.message("b")})
        self.assertEqual(cluster_e2e.imap_find(imap, "b"), imap.messages[9])
        self.assertIsNone(cluster_e2e.imap_find(imap, "c"))

    def test_pop3_find_numbers_from_one(self):
        heads = [self.message("a"), self.message("b"), self.message("c")]
        self.assertEqual(cluster_e2e.pop3_find(heads, "b"), 2)
        self.assertIsNone(cluster_e2e.pop3_find(heads, "d"))

    def clock(self):
        now = [0.0]
        return now, (lambda: now[0]), (lambda seconds: now.__setitem__(0, now[0] + seconds))

    def test_wait_for_retries_until_the_message_is_there(self):
        now, clock, sleep = self.clock()
        answers = iter([None, None, b"raw"])
        self.assertEqual(cluster_e2e.wait_for(lambda: next(answers), 60, 5, clock, sleep), (b"raw", 10.0))

    def test_wait_for_gives_up_at_the_timeout(self):
        now, clock, sleep = self.clock()
        calls = []
        result, waited = cluster_e2e.wait_for(lambda: calls.append(1), 12, 5, clock, sleep)
        self.assertIsNone(result)
        self.assertEqual((waited, len(calls)), (15.0, 4))

    def test_an_error_ends_the_wait(self):
        now, clock, sleep = self.clock()

        def probe():
            raise cluster_e2e.StepFailed("refused")
        with self.assertRaises(cluster_e2e.StepFailed):
            cluster_e2e.wait_for(probe, 60, 5, clock, sleep)
        self.assertEqual(now[0], 0.0)

    def test_without_a_reference_namespace_stage_3_says_what_it_needs(self):
        args = cluster_e2e.parse_args(["--commit", "c"])
        stage = cluster_e2e.Stages(args, cluster=None)
        stage.state.update(token="t", admin="admin@restmail-ci.test", mailbox="m@x.test", mailbox_password="p",
                           inbound_subject="s")
        for step in (stage.mail_out, stage.mail_in):
            with self.subTest(step=step.__name__), self.assertRaisesRegex(cluster_e2e.StepFailed,
                                                                          "--reference-namespace"):
                step()

    def test_the_reference_account_comes_from_its_secret(self):
        args = cluster_e2e.parse_args(["--commit", "c", "--reference-namespace", "ref"])
        cluster = unittest.mock.Mock()
        cluster.secret_value.side_effect = lambda ns, name, key: f"{ns}/{name}/{key}"
        stage = cluster_e2e.Stages(args, cluster)
        self.assertEqual(stage.reference_account(), ("ref/reference-mail/address", "ref/reference-mail/password"))
        stage.reference_account()
        self.assertEqual(cluster.secret_value.call_count, 2)


class EntryPointTests(unittest.TestCase):
    COMMAND = "scripts/cluster_e2e.py"

    def test_chore_entry_runs_the_script(self):
        chores = yaml.safe_load((ROOT / "chores.yml").read_text())
        task = chores["tasks"]["cluster:e2e"]
        self.assertEqual(task["cmds"], ["python3 -B scripts/cluster_e2e.py {{.CLI_ARGS}}"])

    def workflow(self):
        return yaml.safe_load((ROOT / ".github/workflows/cluster-e2e.yml").read_text())

    def test_workflow_runs_the_same_script(self):
        steps = self.workflow()["jobs"]["install"]["steps"]
        runs = [step for step in steps if self.COMMAND in step.get("run", "")]
        self.assertEqual(len(runs), 1)
        names = [step["name"] for step in steps]
        self.assertLess(names.index("Wait for every pod to be ready"), names.index(runs[0]["name"]))
        self.assertLess(names.index(runs[0]["name"]), names.index("Collect pod logs, descriptions and events"))
        self.assertIn('--commit "$GITHUB_SHA"', runs[0]["run"])
        self.assertIn('--report-dir "$REPORT_DIR"', runs[0]["run"])
        self.assertIn('--ingress-url "https://$INGRESS_HOST"', runs[0]["run"])
        self.assertIn('--ingress-ca "$RUNNER_TEMP/tls.crt"', runs[0]["run"])
        self.assertIn('--reference-namespace "$REFERENCE_NAMESPACE"', runs[0]["run"])
        # The old inline two-request smoke is gone: the script owns it now.
        self.assertNotIn("Request the API through a port-forward", names)

    def test_workflow_triggers_daily_on_v_tags_and_by_hand_never_on_pull_requests(self):
        triggers = self.workflow()[True]  # YAML 1.1 reads the key "on" as true
        self.assertEqual(set(triggers), {"schedule", "push", "workflow_dispatch"})
        self.assertEqual(triggers["push"], {"tags": ["v*"]})
        self.assertEqual(triggers["schedule"], [{"cron": "0 5 * * *"}])

    def test_the_ingress_serves_the_ci_host_with_the_ci_certificate(self):
        workflow = self.workflow()
        host = workflow["env"]["INGRESS_HOST"]
        values = yaml.safe_load((ROOT / "helm/restmail/values-ci.yaml").read_text())["api"]["ingress"]
        self.assertEqual(values, {"enabled": True, "className": "nginx", "host": host,
                                  "tls": {"enabled": True, "secretName": "restmail-ci-tls"}})
        steps = {step["name"]: step.get("run", "") for step in workflow["jobs"]["install"]["steps"]}
        self.assertIn(f"DNS:{host}", steps["Create the self-signed TLS Secret"])
        controller = steps["Install the ingress controller"]
        self.assertIn('sha256sum -c -', controller)
        self.assertRegex(workflow["env"]["INGRESS_NGINX_SHA256"], r"^[0-9a-f]{64}$")
        self.assertRegex(workflow["env"]["INGRESS_NGINX_MANIFEST"], r"/controller-v[0-9.]+/deploy/static/provider/kind/")
        self.assertIn("hostPort: 443", steps["Create the cluster"])
        names = list(steps)
        self.assertLess(names.index("Install the ingress controller"), names.index("Install the chart"))

    def test_the_reference_is_deployed_before_the_stages_with_the_installs_mail_names(self):
        workflow = self.workflow()
        steps = workflow["jobs"]["install"]["steps"]
        names = [step["name"] for step in steps]
        deploy = steps[names.index("Deploy the reference mail server")]["run"]
        self.assertIn("scripts/cluster_reference.py", deploy)
        self.assertLess(names.index("Wait for every pod to be ready"), names.index("Deploy the reference mail server"))
        self.assertLess(names.index("Deploy the reference mail server"), names.index("Stages 1 to 3 against the install"))
        values = yaml.safe_load((ROOT / "helm/restmail/values-ci.yaml").read_text())["mailserver"]
        self.assertEqual(workflow["env"]["MAIL_DOMAIN"], values["domain"])
        self.assertEqual(workflow["env"]["INGRESS_HOST"], values["hostname"])
        for flag in ('--mail-domain "$MAIL_DOMAIN"', '--mail-host "$INGRESS_HOST"',
                     '--namespace "$REFERENCE_NAMESPACE"', '--release "$RELEASE"'):
            self.assertIn(flag, deploy)
        collect = steps[names.index("Collect pod logs, descriptions and events")]["run"]
        self.assertIn("logs/reference-mail.log", collect)

    def test_script_needs_no_go_toolchain(self):
        source = SCRIPT.read_text()
        for needle in ("go run", "go build", "go test", "GOTOOLCHAIN"):
            self.assertNotIn(needle, source)
        self.assertTrue(os.access(SCRIPT, os.X_OK))


if __name__ == "__main__":
    unittest.main()
