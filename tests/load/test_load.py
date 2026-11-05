"""Unit tests for the load runner's parsing and the run comparison (#1583)."""

import json
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import compare  # noqa: E402
import run  # noqa: E402


def load(rate, p95, errors=0):
    return {"kind": "x", "target": 100, "achieved_rate": rate, "ok": 1, "errors": errors,
            "latency_ms": {"p50": 1, "p95": p95, "p99": p95 * 2}}


def results(loads, resources=None):
    return {"tested": {"commit": "abc"}, "started_at": "t", "duration_s": 60,
            "targets": {}, "loads": loads, "resources": resources or {}}


class Parsing(unittest.TestCase):
    def test_docker_stats_units(self):
        self.assertEqual(run.mib("512KiB "), 0.5)
        self.assertEqual(run.mib("1.5GiB"), 1536.0)
        self.assertEqual(run.mib(" 20MiB"), 20.0)
        self.assertIsNone(run.mib("--"))
        self.assertEqual(run.percent("12.5%"), 12.5)
        self.assertIsNone(run.percent("--"))

    def test_met_needs_the_rate_and_no_errors(self):
        self.assertTrue(run.met({"target": 100, "achieved_rate": 96, "errors": 0}))
        self.assertFalse(run.met({"target": 100, "achieved_rate": 90, "errors": 0}))
        self.assertFalse(run.met({"target": 100, "achieved_rate": 100, "errors": 1}))
        self.assertFalse(run.met({"failed_to_run": "no file"}))

    def test_k6_summary_export(self):
        summary = {"metrics": {
            "http_reqs": {"count": 601, "rate": 10},
            "http_req_failed": {"passes": 2, "fails": 599, "value": 0.003},
            "http_req_duration": {"p(50)": 3.2, "p(90)": 5, "p(95)": 6.5, "p(99)": 9, "max": 40},
            "dropped_iterations": {"count": 1},
        }}
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / "k6.json"
            p.write_text(json.dumps(summary))
            r = run.k6_result(p, 10, 60)
        # The sign-in in setup() is not load.
        self.assertEqual(r["ok"], 598)
        self.assertEqual(r["errors"], 3)
        self.assertEqual(r["attempted"], 601)
        self.assertEqual(r["latency_ms"]["p95"], 6.5)
        self.assertAlmostEqual(r["achieved_rate"], 9.967)


class Delivery(unittest.TestCase):
    """How a run asks for delivery to be measured; loadgen_test.go proves the count."""

    def jobs(self, *argv):
        a = run.parse_args(["--duration", "60", "--delivery-drain", "120", *argv])
        return a, run.build_jobs(a, Path("/lg"), Path("/raw"), Path("/raw/resolv.conf"), "1:1", "run1")

    @staticmethod
    def arg(cmd, flag):
        return cmd[cmd.index(flag) + 1]

    def test_outbound_goes_to_the_sink_and_is_tagged_with_the_run(self):
        a, jobs = self.jobs()
        out, sink = jobs["smtp_out"], jobs["smtp_delivery"]
        self.assertEqual(self.arg(out, "-to"), "load@loadgen.test")
        self.assertEqual(self.arg(out, "-run"), "run1")
        self.assertEqual(self.arg(sink, "-run"), "run1")
        # It compares against what the sender was accepted, and outlasts sender and queue.
        self.assertEqual(self.arg(sink, "-sent-file"), "/out/smtp_out.json")
        self.assertEqual(self.arg(out, "-out"), "/out/smtp_out.json")
        self.assertEqual(self.arg(sink, "-drain"), "120s")
        self.assertEqual(self.arg(sink, "-duration"), f"{60 + run.GENERATOR_DRAIN + 120}s")
        self.assertEqual(self.arg(sink, "--ip"), a.sink_ip)
        self.assertEqual(self.arg(sink, "-addr"), ":25")

    def test_no_outbound_target_means_no_sink(self):
        _, jobs = self.jobs("--smtp-out-rate", "0")
        self.assertNotIn("smtp_out", jobs)
        self.assertNotIn("smtp_delivery", jobs)

    def test_the_dns_makes_the_sink_the_domains_mx(self):
        a, _ = self.jobs()
        fragment = run.sink_fragment(a)
        self.assertIn(f"address=/loadgen.test/{a.sink_ip}\n", fragment)
        self.assertIn("mx-host=loadgen.test,loadgen.test,10\n", fragment)
        self.assertIn('"v=spf1 ip4:10.99.0.0/16 -all"', fragment)

    def test_undelivered_mail_misses_the_target_whatever_was_accepted(self):
        delivered = {"target": 50, "achieved_rate": 50, "errors": 0}
        self.assertTrue(run.met(delivered))
        self.assertFalse(run.met({"target": 50, "achieved_rate": 50, "errors": 3}))
        self.assertFalse(run.met({"target": 50, "achieved_rate": 40, "errors": 0}))

    def test_the_summary_says_what_was_delivered_of_what_was_accepted(self):
        r = results({"smtp_delivery": {
            "target": 50, "target_unit": "msg/s", "achieved_rate": 25, "ok": 1500, "errors": 1500, "met_target": False,
            "latency_ms": {"p50": 1, "p95": 2, "p99": 3},
            "extra": {"accepted": 3000, "delivered": 1500, "duplicates": 2, "first_to_last_s": 60}}})
        r["tested"]["describe"] = "v1"
        self.assertIn("delivered 1500 of 3000 accepted", run.summary(r))


class Sources(unittest.TestCase):
    def test_enough_addresses_to_stay_under_the_per_ip_limit(self):
        self.assertEqual(run.source_count(1000), 56)
        self.assertLessEqual(1000 / run.source_count(1000), 20)
        self.assertEqual(run.source_count(1), 1)
        self.assertEqual(run.source_count(100000), 254)

    def test_the_smtp_loads_together_stay_under_what_one_gateway_allows(self):
        # They share one gateway's total. At 500 + 500 + 100 they asked for more than
        # it allows, and most of the second real run's errors were it closing them.
        together = 2 * run.SMTP_MAX_INFLIGHT + run.SMTP_IN_GREYLIST_MAX_INFLIGHT
        self.assertLessEqual(together, run.MAX_GLOBAL - 100)
        limits = (run.ROOT / "cmd/smtp-gateway/main.go").read_text()
        self.assertIn(f"MaxGlobal: {run.MAX_GLOBAL}", limits)

    def test_generators_add_the_addresses_they_are_told_to_use(self):
        a = run.parse_args([])
        script, flags = run.from_sources(a, 203, 56)
        self.assertEqual(flags, ["-source-ips", "10.99.203.1-56"])
        self.assertIn("-le 56", script)
        self.assertIn("ip addr add 10.99.203.$i/16 dev eth0", script)
        jobs = run.build_jobs(a, Path("/lg"), Path("/raw"), Path("/raw/r"), "1:1", "r")
        for kind in ("smtp_in", "smtp_out", "imap"):
            self.assertIn("NET_ADMIN", jobs[kind], kind)
            self.assertIn("-source-ips", jobs[kind], kind)
        self.assertNotIn("NET_ADMIN", jobs["smtp_delivery"])


class Greylist(unittest.TestCase):
    """smtp_in measures the gateway, with the greylist's cost as a figure of its own."""

    def jobs(self, *argv):
        a = run.parse_args(list(argv))
        return a, run.build_jobs(a, Path("/lg"), Path("/raw"), Path("/raw/r"), "1:1", "r")

    @staticmethod
    def arg(cmd, flag):
        return cmd[cmd.index(flag) + 1]

    def test_the_allowed_sender_is_smtp_ins_and_not_the_greylisted_ones(self):
        a, jobs = self.jobs()
        self.assertEqual(self.arg(jobs["smtp_in"], "-from"), a.smtp_in_from)
        command = run.allow_command(a, Path("/lg"))
        self.assertEqual(self.arg(command, "-sender"), self.arg(jobs["smtp_in"], "-from"))
        self.assertEqual(self.arg(command, "-domain"), "restmail.test")
        self.assertNotIn("-remove", command)
        self.assertIn("-remove", run.allow_command(a, Path("/lg"), remove=True))
        # An exact address, so the other load's senders at the same domain don't match.
        self.assertTrue(a.smtp_in_from.split("@")[0])  # not "@domain"
        self.assertNotEqual(a.smtp_in_greylist_from, a.smtp_in_from)

    def test_the_greylist_load_expects_deferrals_from_new_senders_at_the_spf_domain(self):
        a, jobs = self.jobs()
        job = jobs["smtp_in_greylist"]
        script = job[job.index("sh") + 2]
        self.assertIn("-expect-defer", script)
        self.assertIn("-unique-from", script)
        self.assertEqual(self.arg(job, "-from").split("@")[1], a.sink_domain)
        self.assertEqual(self.arg(job, "-to"), self.arg(jobs["smtp_in"], "-to"))
        self.assertNotIn("-expect-defer", jobs["smtp_in"][jobs["smtp_in"].index("sh") + 2])

    def test_the_greylist_load_has_its_own_source_addresses(self):
        _, jobs = self.jobs()
        self.assertNotEqual(self.arg(jobs["smtp_in"], "-source-ips"), self.arg(jobs["smtp_in_greylist"], "-source-ips"))
        self.assertEqual(self.arg(jobs["smtp_in_greylist"], "-source-ips"), f"10.99.{run.SMTP_IN_GREYLIST_SOURCES}.1-6")

    def test_a_rate_of_zero_leaves_it_out(self):
        _, jobs = self.jobs("--smtp-in-greylist-rate", "0")
        self.assertNotIn("smtp_in_greylist", jobs)
        self.assertIn("smtp_in", jobs)

    def test_the_seeded_admin_is_the_one_the_e2e_suite_signs_in_as(self):
        a = run.parse_args([])
        suite = (run.ROOT / "tests/e2e/suite_test.go").read_text()
        self.assertIn(f'seedAdminAddress  = "{a.admin_user}"', suite)
        self.assertIn(f'seedAdminPassword = "{a.admin_password}"', suite)

    def test_the_default_pipeline_still_greylists_and_the_allowlist_comes_first(self):
        templates = (run.ROOT / "internal/pipeline/templates.go").read_text()
        self.assertLess(templates.index('Name:    "domain_allowlist"'), templates.index('Name:    "greylist"'))

    def test_the_workflow_passes_the_rate_on(self):
        workflow = (run.ROOT / ".github/workflows/load.yml").read_text()
        self.assertIn("LOAD_SMTP_IN_GREYLIST_RATE: ${{ inputs.smtp_in_greylist_rate }}", workflow)


class Evidence(unittest.TestCase):
    """What explains a run reaches the artifact."""

    def test_the_tables_reported_are_real(self):
        models = "".join(p.read_text() for p in (run.ROOT / "internal/db/models").glob("*.go"))
        for table in ("outbound_queue", "message_traces", "greylist_entries"):
            self.assertIn(table, run.DB_REPORT)
            self.assertIn(f'"{table}"', models)

    def test_the_artifact_takes_what_is_saved(self):
        workflow = (run.ROOT / ".github/workflows/load.yml").read_text()
        self.assertIn("/load/raw/*.log", workflow)

    def test_logs_and_the_report_are_saved_for_the_services_there(self):
        with tempfile.TemporaryDirectory() as d:
            raw = Path(d)
            calls = []

            def fake_run(cmd, **kw):
                calls.append(cmd)
                kw["stdout"].write("line\n")
                kw["stdout"].flush()

            original, run.subprocess.run = run.subprocess.run, fake_run
            try:
                run.save_evidence(raw, ["rest-mail-api", "rest-mail-postgres", "testbed-dnsmasq"])
            finally:
                run.subprocess.run = original
            self.assertEqual(sorted(p.name for p in raw.iterdir()), ["db-report.log", "service-rest-mail-api.log"])
            self.assertEqual(calls[0][:2], ["docker", "logs"])
            self.assertEqual(calls[1][:3], ["docker", "exec", "-i"])

    def test_the_logs_cover_the_whole_run_and_every_gateway_doing_load(self):
        started = run.dt.datetime(2026, 11, 5, 6, 40, 17, tzinfo=run.dt.timezone.utc)
        with tempfile.TemporaryDirectory() as d:
            calls = []

            def fake_run(cmd, **kw):
                calls.append(cmd)
                kw["stdout"].write("line\n")
                kw["stdout"].flush()

            original, run.subprocess.run = run.subprocess.run, fake_run
            try:
                run.save_evidence(Path(d), ["rest-mail-imap-gateway"], started)
            finally:
                run.subprocess.run = original
        self.assertEqual(calls[0], ["docker", "logs", "--since", "2026-11-05T06:40:17Z", "rest-mail-imap-gateway"])

    def test_warnings_are_counted_past_the_statements_around_them(self):
        lines = [
            '{"time":"t","level":"WARN","msg":"smtp: connection rejected by limiter","ip":"10.99.201.1"}',
            "SELECT * FROM messages",
            '{"time":"t","level":"WARN","msg":"smtp: connection rejected by limiter","ip":"10.99.201.2"}',
            '{"time":"t","level":"DEBUG","msg":"filter executed"}',
            '{"time":"t","level":"ERROR","msg":"smtp: delivery failed"}',
            "{not json",
            "[1]",
        ]
        with tempfile.TemporaryDirectory() as d:
            path = Path(d) / "service.log"
            path.write_text("\n".join(lines) + "\n")
            self.assertEqual(run.log_events(path), {"WARN smtp: connection rejected by limiter": 2, "ERROR smtp: delivery failed": 1})

    def test_what_a_container_was_allowed_and_whether_it_hit_that(self):
        class Done:
            def __init__(self, stdout, returncode=0):
                self.stdout, self.returncode = stdout, returncode

        def fake_run(cmd, **kw):
            if cmd[1] == "inspect":
                return Done("1500000000 1073741824 0 false\n")
            return Done("low 0\nhigh 0\nmax 12\noom 1\noom_kill 1\nusage_usec 9\nnr_throttled 3\nthrottled_usec 41500000\n")

        original, run.subprocess.run = run.subprocess.run, fake_run
        try:
            facts = run.container_facts(["rest-mail-imap-gateway"])
        finally:
            run.subprocess.run = original
        self.assertEqual(facts["rest-mail-imap-gateway"], {"cpus": 1.5, "memory_mib": 1024, "restarts": 0, "oom_killed": False, "oom_kills": 1, "throttled_s": 41.5})

    def test_a_container_that_cannot_be_read_does_not_lose_the_run(self):
        def fails(cmd, **kw):
            raise OSError("no docker")

        original, run.subprocess.run = run.subprocess.run, fails
        try:
            self.assertEqual(run.container_facts(["x"]), {"x": {"error": "no docker"}})
        finally:
            run.subprocess.run = original

    def test_the_summary_shows_every_error_each_command_and_the_ceilings(self):
        r = {
            "tested": {"commit": "abc", "describe": "v1"}, "started_at": "t", "duration_s": 60,
            "loads": {"imap": {
                "kind": "imap", "target": 1000, "target_unit": "connections", "achieved_rate": 999, "ok": 518, "errors": 1000, "met_target": False,
                "error_kinds": {f"kind {i}": i + 1 for i in range(5)}, "other_error_example": "something else",
                "extra": {"noop": {"ok": 500, "errors": 0, "latency_ms": {"p50": 0.3, "p99": 6}}, "select": {"ok": 18, "errors": 981, "latency_ms": {"p50": 20000, "p99": 29000}}},
            }},
            "resources": {"rest-mail-postgres": {"cpu_pct": {"max": 104.05}, "mem_mib": {"max": 305.2}, "net_io_last": "87.6MB / 1.81GB", "cpus": 1.0, "memory_mib": 512, "throttled_s": 41.5, "oom_kills": 0, "restarts": 2}},
            "log_events": {"rest-mail-smtp-gateway": {"WARN smtp: connection rejected by limiter": 3000}},
        }
        text = run.summary(r)
        for want in ("1 x kind 0", "5 x kind 4", "the first of the other errors: something else", "select: 18 ok, 981 errors, p50 20000ms p99 29000ms",
                     "net 87.6MB / 1.81GB  of 1.0 CPU, 512 MiB  throttled 41.5s  oom kills 0  restarts 2", "rest-mail-smtp-gateway: 3000 x WARN smtp: connection rejected by limiter"):
            self.assertIn(want, text)

    def test_a_docker_failure_does_not_lose_the_run(self):
        with tempfile.TemporaryDirectory() as d:
            raw = Path(d)

            def fails(cmd, **kw):
                raise OSError("no docker")

            original, run.subprocess.run = run.subprocess.run, fails
            try:
                run.save_evidence(raw, ["rest-mail-api", "rest-mail-postgres"])
            finally:
                run.subprocess.run = original
            self.assertIn("no docker", (raw / "db-report.log").read_text())


class Limits(unittest.TestCase):
    """The load switch names settings the server reads, and only on the testbed."""

    def test_the_switch_sets_real_settings(self):
        root = run.ROOT
        config = (root / "internal/config/config.go").read_text() + (root / "internal/config/send_limits.go").read_text()
        gateway = (root / "tasks/smtp-gateway.yml").read_text()
        for name in ("API_SEND_RATE_LIMIT_PER_MINUTE", "API_SEND_RATE_LIMIT_PER_HOUR"):
            self.assertIn(f'"{name}"', config)
            self.assertIn(f"-e {name}=0", gateway)
        # internal/config/send_limits.go builds the rest from a prefix and a name.
        self.assertIn('"SEND_LIMIT"', config)
        for name in ("MESSAGES_PER_DAY", "RECIPIENTS_PER_HOUR", "RECIPIENTS_PER_DAY"):
            self.assertIn(f'"{name}"', config)
            self.assertIn(f"-e SEND_LIMIT_{name}=0", gateway)
        self.assertIn('"AUTH_RATE_LIMIT_ENABLED"', config)
        self.assertIn("AUTH_RATE_LIMIT_ENABLED=false", (root / "tasks/api.yml").read_text())

    def test_the_run_turns_it_on_and_a_plain_bring_up_does_not(self):
        root = run.ROOT
        self.assertIn('RESTMAIL_LOAD_LIMITS_OFF: "true"', (root / ".github/workflows/load.yml").read_text())
        chores = (root / "chores.yml").read_text()
        self.assertIn('RESTMAIL_LOAD_LIMITS_OFF=true "${CHORE_BIN:-chore}" e2e:up', chores)
        self.assertEqual(chores.count("RESTMAIL_LOAD_LIMITS_OFF | default \"false\""), 2)
        self.assertNotIn("RESTMAIL_LOAD_LIMITS_OFF", (root / ".github/workflows/e2e-suite.yml").read_text())


class Compare(unittest.TestCase):
    def test_a_slower_run_is_a_regression(self):
        a = results({"smtp_in": load(100, 10)})
        b = results({"smtp_in": load(80, 10)})
        _, n = compare.compare(a, b, 10)
        self.assertEqual(n, 1)

    def test_within_threshold_is_not(self):
        a = results({"smtp_in": load(100, 10)})
        b = results({"smtp_in": load(95, 10.5)})
        _, n = compare.compare(a, b, 10)
        self.assertEqual(n, 0)

    def test_new_errors_and_latency_count(self):
        a = results({"api": load(100, 10)})
        b = results({"api": load(100, 20, errors=3)})
        _, n = compare.compare(a, b, 10)
        self.assertEqual(n, 3)  # p95, p99, errors

    def delivery(self, delivered, p95, accepted=3000):
        l = load(delivered / 60, p95, errors=accepted - delivered)
        l["extra"] = {"accepted": accepted, "delivered": delivered}
        return l

    def test_mail_no_longer_delivered_is_a_regression_though_as_much_was_accepted(self):
        a = results({"smtp_out": load(50, 10), "smtp_delivery": self.delivery(3000, 900)})
        b = results({"smtp_out": load(50, 10), "smtp_delivery": self.delivery(2000, 900)})
        lines, n = compare.compare(a, b, 10)
        # The rate, the count delivered, and the errors; smtp_out did not move.
        self.assertEqual(n, 3)
        self.assertTrue(any("delivered" in l and "3000" in l and "2000" in l and "REGRESSION" in l for l in lines), lines)

    def test_slower_delivery_is_a_regression_though_all_arrived(self):
        a = results({"smtp_delivery": self.delivery(3000, 900)})
        b = results({"smtp_delivery": self.delivery(3000, 5000)})
        _, n = compare.compare(a, b, 10)
        self.assertEqual(n, 2)  # p95, p99

    def test_the_same_delivery_is_not(self):
        a = results({"smtp_delivery": self.delivery(3000, 900)})
        _, n = compare.compare(a, results({"smtp_delivery": self.delivery(3000, 900)}), 10)
        self.assertEqual(n, 0)

    def test_resources_are_shown_not_counted(self):
        res = lambda m: {"rest-mail-api": {"cpu_pct": {"max": 10}, "mem_mib": {"max": m}}}  # noqa: E731
        a = results({"api": load(100, 10)}, res(100))
        b = results({"api": load(100, 10)}, res(300))
        lines, n = compare.compare(a, b, 10)
        self.assertEqual(n, 0)
        self.assertTrue(any("rest-mail-api mem" in l for l in lines))


if __name__ == "__main__":
    unittest.main()
