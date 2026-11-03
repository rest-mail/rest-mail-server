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

    def test_resources_are_shown_not_counted(self):
        res = lambda m: {"rest-mail-api": {"cpu_pct": {"max": 10}, "mem_mib": {"max": m}}}  # noqa: E731
        a = results({"api": load(100, 10)}, res(100))
        b = results({"api": load(100, 10)}, res(300))
        lines, n = compare.compare(a, b, 10)
        self.assertEqual(n, 0)
        self.assertTrue(any("rest-mail-api mem" in l for l in lines))


if __name__ == "__main__":
    unittest.main()
