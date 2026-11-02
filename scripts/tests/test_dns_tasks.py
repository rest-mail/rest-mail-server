"""Exercise DNS lifecycle output and checks without contacting a cluster or resolver."""

from pathlib import Path
import shutil
import subprocess
import unittest


ROOT = Path(__file__).resolve().parents[2]


@unittest.skipUnless(shutil.which("jq"), "DNS lifecycle tests require jq")
class DNSTaskTests(unittest.TestCase):
    def test_policy_hosts_show_check_and_export(self):
        result = subprocess.run(
            ["bash", str(ROOT / "tests/first-start/dns-test.sh")],
            cwd=ROOT,
            capture_output=True,
            text=True,
            timeout=15,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
