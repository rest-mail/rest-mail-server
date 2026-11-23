import copy
import json
import os
import subprocess
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]


class FuzzAuthLifetimeTests(unittest.TestCase):
    def test_configured_fuzz_session_renews_after_setup_without_changing_principal_or_body(
        self,
    ):
        binary = Path(os.environ.get("CONTRACT_VERTRAG", ROOT / ".cache/bin/vertrag"))
        if not binary.is_file():
            self.skipTest("the pinned Vertrag executable is not installed")
        auth = copy.deepcopy(yaml.safe_load((ROOT / "vertrag.yml").read_text())["auth"])
        auth.pop("except", None)
        logins = []
        requests = []
        session = {"token": "", "until": 0}

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_POST(self):
                body = self.rfile.read(
                    int(self.headers.get("Content-Length", 0))
                ).decode()
                if self.path == auth["login"]["path"]:
                    logins.append(json.loads(body))
                    session.update(
                        token=f"session-{len(logins)}", until=time.monotonic() + 1
                    )
                    response = json.dumps(
                        {"data": {"access_token": session["token"], "expires_in": 1}}
                    ).encode()
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(response)))
                    self.end_headers()
                    self.wfile.write(response)
                    return
                valid = (
                    self.headers.get("Authorization") == "Bearer " + session["token"]
                    and time.monotonic() < session["until"]
                )
                requests.append((body, valid))
                self.send_response(201 if valid else 401)
                self.send_header("Content-Length", "0")
                self.end_headers()

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        worker = threading.Thread(target=server.serve_forever, daemon=True)
        worker.start()
        self.addCleanup(worker.join)
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory)
            observed = temporary / "observed.jsonl"
            hooks = temporary / "hooks.py"
            hooks.write_text(
                "import json, time\nimport vertrag_hooks as hooks\n"
                "@hooks.before('sendMessage')\ndef slow_setup(transaction):\n"
                f"    with open({str(observed)!r}, 'a') as stream:\n"
                "        stream.write(json.dumps(transaction['request']['body']) + '\\n')\n"
                "    time.sleep(1.1)\n"
            )
            description = temporary / "openapi.json"
            description.write_text(
                json.dumps(
                    {
                        "openapi": "3.1.0",
                        "info": {"title": "Session lifetime", "version": "1"},
                        "paths": {
                            "/send": {
                                "post": {
                                    "operationId": "sendMessage",
                                    "summary": "Send unchanged generated input",
                                    "requestBody": {
                                        "required": True,
                                        "content": {
                                            "application/json": {
                                                "schema": {
                                                    "type": "object",
                                                    "additionalProperties": False,
                                                    "required": ["message"],
                                                    "properties": {
                                                        "message": {
                                                            "type": "string",
                                                            "minLength": 1,
                                                        }
                                                    },
                                                },
                                                "example": {
                                                    "message": "unchanged example"
                                                },
                                            }
                                        },
                                    },
                                    "responses": {"201": {"description": "Accepted"}},
                                }
                            }
                        },
                    }
                )
            )
            config = temporary / "vertrag.yml"
            config.write_text(
                yaml.safe_dump(
                    {
                        "spec": str(description),
                        "endpoint": f"http://127.0.0.1:{server.server_port}",
                        "auth": auth,
                        "hookfiles": str(hooks),
                        "language": "python",
                    }
                )
            )
            result = subprocess.run(
                [
                    str(binary),
                    "run",
                    "--config",
                    str(config),
                    "--phases",
                    "fuzz",
                    "--mode",
                    "valid",
                    "--cases",
                    "1",
                    "--seed",
                    "8177294033058856637",
                    "--no-color",
                ],
                check=False,
                capture_output=True,
                text=True,
                timeout=600,
            )
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertGreaterEqual(len(requests), 3)
            self.assertTrue(all(valid for _, valid in requests), requests)
            self.assertEqual(
                [body for body, _ in requests],
                [json.loads(line) for line in observed.read_text().splitlines()],
            )
            self.assertGreaterEqual(len(logins), len(requests) + 1)
            self.assertTrue(
                all(body == auth["login"]["body"] for body in logins), logins
            )
            self.assertIn("body · valid", result.stdout)


if __name__ == "__main__":
    unittest.main()
