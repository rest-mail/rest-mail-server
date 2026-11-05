"""Stage 1 of the installed-cluster check (#1750), without a cluster.

The steps themselves need an install (cluster-e2e.yml runs them); these test
what does not: the arguments, the receipt lines, the TOTP the admin signs in
with, reading kubectl's port-forward line, what a failed step does to the rest,
and that the workflow and `chore cluster:e2e` run the same script.
"""

import importlib.util
import io
import os
from pathlib import Path
import stat
import subprocess
import tempfile
import textwrap
import unittest
from contextlib import redirect_stderr, redirect_stdout
from types import SimpleNamespace
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

    def test_environment_supplies_what_the_workflow_sets(self):
        args = self.parse(env={"NAMESPACE": "ns", "RELEASE": "rel", "REPORT_DIR": "/r", "GITHUB_SHA": "deadbeef"})
        self.assertEqual((args.namespace, args.release, args.report_dir, args.commit), ("ns", "rel", "/r", "deadbeef"))

    def test_commit_falls_back_to_git_head(self):
        with patch.object(cluster_e2e, "git_head", return_value="fromgit"):
            self.assertEqual(self.parse().commit, "fromgit")

    def test_only_takes_known_steps(self):
        args = self.parse("--commit", "c", "--only", "health", "--only", "smtp-rcpt")
        self.assertEqual(args.only, ["health", "smtp-rcpt"])
        for bad in (["--only", "nope"], ["--timeout", "0"], ["--release", "a/b"]):
            with self.subTest(bad=bad), redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                self.parse("--commit", "c", *bad)

    def test_every_stage1_check_is_a_step(self):
        self.assertEqual(cluster_e2e.STEPS, ("health", "description", "admin-sign-in", "mailbox", "domain-live",
                                             "smtp-rcpt", "imap-sign-in", "filter-validate"))
        for step in cluster_e2e.STEPS:
            self.assertTrue(callable(getattr(cluster_e2e.Stage1, step.replace("-", "_"))), step)


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
        self.stage = cluster_e2e.Stage1(SimpleNamespace(timeout=5, report_dir="unused"), cluster)
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
        # The old inline two-request smoke is gone: the script owns it now.
        self.assertNotIn("Request the API through a port-forward", names)

    def test_workflow_triggers_daily_on_v_tags_and_by_hand_never_on_pull_requests(self):
        triggers = self.workflow()[True]  # YAML 1.1 reads the key "on" as true
        self.assertEqual(set(triggers), {"schedule", "push", "workflow_dispatch"})
        self.assertEqual(triggers["push"], {"tags": ["v*"]})
        self.assertEqual(triggers["schedule"], [{"cron": "0 5 * * *"}])

    def test_script_needs_no_go_toolchain(self):
        source = SCRIPT.read_text()
        for needle in ("go run", "go build", "go test", "GOTOOLCHAIN"):
            self.assertNotIn(needle, source)
        self.assertTrue(os.access(SCRIPT, os.X_OK))


if __name__ == "__main__":
    unittest.main()
