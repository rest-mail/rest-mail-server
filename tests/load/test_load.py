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
