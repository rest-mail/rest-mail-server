"""The measured zero for account time zones remains a gate, not spare budget."""

import contextlib
import io
from pathlib import Path
import tempfile
import unittest

import gate


class TimeZoneLostProbes(unittest.TestCase):
    def test_set_account_time_zone_has_no_lost_probe_allowance(self):
        baseline = Path(__file__).with_name("lost-probes-baseline.txt")
        identifiers = {line.split()[0] for raw in baseline.read_text().splitlines()
                       if (line := raw.split("#", 1)[0].strip())}
        identifiers.add("setAccountTimeZone")
        allowed = gate.skipped_baseline(baseline, identifiers)
        self.assertNotIn("setAccountTimeZone", allowed, "the real whole-API run measured zero")
        existing = "".join((operation + "\n") * count for operation, count in allowed.items())
        with tempfile.TemporaryDirectory() as directory:
            report = Path(directory) / "lost-probes.txt"
            report.write_text(existing)
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertTrue(gate.check_lost_probes(baseline, report, identifiers))
            report.write_text(existing + "setAccountTimeZone\n")
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                self.assertFalse(gate.check_lost_probes(baseline, report, identifiers))
            self.assertIn("setAccountTimeZone: 1 > 0", output.getvalue())


if __name__ == "__main__":
    unittest.main()
