"""The local HTTPS front validates replacements before reloading nginx."""

import os
import subprocess
import tempfile
import time
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


class TLSFrontReloadTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)
        self.cert = self.directory / "tls.crt"
        self.cert.write_text("initial")
        self.log = self.directory / "reloads"
        binary = self.directory / "nginx"
        binary.write_text("""#!/usr/bin/env python3
import os, signal, sys, time
from pathlib import Path
cert = Path(os.environ['FRONT_TEST_CERT'])
log = Path(os.environ['FRONT_TEST_LOG'])
if '-t' in sys.argv:
    sys.exit(0 if cert.exists() and cert.read_text() != 'invalid' else 1)
def reload(*unused):
    with log.open('a') as output:
        output.write(cert.read_text() + '\\n')
signal.signal(signal.SIGHUP, reload)
Path(os.environ['FRONT_TEST_READY']).write_text('ready')
while True:
    time.sleep(0.01)
""")
        binary.chmod(0o755)
        self.output = (self.directory / "output").open("w+")
        self.addCleanup(self.output.close)
        self.process = subprocess.Popen(["sh", str(ROOT / "tasks/tls-front-reload.sh"), "events {}", str(self.cert)],
                                        env={**os.environ, "PATH": str(self.directory) + os.pathsep + os.environ["PATH"],
                                             "TMPDIR": str(self.directory),
                                             "TLS_FRONT_RELOAD_INTERVAL": "0.02", "FRONT_TEST_CERT": str(self.cert),
                                             "FRONT_TEST_READY": str(self.directory / "ready"),
                                             "FRONT_TEST_LOG": str(self.log)}, stdout=self.output, stderr=self.output)
        self.addCleanup(self.stop)
        deadline = time.monotonic() + 5
        while not (self.directory / "ready").exists():
            if time.monotonic() > deadline:
                self.fail("fake nginx never started")
            time.sleep(0.01)

    def stop(self):
        if self.process.poll() is None:
            self.process.terminate()
        self.process.wait(timeout=5)

    def wait_for(self, expected):
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            if self.log.exists() and expected in self.log.read_text():
                return
            time.sleep(0.01)
        self.fail(f"no nginx reload for {expected}")

    def wait_report(self):
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            self.output.seek(0)
            if "reload failed; retaining last usable pair" in self.output.read():
                return
            time.sleep(0.01)
        self.fail("reload failure was not reported")

    def test_valid_replacement_signals_the_running_master(self):
        self.cert.write_text("renewed")
        self.wait_for("renewed")
        self.assertIsNone(self.process.poll())

    def test_invalid_replacement_is_reported_without_signalling_master(self):
        self.cert.write_text("invalid")
        self.wait_report()
        self.assertFalse(self.log.exists())
        self.output.seek(0)
        self.assertIn("reload failed; retaining last usable pair", self.output.read())
        self.cert.write_text("repaired")
        self.wait_for("repaired")

    def test_missing_replacement_is_reported_without_signalling_master(self):
        hidden = self.directory / "hidden.crt"
        self.cert.rename(hidden)
        self.wait_report()
        self.assertFalse(self.log.exists())
        self.output.seek(0)
        self.assertIn("reload failed; retaining last usable pair", self.output.read())
        hidden.rename(self.cert)
        self.cert.write_text("repaired")
        self.wait_for("repaired")


if __name__ == "__main__":
    unittest.main()
