import json
import os
from pathlib import Path
import tempfile
import threading
import types
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest.mock import patch

import live_requirements


TLS_REPORT_LIMIT = 512 * 1024
PAIR = {"requires": {"body": "over_cap"}, "response": {"status": 413}}
DOCUMENT = {
    "openapi": "3.1.0",
    "info": {"title": "TLS report body cap", "version": "1"},
    "paths": {
        "/.well-known/smtp-tlsrpt": {
            "post": {
                "operationId": "receiveTlsReport",
                "requestBody": {"content": {"application/tlsrpt+json": {
                    "schema": {"type": "object"},
                    "examples": {"request_too_large": {"value": {}}},
                }}},
                "responses": {"413": {
                    "description": "Report exceeds 512 KiB",
                    "content": {"application/json": {
                        "schema": {"type": "object", "additionalProperties": False,
                                   "required": ["error"], "properties": {"error": {"type": "string"}}},
                        "examples": {"request_too_large": {
                            "value": {"error": "too large"}, "x-requires": PAIR["requires"],
                        }},
                    }},
                }},
            },
        },
    },
}


class TLSReportBodyCapTests(unittest.TestCase):
    def test_tls_report_uses_its_actual_limit_and_records_the_body_size(self):
        def run(command, **kwargs):
            self.assertEqual(kwargs["env"]["CONTRACT_OVER_CAP_BYTES"], str(TLS_REPORT_LIMIT + 1))
            config_path = Path(command[command.index("--config") + 1])
            (config_path.parent / "report.json").write_text(json.dumps({
                "summary": {"passed": True},
                "results": [{"status": "pass", "expected": "413", "actual": "413"}],
            }))
            return types.SimpleNamespace(returncode=0)

        with tempfile.TemporaryDirectory() as directory, patch.object(
            live_requirements.subprocess, "run", side_effect=run,
        ):
            code = live_requirements.prove(
                "vertrag", "http://localhost", DOCUMENT,
                {"receiveTlsReport": {"request_too_large": PAIR}},
                "over-cap", directory, "session",
            )
            inventory = json.loads((Path(directory) / "over-cap-inventory.json").read_text())
        self.assertEqual(code, 0)
        self.assertEqual(inventory["body_bytes_by_operation"], {"receiveTlsReport": TLS_REPORT_LIMIT + 1})

    @unittest.skipUnless(os.environ.get("RESTMAIL_VERTRAG_BINARY"), "set RESTMAIL_VERTRAG_BINARY")
    def test_streaming_tls_report_rejection_receives_every_over_limit_byte(self):
        received = []

        class API(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *args):
                pass

            def do_POST(self):
                size = len(self.rfile.read(TLS_REPORT_LIMIT + 1))
                received.append((int(self.headers["Content-Length"]), size))
                reply = json.dumps({"error": "too large"}).encode()
                self.send_response(413)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(reply)))
                self.send_header("Connection", "close")
                self.end_headers()
                self.wfile.write(reply)
                self.wfile.flush()
                self.close_connection = True

        server = ThreadingHTTPServer(("127.0.0.1", 0), API)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        with tempfile.TemporaryDirectory() as directory:
            code = live_requirements.prove(
                os.environ["RESTMAIL_VERTRAG_BINARY"], f"http://127.0.0.1:{server.server_port}", DOCUMENT,
                {"receiveTlsReport": {"request_too_large": PAIR}},
                "over-cap", directory, "session",
            )
            proofs = json.loads((Path(directory) / "over-cap-proofs.json").read_text())
        self.assertEqual(code, 0)
        self.assertEqual(proofs, {"receiveTlsReport": ["request_too_large"]})
        self.assertEqual(received, [(TLS_REPORT_LIMIT + 1, TLS_REPORT_LIMIT + 1)])
