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
import re
from pathlib import Path
import shlex
import stat
import subprocess
import sys
import tempfile
import textwrap
import threading
import unittest
from contextlib import redirect_stderr, redirect_stdout
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
                                             "contract-examples", "dkim-key", "mail-out", "mail-in",
                                             "pop3-retrieve", "dkim-receive", "restmail-peer",
                                             "mta-sts-enforce", "mta-sts-mismatch"))
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
        fake = self.work / "fake_vertrag.py"
        fake.write_text(FAKE_VERTRAG)
        self.vertrag.write_text(f'#!/bin/sh\nexec {shlex.quote(sys.executable)} {shlex.quote(str(fake))} "$@"\n')
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

    def test_the_fake_runs_whatever_python3_the_path_finds(self):
        # The fake needs PyYAML, as this file does. A python3 on the PATH without
        # it made these tests fail on a machine whose CI passed them: CI's own
        # python3 has PyYAML. So the fake runs on this file's interpreter.
        empty = self.work / "no-python-here"
        empty.mkdir()
        with patch.dict(os.environ, {"PATH": str(empty)}):
            detail = self.run_step(description=DESCRIPTION)
        self.assertIn("transactions=3,failed=0", detail)

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

    def test_mail_in_waits_out_the_greylist_before_the_mail_timeout(self):
        # A fresh install defers the reference's first delivery (451) for the
        # greylist delay of the default inbound pipeline; the first run waited
        # only --mail-timeout and gave up before the retry.
        templates = (ROOT / "internal" / "pipeline" / "templates.go").read_text()
        minutes = {int(m) for m in re.findall(r'"delay_minutes":\s*(\d+)', templates)}
        self.assertTrue(minutes)
        self.assertGreaterEqual(cluster_e2e.GREYLIST_DELAY, max(minutes) * 60)
        args = cluster_e2e.parse_args(["--commit", "c"])
        self.assertEqual(cluster_e2e.mail_in_wait(args.mail_timeout), cluster_e2e.GREYLIST_DELAY + args.mail_timeout)

    def test_restmail_peer_waits_out_the_queue_greylisted_retry(self):
        # The peer install greylists the first RESTMAIL delivery (503
        # deferred); the first install's queue retries it on the greylisted
        # schedule, up to a fifth later than its first delay. The first run
        # waited only --mail-timeout (240s) and gave up before any retry.
        retry = (ROOT / "internal" / "gateway" / "queue" / "retry.go").read_text()
        first = re.search(r"models\.FailureGreylisted:\s*\{first:\s*(\d+)\s*\*\s*time\.Minute", retry)
        jitter = re.search(r"backoffJitterFraction\s*=\s*([0-9.]+)", retry)
        self.assertIsNotNone(first)
        self.assertIsNotNone(jitter)
        latest = int(first.group(1)) * 60 * (1 + float(jitter.group(1)))
        self.assertGreaterEqual(cluster_e2e.GREYLIST_RETRY, latest)
        self.assertGreaterEqual(cluster_e2e.GREYLIST_RETRY, cluster_e2e.GREYLIST_DELAY)
        args = cluster_e2e.parse_args(["--commit", "c"])
        self.assertEqual(cluster_e2e.restmail_peer_wait(args.mail_timeout),
                         cluster_e2e.GREYLIST_RETRY + args.mail_timeout)

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


class DomainLiveTests(unittest.TestCase):
    """A fresh install's domain is not live: its mailboxes are refused (550 5.1.1)
    until it has a certificate and is turned live, as run 41780851725 showed."""

    class FakeApi:
        def __init__(self, active=True):
            self.calls, self.active = [], active

        def expect(self, want, method, path, body=None, token=""):
            self.calls.append((want, method, path, body, token))
            if (method, path) == ("GET", "/api/v1/admin/domains"):
                return {"data": [{"id": 3, "name": "other.test"}, {"id": 7, "name": "RestMail-CI.test"}]}
            if (method, path) == ("GET", "/api/v1/admin/domains/7"):
                return {"data": {"id": 7, "name": "restmail-ci.test", "active": self.active}}
            return {"data": {}}

    def cluster(self):
        cluster = unittest.mock.Mock(namespace="restmail-ci")
        cluster.tls_secret.return_value = "restmail-ci-tls"
        cluster.secret_value.side_effect = lambda ns, name, key: f"{ns}/{name}/{key}"
        return cluster

    def test_uploads_the_served_certificate_then_turns_the_domain_live(self):
        api, cluster = self.FakeApi(), self.cluster()
        detail = cluster_e2e.put_domain_live(api, "tok", cluster, "restmail-ci.test")
        self.assertEqual(detail, "domain=restmail-ci.test,id=7,certificate=restmail-ci-tls,active=true")
        self.assertEqual([(c[1], c[2]) for c in api.calls], [
            ("GET", "/api/v1/admin/domains"), ("POST", "/api/v1/admin/certificates"),
            ("PATCH", "/api/v1/admin/domains/7"), ("GET", "/api/v1/admin/domains/7")])
        self.assertEqual(api.calls[1][0], 201)
        self.assertEqual(api.calls[1][3], {"domain_id": 7, "cert_pem": r"restmail-ci/restmail-ci-tls/tls\.crt",
                                           "key_pem": r"restmail-ci/restmail-ci-tls/tls\.key", "auto_renew": False})
        self.assertEqual(api.calls[2][3], {"active": True})
        self.assertTrue(all(c[4] == "tok" for c in api.calls))

    def test_a_domain_still_not_live_fails_the_step(self):
        with self.assertRaisesRegex(cluster_e2e.StepFailed, "not live"):
            cluster_e2e.put_domain_live(self.FakeApi(active=False), "tok", self.cluster(), "restmail-ci.test")

    def test_an_unknown_domain_fails_the_step(self):
        with self.assertRaisesRegex(cluster_e2e.StepFailed, "no domain nope.test"):
            cluster_e2e.find_domain([{"id": 1, "name": "restmail-ci.test"}, "junk"], "nope.test")

    def test_the_step_goes_live_before_mail_reaches_the_mailbox(self):
        steps = cluster_e2e.STEPS
        self.assertLess(steps.index("mailbox"), steps.index("domain-live"))
        self.assertLess(steps.index("domain-live"), steps.index("smtp-rcpt"))
        self.assertLess(steps.index("domain-live"), steps.index("mail-in"))

    def test_the_tls_secret_is_the_one_the_smtp_gateway_mounts(self):
        cluster = cluster_e2e.Cluster("kubectl", "ns", "restmail", Path("."))
        with patch.object(cluster, "run", return_value="restmail-ci-tls\n") as run:
            self.assertEqual(cluster.tls_secret(), "restmail-ci-tls")
        self.assertEqual(run.call_args.args[:2], ("get", "deployment/restmail-smtp-gateway"))
        self.assertIn('?(@.name=="tls")', run.call_args.args[-1])
        with patch.object(cluster, "run", return_value=""), self.assertRaises(cluster_e2e.StepFailed):
            cluster.tls_secret()

    def test_the_chart_mounts_the_tls_secret_as_tls_in_the_smtp_gateway(self):
        template = (ROOT / "helm/restmail/templates/smtp-gateway-deployment.yaml").read_text()
        self.assertRegex(template, r"- name: tls\n\s+secret:\n\s+secretName: \{\{ \.Values\.tls\.secretName \}\}")

    def test_bootstrap_still_creates_the_domain_not_live(self):
        # If the install ever comes up live on its own, this step can go.
        bootstrap = (ROOT / "internal/bootstrap/bootstrap.go").read_text()
        self.assertIn('Active: false', bootstrap)


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
        self.assertIn('--ingress-ca "$RUNNER_TEMP/ca.crt"', runs[0]["run"])
        self.assertIn('--reference-namespace "$REFERENCE_NAMESPACE"', runs[0]["run"])
        self.assertIn('--peer-namespace "$PEER_NAMESPACE" --peer-release "$RELEASE"', runs[0]["run"])
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
        self.assertIn('issue "$NAMESPACE" restmail-ci-tls "$INGRESS_HOST" "$(install_names "$NAMESPACE" "$INGRESS_HOST" "$MAIL_DOMAIN")"',
                      steps["Create the TLS Secrets"])
        self.assertIn('subjectAltName=$4', steps["Create the TLS Secrets"])
        self.assertIn('echo "DNS:$2,', steps["Create the TLS Secrets"])
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


class DkimTests(unittest.TestCase):
    def test_a_new_key_and_the_txt_record_of_its_public_half(self):
        key, record = cluster_e2e.dkim_keypair(1024)
        self.assertIn("-----BEGIN PRIVATE KEY-----", key)
        self.assertRegex(record, r"^v=DKIM1; k=rsa; p=[A-Za-z0-9+/]+=*$")
        import base64, subprocess
        public = subprocess.run(["openssl", "pkey", "-pubout", "-outform", "DER"], input=key.encode(),
                                capture_output=True, check=True).stdout
        self.assertEqual(record.split("p=", 1)[1], base64.b64encode(public).decode())

    def test_the_domain_id_comes_from_the_list(self):
        domains = [{"id": 3, "name": "other.test"}, {"id": 7, "name": "Restmail-CI.test"}]
        self.assertEqual(cluster_e2e.find_domain(domains, "restmail-ci.test"), 7)
        with self.assertRaisesRegex(cluster_e2e.StepFailed, "no domain"):
            cluster_e2e.find_domain(domains, "missing.test")

    def test_only_a_found_dkim_record_under_the_owner_counts(self):
        check = {"records": [
            {"type": "DKIM", "name": "old._domainkey.restmail-ci.test", "status": "ok", "value": "v=DKIM1; p=A"},
            {"type": "DKIM", "name": "s1._domainkey.restmail-ci.test", "status": "missing"},
            {"type": "MX", "name": "restmail-ci.test", "status": "ok"}]}
        self.assertIsNone(cluster_e2e.dkim_record_check(check, "s1._domainkey.restmail-ci.test"))
        check["records"][1].update(status="ok", value="v=DKIM1; p=B")
        self.assertEqual(cluster_e2e.dkim_record_check(check, "s1._domainkey.restmail-ci.test")["value"],
                         "v=DKIM1; p=B")

    def test_the_signature_tags_are_read_through_folding(self):
        raw = (b"DKIM-Signature: v=1; a=rsa-sha256; c=relaxed/relaxed;\r\n\td=restmail-ci.test; s=sel;\r\n"
               b"\tb=abc\r\n def==\r\nFrom: a@restmail-ci.test\r\n\r\nbody\r\n")
        tags = cluster_e2e.dkim_tags(raw)
        self.assertEqual((tags["d"], tags["s"], tags["a"], tags["b"]), ("restmail-ci.test", "sel", "rsa-sha256", "abcdef=="))
        with self.assertRaisesRegex(cluster_e2e.StepFailed, "no DKIM-Signature"):
            cluster_e2e.dkim_tags(b"From: a@b.test\r\n\r\nx\r\n")

    def test_verification_gets_the_published_record_for_the_owner_only(self):
        asked = []

        def verify(raw, dnsfunc):
            asked.append(dnsfunc(b"sel._domainkey.restmail-ci.test.", timeout=5))
            asked.append(dnsfunc(b"other._domainkey.restmail-ci.test."))
            return True
        self.assertTrue(cluster_e2e.dkim_verify(b"m", "sel._domainkey.restmail-ci.test", "v=DKIM1; p=K", verify))
        self.assertEqual(asked, [b"v=DKIM1; p=K", None])

    def test_dkim_receive_needs_the_signature_of_the_domain_under_the_selector(self):
        args = cluster_e2e.parse_args(["--commit", "c"])
        stage = cluster_e2e.Stages(args, cluster=None)
        raw = b"DKIM-Signature: v=1; a=rsa-sha256; d=other.test; s=sel; b=x\r\nFrom: a@restmail-ci.test\r\n\r\nb\r\n"
        stage.state.update(domain="restmail-ci.test", mail_out_raw=raw,
                           dkim={"selector": "sel", "owner": "sel._domainkey.restmail-ci.test", "record": "v=DKIM1; p=K"})
        with self.assertRaisesRegex(cluster_e2e.StepFailed, "want d=restmail-ci.test s=sel"):
            stage.dkim_receive()
        stage.state["mail_out_raw"] = raw.replace(b"d=other.test", b"d=restmail-ci.test")
        with patch.object(cluster_e2e, "dkim_verify", return_value=False), \
                self.assertRaisesRegex(cluster_e2e.StepFailed, "does not verify"):
            stage.dkim_receive()
        with patch.object(cluster_e2e, "dkim_verify", return_value=True), \
                patch.dict(os.environ, {"DKIMPY_VERSION": "1.1.8"}):
            self.assertIn("verified=dkimpy-1.1.8", stage.dkim_receive())

    def test_a_signature_the_pinned_dkimpy_makes_verifies_and_a_changed_body_does_not(self):
        try:
            import dkim
        except ImportError:
            self.skipTest("dkimpy is not on the path (the workflow pins it)")
        key, record = cluster_e2e.dkim_keypair(1024)
        message = b"From: a@restmail-ci.test\r\nTo: b@x.test\r\nSubject: s\r\n\r\nbody\r\n"
        signed = dkim.sign(message, b"sel", b"restmail-ci.test", key.encode()) + message
        self.assertTrue(cluster_e2e.dkim_verify(signed, "sel._domainkey.restmail-ci.test", record))
        self.assertFalse(cluster_e2e.dkim_verify(signed.replace(b"body", b"bodx"),
                                                 "sel._domainkey.restmail-ci.test", record))


class RestmailPeerTests(unittest.TestCase):
    URL = "https://mail.restmail-ci2.test/restmail/messages"

    def log(self, *entries):
        return "\n".join(["not json", *(json.dumps(e) for e in entries)])

    def test_the_restmail_delivery_to_the_recipient_is_found(self):
        log = self.log({"msg": cluster_e2e.RESTMAIL_DELIVERED, "url": self.URL, "recipient": "Cluster-E2E@restmail-ci2.test"},
                       {"msg": cluster_e2e.RESTMAIL_DELIVERED, "url": "https://x/restmail/messages", "recipient": "o@x.test"},
                       {"msg": "queue: RESTMAIL capability detected", "host": "mail.restmail-ci2.test"})
        self.assertEqual(cluster_e2e.restmail_hops(log, "cluster-e2e@restmail-ci2.test"), ([self.URL], []))

    def test_a_delivery_by_smtp_to_the_peers_domain_is_reported(self):
        log = self.log({"msg": cluster_e2e.SMTP_DELIVERED, "host": "mail.restmail-ci2.test", "domain": "B.test",
                        "transport": "authenticated"})
        self.assertEqual(cluster_e2e.restmail_hops(log, "a@b.test"), ([], ["mail.restmail-ci2.test"]))

    def test_a_delivery_by_smtp_to_another_domain_is_not_the_peers(self):
        log = self.log({"msg": cluster_e2e.SMTP_DELIVERED, "host": "mail.reference.test", "domain": "reference.test"})
        self.assertEqual(cluster_e2e.restmail_hops(log, "a@b.test"), ([], []))

    def test_the_log_lines_are_the_ones_the_queue_writes(self):
        worker = (ROOT / "internal/gateway/queue/worker.go").read_text()
        self.assertIn(f'"{cluster_e2e.RESTMAIL_DELIVERED}", "url", messagesURL, "recipient"', worker)
        self.assertIn(f'"{cluster_e2e.SMTP_DELIVERED}", "host", host, "domain", item.Domain', worker)

    def test_only_the_peers_smtp_stamp_counts(self):
        raw = (b"Received: from mail.restmail-ci.test by mail.reference.test with ESMTPS id 1;\r\n Wed, 4 Nov 2026\r\n"
               b"Received: from x by mail.restmail-ci2.test with HTTPS;\r\n Wed, 4 Nov 2026\r\n"
               b"Subject: s\r\n\r\nb\r\n")
        self.assertEqual(cluster_e2e.smtp_stamps(raw, "mail.restmail-ci2.test"), [])
        stamped = b"Received: from mail.restmail-ci.test (10.0.0.1)\r\n by mail.restmail-ci2.test with ESMTPS id 2;\r\n x\r\n" + raw
        self.assertEqual(len(cluster_e2e.smtp_stamps(stamped, "mail.restmail-ci2.test")), 1)

    def test_without_a_peer_namespace_the_step_says_what_it_needs(self):
        args = cluster_e2e.parse_args(["--commit", "c"])
        stage = cluster_e2e.Stages(args, cluster=None)
        stage.state.update(token="t", admin="admin@restmail-ci.test")
        with self.assertRaisesRegex(cluster_e2e.StepFailed, "--peer-namespace"):
            stage.restmail_peer()

    def test_the_peers_domain_goes_live_before_anything_is_sent_to_it(self):
        import inspect
        source = inspect.getsource(cluster_e2e.Stages.restmail_peer)
        live = source.index("put_domain_live(peer_api, peer_token, peer, peer_domain)")
        self.assertLess(source.index("bootstrap_session(peer_api, peer)"), live)
        self.assertLess(live, source.index('"/api/v1/messages/send"'))

    def test_the_new_steps_run_in_order_after_the_mail_they_check(self):
        steps = list(cluster_e2e.STEPS)
        self.assertLess(steps.index("contract-examples"), steps.index("dkim-key"))
        self.assertLess(steps.index("dkim-key"), steps.index("mail-out"))
        self.assertLess(steps.index("mail-out"), steps.index("dkim-receive"))
        self.assertLess(steps.index("dkim-key"), steps.index("restmail-peer"))

    def test_each_install_certificate_names_its_mail_domain(self):
        # The API refuses a domain certificate that does not name the domain (#1715),
        # and restmail-peer uploads the peer's TLS Secret for its domain. Run 42087756027
        # failed there with 422: the Secret named only the mail host. This runs the
        # workflow's own step, with kubectl stubbed, and reads the certificates it made.
        workflow = yaml.safe_load((ROOT / ".github/workflows/cluster-e2e.yml").read_text())
        step = next(s["run"] for s in workflow["jobs"]["install"]["steps"] if s["name"] == "Create the TLS Secrets")
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory)
            (work / "bin").mkdir()
            (work / "out").mkdir()
            fake = work / "fake_kubectl.py"
            fake.write_text(textwrap.dedent('''\
                import shutil, sys
                args = sys.argv[1:]
                if "secret" in args:
                    namespace, name = args[args.index("-n") + 1], args[args.index("generic") + 1]
                    for arg in args:
                        if arg.startswith("--from-file=tls.crt="):
                            shutil.copy(arg.split("=", 2)[2], f"{sys.argv[0].rsplit('/', 1)[0]}/out/{namespace}.crt")
            '''))
            kubectl = work / "bin/kubectl"
            kubectl.write_text(f'#!/bin/sh\nexec {shlex.quote(sys.executable)} {shlex.quote(str(fake))} "$@"\n')
            kubectl.chmod(0o755)
            env = {**os.environ, **{key: str(value) for key, value in workflow["env"].items()},
                   "PATH": f"{work / 'bin'}:{os.environ['PATH']}", "RUNNER_TEMP": str(work),
                   "GITHUB_WORKSPACE": str(ROOT)}
            subprocess.run(["bash", "-e", "-o", "pipefail", "-c", step], env=env, cwd=work, check=True,
                           capture_output=True, text=True)
            for namespace, domain, host in (("NAMESPACE", "MAIL_DOMAIN", "INGRESS_HOST"),
                                            ("PEER_NAMESPACE", "PEER_MAIL_DOMAIN", "PEER_MAIL_HOST")):
                with self.subTest(install=workflow["env"][namespace]):
                    names = subprocess.run(
                        ["openssl", "x509", "-noout", "-ext", "subjectAltName",
                         "-in", str(work / "out" / f"{workflow['env'][namespace]}.crt")],
                        capture_output=True, text=True, check=True).stdout
                    names = {name.strip() for name in names.split("\n", 1)[1].split(",")}
                    self.assertIn("DNS:" + workflow["env"][domain], names)
                    self.assertIn("DNS:" + workflow["env"][host], names)

    def test_the_peer_install_has_its_own_names_and_a_trusted_ca(self):
        base = yaml.safe_load((ROOT / "helm/restmail/values-ci.yaml").read_text())
        peer = yaml.safe_load((ROOT / "helm/restmail/values-ci-peer.yaml").read_text())
        env = yaml.safe_load((ROOT / ".github/workflows/cluster-e2e.yml").read_text())["env"]
        self.assertEqual(peer["mailserver"], {"hostname": env["PEER_MAIL_HOST"], "domain": env["PEER_MAIL_DOMAIN"]})
        self.assertNotEqual(peer["mailserver"]["domain"], base["mailserver"]["domain"])
        self.assertEqual(peer["api"]["ingress"]["host"], env["PEER_MAIL_HOST"])
        self.assertEqual(peer["tls"]["secretName"], peer["api"]["ingress"]["tls"]["secretName"])
        self.assertTrue(base["smtpGateway"]["allowPrivateDestinations"])
        steps = {s["name"]: s.get("run", "") for s in
                 yaml.safe_load((ROOT / ".github/workflows/cluster-e2e.yml").read_text())["jobs"]["install"]["steps"]}
        self.assertIn('issue "$PEER_NAMESPACE" restmail-ci2-tls "$PEER_MAIL_HOST"', steps["Create the TLS Secrets"])
        self.assertIn("--from-file=ca.crt=ca.crt", steps["Create the TLS Secrets"])
        self.assertIn("values-ci-peer.yaml", steps["Install the chart"])
        self.assertIn('--peer-namespace "$PEER_NAMESPACE"', steps["Deploy the reference mail server"])
        self.assertIn('sha256sum -c -', steps["Stages 1 to 3 against the install"])
        self.assertRegex(env["DKIMPY_SHA256"], r"^[0-9a-f]{64}$")


class MTASTSTests(unittest.TestCase):
    # Lines as the queue's slog JSON handler writes them (internal/gateway/queue/worker.go).
    ENFORCE_LOG = "\n".join([
        '{"level":"INFO","msg":"queue: processing","recipient":"reference@reference.test","domain":"reference.test"}',
        '{"level":"INFO","msg":"queue: MTA-STS policy found","domain":"reference.test","mode":"enforce","mx_count":1,"enforce":true}',
        '{"level":"INFO","msg":"queue: delivered over SMTP","host":"mail.reference.test","domain":"reference.test","transport":"authenticated"}',
        '{"level":"INFO","msg":"queue: delivered","id":7,"recipient":"reference@reference.test"}',
        'not json',
    ])
    MISMATCH_LOG = "\n".join([
        '{"level":"INFO","msg":"queue: MTA-STS policy found","domain":"sts-mismatch.test","mode":"enforce","mx_count":1,"enforce":true}',
        '{"level":"WARN","msg":"queue: MTA-STS enforce \u2014 MX host not named by policy, skipping","host":"mail.sts-mismatch.test","domain":"sts-mismatch.test"}',
        '{"level":"WARN","msg":"queue: delivery failed","id":8,"recipient":"reference@sts-mismatch.test","attempt":1,'
        '"error":"all MX hosts failed: MTA-STS enforce: MX host not named by policy (domain=sts-mismatch.test mx=mail.sts-mismatch.test)",'
        '"smtp_code":0,"failure_category":"tls"}',
    ])

    def test_enforce_trail(self):
        trail = cluster_e2e.sts_trail(self.ENFORCE_LOG, "reference.test")
        self.assertEqual(trail["policies"], [("enforce", True)])
        self.assertEqual(trail["delivered"], [("mail.reference.test", "authenticated")])
        self.assertEqual((trail["skipped"], trail["refused"], trail["failed"]), ([], [], []))
        self.assertEqual(cluster_e2e.sts_trail(self.ENFORCE_LOG, "other.test")["delivered"], [])

    def test_mismatch_trail(self):
        trail = cluster_e2e.sts_trail(self.MISMATCH_LOG, "sts-mismatch.test", "reference@sts-mismatch.test")
        self.assertEqual(trail["skipped"], ["mail.sts-mismatch.test"])
        self.assertEqual(trail["delivered"], [])
        self.assertEqual(len(trail["failed"]), 1)
        self.assertIn("MX host not named by policy", trail["failed"][0])
        self.assertEqual(cluster_e2e.sts_trail(self.MISMATCH_LOG, "sts-mismatch.test", "x@y.test")["failed"], [])

    def test_the_log_messages_are_the_queues(self):
        source = (ROOT / "internal/gateway/queue/worker.go").read_text()
        for msg in (cluster_e2e.STS_POLICY_FOUND, cluster_e2e.SMTP_DELIVERED, cluster_e2e.STS_MX_SKIPPED,
                    cluster_e2e.STS_REFUSED, cluster_e2e.DELIVERY_FAILED):
            self.assertIn(f'"{msg}"', source)
        self.assertIn('transportAuthenticated = "authenticated"', source)

    def test_policy_fetches_count_only_served_policies(self):
        log = "\n".join([
            'mta-sts 10.244.0.9 host=mta-sts.reference.test "GET /.well-known/mta-sts.txt HTTP/1.1" 200 -',
            'mta-sts 10.244.0.9 host=mta-sts.reference.test "GET /.well-known/mta-sts.txt HTTP/1.1" 404 -',
            'mta-sts 10.244.0.9 host=mta-sts.sts-mismatch.test "GET /.well-known/mta-sts.txt HTTP/1.1" 200 -',
        ])
        self.assertEqual(cluster_e2e.policy_fetches(log, "mta-sts.reference.test"), 1)
        self.assertEqual(cluster_e2e.policy_fetches(log, "mta-sts.sts-mismatch.test"), 1)

    def test_without_a_reference_namespace_the_steps_say_what_they_need(self):
        stage = cluster_e2e.Stages(cluster_e2e.parse_args(["--commit", "c"]), cluster=None)
        stage.state.update(token="t", admin="admin@restmail-ci.test")
        for step in (stage.mta_sts_enforce, stage.mta_sts_mismatch):
            with self.subTest(step=step.__name__), self.assertRaisesRegex(cluster_e2e.StepFailed, "--reference-namespace"):
                step()

    def test_the_gateway_trusts_the_charts_ca_for_smtp_and_policy_fetches(self):
        entrypoint = (ROOT / "projects/smtp-gateway/entrypoint.sh").read_text()
        self.assertIn('ca="${TRUSTED_CA_CERT:-/certs/ca.crt}"', entrypoint)
        self.assertIn("export SSL_CERT_FILE=/tmp/ca-certificates.crt", entrypoint)
        template = (ROOT / "helm/restmail/templates/smtp-gateway-deployment.yaml").read_text()
        self.assertRegex(template, r"name: TRUSTED_CA_CERT\s+value: /etc/restmail-trusted-ca/ca\.crt")
        steps = {s["name"]: s.get("run", "") for s in
                 yaml.safe_load((ROOT / ".github/workflows/cluster-e2e.yml").read_text())["jobs"]["install"]["steps"]}
        self.assertIn('issue "$REFERENCE_NAMESPACE" reference-mail-tls', steps["Create the TLS Secrets"])

    def test_the_mta_sts_steps_come_after_mail_out(self):
        steps = list(cluster_e2e.STEPS)
        self.assertLess(steps.index("mail-out"), steps.index("mta-sts-enforce"))
        self.assertLess(steps.index("mta-sts-enforce"), steps.index("mta-sts-mismatch"))


if __name__ == "__main__":
    unittest.main()
