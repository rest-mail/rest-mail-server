"""Verify the pinned tool's skipped-probe verdict and JUnit representation."""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import unittest
import xml.etree.ElementTree as ET

import gate


@unittest.skipUnless(os.environ.get("RESTMAIL_VERTRAG_BINARY"), "set RESTMAIL_VERTRAG_BINARY")
class VertragSkippedProbes(unittest.TestCase):
    def test_flag_fails_only_when_valid_probes_are_skipped(self):
        binary = os.environ["RESTMAIL_VERTRAG_BINARY"]
        help_result = subprocess.run([binary, "run", "--help"], capture_output=True, text=True, timeout=20)
        self.assertIn("fail-on-skipped-probes", help_result.stdout + help_result.stderr)

        class API(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                self.server.requests += 1
                target = self.path.rsplit("/", 1)[-1]
                valid = target.isdecimal() and int(target) >= 1
                status = 200 if valid else 400
                if self.server.break_baseline and self.server.requests > 1:
                    status = 404
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(b"{}")

        server = ThreadingHTTPServer(("127.0.0.1", 0), API)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            spec = directory / "spec.yml"
            spec.write_text("""openapi: 3.1.0
info: {title: Skipped probes, version: 1.0.0}
paths:
  /things/{id}:
    get:
      operationId: getThing
      summary: Thing
      parameters:
        - name: id
          in: path
          required: true
          example: 7
          schema: {type: integer, minimum: 1}
      responses:
        '200':
          description: Found
          content:
            application/json:
              schema: {type: object}
""")
            for breaks, flag, expected in ((True, False, 0), (True, True, 2), (False, True, 0)):
                with self.subTest(breaks=breaks, flag=flag):
                    server.requests = 0
                    server.break_baseline = breaks
                    report = directory / "report.xml"
                    command = [binary, "run", "--endpoint", f"http://127.0.0.1:{server.server_port}",
                               "--phases", "coverage", "--reporter", "junit", "--output", str(report)]
                    if flag:
                        command.append("--fail-on-skipped-probes")
                    command.append(str(spec))
                    result = subprocess.run(command, cwd=directory, capture_output=True, text=True, timeout=30)
                    self.assertEqual(result.returncode, expected, result.stdout + result.stderr)
                    counts = gate.skipped_counts(report, gate.operations(spec))
                    self.assertEqual(counts, {"getThing": 1} if breaks else {})
                    self.assertTrue(list(ET.parse(report).getroot().iter("testcase")))


if __name__ == "__main__":
    unittest.main()
