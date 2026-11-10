"""A live proof requires a real, successful, uniquely selected exchange."""

import copy
import importlib.util
import json
import os
import sys
import tempfile
import threading
import types
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch

import yaml

import live_requirements

PAIR = {"requires": {"principal": "anonymous"}, "response": {"status": 401}}
PARAMETER = {
    "name": "id",
    "in": "path",
    "required": True,
    "schema": {"type": "integer"},
    "examples": {"unauthenticated": {"value": 7}, "found": {"value": 10}},
}
SCHEMA = {
    "type": "object",
    "additionalProperties": False,
    "required": ["error"],
    "properties": {"error": {"type": "string"}},
}
DOCUMENT = {
    "openapi": "3.1.0",
    "info": {"title": "Proof", "version": "1"},
    "paths": {
        "/things/{id}": {
            "parameters": [PARAMETER],
            "get": {
                "operationId": "getThing",
                "responses": {
                    "200": {"description": "OK"},
                    "401": {
                        "description": "Unauthorized",
                        "headers": {"X-Guard": {"schema": {"type": "string"}}},
                        "content": {
                            "application/json": {
                                "schema": SCHEMA,
                                "examples": {
                                    "unauthenticated": {
                                        "value": {"error": "missing"},
                                        "x-requires": PAIR["requires"],
                                    },
                                    "other": {"value": {"error": "other"}},
                                },
                            }
                        },
                    },
                },
            },
            "delete": {
                "operationId": "deleteThing",
                "responses": {"204": {"description": "OK"}},
            },
        }
    },
}
REPORT = {
    "summary": {"passed": True},
    "results": [{"status": "pass", "expected": "401", "actual": "401"}],
}


class LiveRequirementTests(unittest.TestCase):
    def test_selection_preserves_response_contract_and_original_document(self):
        original = copy.deepcopy(DOCUMENT)
        selected = live_requirements.select_pair(
            DOCUMENT, "getThing", "unauthenticated", PAIR
        )
        self.assertEqual(DOCUMENT, original)
        self.assertNotIn("delete", selected["paths"]["/things/{id}"])
        operation = selected["paths"]["/things/{id}"]["get"]
        self.assertEqual(set(operation["responses"]), {"401"})
        response = operation["responses"]["401"]
        self.assertEqual(response["content"]["application/json"]["schema"], SCHEMA)
        self.assertEqual(
            response["headers"],
            original["paths"]["/things/{id}"]["get"]["responses"]["401"]["headers"],
        )
        self.assertEqual(
            set(response["content"]["application/json"]["examples"]),
            {"unauthenticated"},
        )
        self.assertEqual(
            selected["paths"]["/things/{id}"]["parameters"][0]["examples"],
            {"unauthenticated": {"value": 7}},
        )

    def test_missing_or_changed_served_pair_is_not_a_proof(self):
        for operation, name, pair in (
            ("missing", "unauthenticated", PAIR),
            ("getThing", "absent", PAIR),
            ("getThing", "unauthenticated", {**PAIR, "requires": {"body": "over_cap"}}),
        ):
            with (
                self.subTest(operation=operation, name=name),
                self.assertRaises(ValueError),
            ):
                live_requirements.select_pair(DOCUMENT, operation, name, pair)

    def test_real_fixture_ids_replace_only_selected_request_values(self):
        selected = live_requirements.select_pair(
            DOCUMENT, "getThing", "unauthenticated", PAIR, {"id": 83}
        )
        parameter = selected["paths"]["/things/{id}"]["parameters"][0]
        self.assertEqual(parameter["examples"], {"unauthenticated": {"value": 83}})
        self.assertEqual(DOCUMENT["paths"]["/things/{id}"]["parameters"][0], PARAMETER)

    def test_oversized_hook_sends_the_declared_number_of_actual_bytes(self):
        stub = types.ModuleType("vertrag_hooks")
        stub.before_each = lambda function: function
        stub.before_each_validation = lambda function: function
        path = Path(live_requirements.__file__).with_name("live_requirement_hooks.py")
        spec = importlib.util.spec_from_file_location("live_body_hook", path)
        module = importlib.util.module_from_spec(spec)
        with (
            patch.dict(sys.modules, {"vertrag_hooks": stub}),
            patch.dict("os.environ", {"CONTRACT_OVER_CAP_BYTES": "8193"}),
        ):
            spec.loader.exec_module(module)
            transaction = {"request": {"body": "{}", "headers": {}}}
            module.oversized_body(transaction)
        self.assertEqual(len(transaction["request"]["body"].encode()), 8193)
        self.assertEqual(transaction["request"]["headers"]["Content-Length"], "8193")
        self.assertEqual(transaction["request"]["headers"]["Expect"], "100-continue")
        # Once sent, the bytes are not carried through the later hooks.
        module.forget_the_sent_body(transaction)
        self.assertEqual(transaction["request"]["body"], "")
        self.assertEqual(transaction["request"]["headers"]["Content-Length"], "8193")

    def test_only_one_passed_expected_exchange_counts(self):
        self.assertTrue(live_requirements.passed(REPORT, 401))
        self.assertFalse(live_requirements.passed(REPORT, 403))
        for status in ("fail", "error", "skip", "skipped", ""):
            report = copy.deepcopy(REPORT)
            report["results"][0]["status"] = status
            self.assertFalse(live_requirements.passed(report, 401))
        for results in ([], REPORT["results"] * 2):
            self.assertFalse(
                live_requirements.passed({**REPORT, "results": results}, 401)
            )
        for field, value in (
            ("actual", "403"),
            ("errors", ["body mismatch"]),
            ("additional", ["failed header check"]),
        ):
            report = copy.deepcopy(REPORT)
            report["results"][0][field] = value
            self.assertFalse(live_requirements.passed(report, 401))

    def run_proof(self, directory, code=0, report=REPORT):
        def run(command, **kwargs):
            config_path = Path(command[command.index("--config") + 1])
            self.assertEqual(config_path.stat().st_mode & 0o777, 0o600)
            Path(config_path.parent / "report.json").write_text(json.dumps(report))
            return types.SimpleNamespace(returncode=code)

        with patch.object(live_requirements.subprocess, "run", side_effect=run):
            return live_requirements.prove(
                "vertrag",
                "http://localhost",
                DOCUMENT,
                {"getThing": {"unauthenticated": PAIR}},
                "anonymous",
                directory,
            )

    def test_failed_runner_cannot_leave_old_proofs(self):
        with tempfile.TemporaryDirectory() as directory:
            self.assertEqual(self.run_proof(directory), 0)
            path = Path(directory) / "anonymous-proofs.json"
            self.assertEqual(
                json.loads(path.read_text()), {"getThing": ["unauthenticated"]}
            )
            self.assertEqual(self.run_proof(directory, code=1), 1)
            self.assertEqual(json.loads(path.read_text()), {})
            self.assertEqual(
                len(
                    json.loads(
                        (Path(directory) / "anonymous-unproven.json").read_text()
                    )
                ),
                1,
            )

    def test_no_matching_examples_is_not_a_success(self):
        with tempfile.TemporaryDirectory() as directory:
            self.assertEqual(
                live_requirements.prove(
                    "vertrag", "http://localhost", DOCUMENT, {}, "anonymous", directory
                ),
                1,
            )

    def test_cases_side_by_side_prove_the_same_as_one_at_a_time(self):
        pairs = {
            f"case{index}": {**PAIR, "requires": {"body": "over_cap"}}
            for index in range(8)
        }
        document = copy.deepcopy(DOCUMENT)
        media = document["paths"]["/things/{id}"]["get"]["responses"]["401"]["content"]
        media["application/json"]["examples"] = {
            name: {"value": {"error": name}, "x-requires": {"body": "over_cap"}}
            for name in pairs
        }
        renewals, seen = [], set()

        def run(command, **kwargs):
            config_path = Path(command[command.index("--config") + 1])
            # No case shares another's files, and only one of them fails.
            self.assertNotIn(config_path, seen)
            seen.add(config_path)
            failing = "case3" in config_path.parent.name
            report = copy.deepcopy(REPORT)
            if failing:
                report["results"][0]["status"] = "fail"
            (config_path.parent / "report.json").write_text(json.dumps(report))
            return types.SimpleNamespace(returncode=0)

        with (
            tempfile.TemporaryDirectory() as directory,
            patch.object(live_requirements.subprocess, "run", side_effect=run),
        ):
            code = live_requirements.prove(
                "vertrag",
                "http://localhost",
                document,
                {"getThing": pairs},
                "over-cap",
                directory,
                "a-session",
                renew_token=lambda: renewals.append(1) or "renewed",
                workers=4,
            )
            proofs = json.loads((Path(directory) / "over-cap-proofs.json").read_text())
            unproven = json.loads(
                (Path(directory) / "over-cap-unproven.json").read_text()
            )
        self.assertEqual(code, 1)
        self.assertEqual(len(seen), 8)
        self.assertEqual(proofs["getThing"], sorted(set(pairs) - {"case3"}))
        self.assertEqual([item["example"] for item in unproven], ["case3"])
        self.assertEqual(renewals, [])

    def test_an_empty_report_is_unproven_and_names_the_exit(self):
        def run(command, **kwargs):
            config_path = Path(command[command.index("--config") + 1])
            (config_path.parent / "report.json").write_text("")
            return types.SimpleNamespace(returncode=70)

        with (
            tempfile.TemporaryDirectory() as directory,
            patch.object(live_requirements.subprocess, "run", side_effect=run),
        ):
            code = live_requirements.prove(
                "vertrag",
                "http://localhost",
                DOCUMENT,
                {"getThing": {"unauthenticated": PAIR}},
                "anonymous",
                directory,
            )
            unproven = json.loads(
                (Path(directory) / "anonymous-unproven.json").read_text()
            )
            proofs = json.loads((Path(directory) / "anonymous-proofs.json").read_text())
        self.assertEqual(code, 1)
        self.assertEqual(proofs, {})
        self.assertIn("vertrag exit 70", unproven[0]["reason"])


    def test_empty_report_keeps_private_runner_diagnostics(self):
        diagnostic = b"hook startup failed; Authorization: Bearer disposable-secret\n"
        with (
            tempfile.TemporaryDirectory() as directory,
            patch.object(
                live_requirements.subprocess,
                "run",
                return_value=types.SimpleNamespace(
                    returncode=1, stdout=b"runner stopped\n", stderr=diagnostic
                ),
            ),
        ):
            code = live_requirements.prove(
                "vertrag",
                "http://localhost",
                DOCUMENT,
                {"getThing": {"unauthenticated": PAIR}},
                "anonymous",
                directory,
            )
            unproven = json.loads(
                (Path(directory) / "anonymous-unproven.json").read_text()
            )
            for stream, expected in (
                ("stdout", b"runner stopped\n"), ("stderr", diagnostic)
            ):
                saved = Path(directory) / f"anonymous-getThing-unauthenticated.{stream}.log"
                self.assertTrue(saved.is_file(), str(saved))
                self.assertEqual(saved.read_bytes(), expected)
                self.assertEqual(saved.stat().st_mode & 0o777, 0o600)
                self.assertIn(saved.name, unproven[0]["reason"])
            self.assertEqual(code, 1)
            self.assertNotIn("disposable-secret", unproven[0]["reason"])
            self.assertEqual(
                json.loads((Path(directory) / "anonymous-proofs.json").read_text()), {}
            )


class GatewayListenerProof(unittest.TestCase):
    def setUp(self):
        self.operation = "closeGatewaySession"
        self.example = "session_required"
        self.pair = {"requires": {"principal": "anonymous"}, "response": {"status": 400}}
        self.document = copy.deepcopy(DOCUMENT)
        operation = self.document["paths"]["/things/{id}"]["get"]
        operation["operationId"] = self.operation
        operation["responses"] = {"400": {
            "description": "A gateway session token is required",
            "content": {"application/json": {
                "schema": SCHEMA,
                "examples": {self.example: {
                    "value": {"error": "session required"},
                    "x-requires": self.pair["requires"],
                }},
            }},
        }}
        self.document["paths"] = {"/api/v1/gateway-sessions/current": {"delete": operation}}
        self.configurations = []

    def invoke(self, endpoint="https://internal.test:8443", transport=None, kind="anonymous"):
        def run(command, **options):
            config_path = Path(command[command.index("--config") + 1])
            self.configurations.append(yaml.safe_load(config_path.read_text()))
            verdict = copy.deepcopy(REPORT)
            expected = str(self.pair["response"]["status"])
            verdict["results"][0].update(expected=expected, actual=expected)
            (config_path.parent / "report.json").write_text(json.dumps(verdict))
            return types.SimpleNamespace(returncode=0)

        with (
            tempfile.TemporaryDirectory() as directory,
            patch.dict(os.environ, {"CONTRACT_INTERNAL_ENDPOINT": endpoint,
                                    "CONTRACT_MTLS_DIR": directory}),
            patch.object(live_requirements.subprocess, "run", side_effect=run),
        ):
            code = live_requirements.prove(
                "vertrag", "http://public.test:8080", self.document,
                {self.operation: {self.example: self.pair}}, kind, directory,
                token="actual-mfa-session" if kind == "over-cap" else "", transport=transport,
            )
            unproven = json.loads((Path(directory) / f"{kind}-unproven.json").read_text())
            return code, directory, unproven

    def test_missing_session_uses_verified_gateway_transport_without_a_user_credential(self):
        original = copy.deepcopy(self.document)
        code, directory, unproven = self.invoke()
        self.assertEqual((code, unproven), (0, []))
        config = self.configurations[0]
        self.assertEqual(config["endpoint"], "https://internal.test:8443")
        self.assertEqual(config["principals"], [{"name": "anonymous"}])
        self.assertNotIn("header", config)
        self.assertEqual(config["endpoints"], [{
            "endpoint": "https://internal.test:8443",
            "operation-id": [self.operation],
            "transport": {"ca-cert": str(Path(directory).resolve() / "internal-ca.crt"),
                          "cert": str(Path(directory).resolve() / "internal-imap.crt"),
                          "cert-key": str(Path(directory).resolve() / "internal-imap.key")},
        }])
        self.assertEqual(self.document, original)

    def test_an_insecure_internal_listener_is_unproven_not_a_public_fallback(self):
        code, _, unproven = self.invoke("http://internal.test:8080")
        self.assertEqual(code, 1)
        self.assertEqual(self.configurations, [])
        self.assertIn("HTTPS internal listener", unproven[0]["reason"])

    def test_delivery_uses_its_own_service_certificate_and_keeps_the_mfa_credential(self):
        self.operation = "deliverMessage"
        self.pair = {"requires": {"body": "over_cap"}, "response": {"status": 413}}
        operation = self.document["paths"]["/api/v1/gateway-sessions/current"]["delete"]
        operation["operationId"] = self.operation
        response = operation["responses"]["400"]
        response["content"]["application/json"]["examples"][self.example]["x-requires"] = self.pair["requires"]
        operation["responses"] = {"413": response}
        self.document["paths"] = {"/api/v1/messages/deliver": {"post": operation}}
        code, directory, unproven = self.invoke(kind="over-cap")
        self.assertEqual((code, unproven), (0, []))
        config = self.configurations[0]
        self.assertEqual(config["endpoint"], "https://internal.test:8443")
        self.assertEqual(config["header"], ["Authorization: Bearer actual-mfa-session"])
        self.assertEqual(config["endpoints"], [{
            "endpoint": "https://internal.test:8443", "operation-id": [self.operation],
            "transport": {"ca-cert": str(Path(directory).resolve() / "internal-ca.crt"),
                          "cert": str(Path(directory).resolve() / "internal-smtp-delivery.crt"),
                          "cert-key": str(Path(directory).resolve() / "internal-smtp-delivery.key")},
        }])

    def test_explicit_transport_is_not_replaced(self):
        explicit = {"ca-cert": "/selected/ca.crt", "cert": "/selected/client.crt",
                    "cert-key": "/selected/client.key"}
        code, _, _ = self.invoke(transport=explicit)
        self.assertEqual(code, 0)
        config = self.configurations[0]
        self.assertEqual(config["endpoint"], "http://public.test:8080")
        self.assertEqual(config["endpoints"][0]["transport"], explicit)


@unittest.skipUnless(
    os.environ.get("RESTMAIL_VERTRAG_BINARY"), "set RESTMAIL_VERTRAG_BINARY"
)
class RealOverCapExchange(unittest.TestCase):
    def test_an_early_413_is_validated_without_a_broken_pipe(self):
        size = 32 * 1024 * 1024 + 1
        received = []

        class API(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *args):
                pass

            def reject(self):
                received.append((self.headers.get("Expect"), int(self.headers["Content-Length"])))
                reply = json.dumps({"error": "too large"}).encode()
                self.send_response(413)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(reply)))
                self.send_header("X-Guard", "body-limit")
                self.send_header("Connection", "close")
                self.end_headers()
                self.wfile.write(reply)
                self.wfile.flush()
                self.close_connection = True

            def handle_expect_100(self):
                self.reject()
                return False

            def do_GET(self):
                self.reject()

        server = ThreadingHTTPServer(("127.0.0.1", 0), API)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        document = copy.deepcopy(DOCUMENT)
        operation = document["paths"]["/things/{id}"]["get"]
        response = operation["responses"]["401"]
        response["content"]["application/json"]["examples"] = {"unauthenticated": {
            "value": {"error": "too large"}, "x-requires": {"body": "over_cap"},
        }}
        operation["responses"] = {"413": response}
        pair = {"requires": {"body": "over_cap"}, "response": {"status": 413}}
        with tempfile.TemporaryDirectory() as directory:
            code = live_requirements.prove(
                os.environ["RESTMAIL_VERTRAG_BINARY"],
                f"http://127.0.0.1:{server.server_port}", document,
                {"getThing": {"unauthenticated": pair}}, "over-cap", directory,
                "a-session", body_bytes=size,
            )
            proofs = json.loads((Path(directory) / "over-cap-proofs.json").read_text())
        self.assertEqual(code, 0)
        self.assertEqual(proofs, {"getThing": ["unauthenticated"]})
        self.assertEqual(received, [("100-continue", size)])

    def test_the_whole_body_is_on_the_wire_before_the_hook_forgets_it(self):
        size = 1024 * 1024 + 1
        received = []

        class API(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *args):
                pass

            def do_POST(self):
                length = int(self.headers["Content-Length"])
                received.append(len(self.rfile.read(length)))
                reply = json.dumps({"error": "too large"}).encode()
                self.send_response(413)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(reply)))
                self.end_headers()
                self.wfile.write(reply)

        server = ThreadingHTTPServer(("127.0.0.1", 0), API)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        requires = {"body": "over_cap"}
        example = {"value": {"error": "too large"}, "x-requires": requires}
        request = {"schema": {"type": "object"}, "examples": {"too_big": {"value": {}}}}
        document = {
            "openapi": "3.1.0",
            "info": {"title": "Proof", "version": "1"},
            "paths": {
                "/things": {
                    "post": {
                        "operationId": "postThing",
                        "requestBody": {"content": {"application/json": request}},
                        "responses": {
                            "413": {
                                "description": "Too large",
                                "content": {
                                    "application/json": {
                                        "schema": SCHEMA,
                                        "examples": {"too_big": example},
                                    }
                                },
                            }
                        },
                    }
                }
            },
        }
        pair = {"requires": requires, "response": {"status": 413}}
        with tempfile.TemporaryDirectory() as directory:
            code = live_requirements.prove(
                os.environ["RESTMAIL_VERTRAG_BINARY"],
                f"http://127.0.0.1:{server.server_port}",
                document,
                {"postThing": {"too_big": pair}},
                "over-cap",
                directory,
                "a-session",
                body_bytes=size,
            )
            proofs = json.loads((Path(directory) / "over-cap-proofs.json").read_text())
        self.assertEqual(code, 0)
        self.assertEqual(proofs, {"postThing": ["too_big"]})
        self.assertEqual(received, [size])
