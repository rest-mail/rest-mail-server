"""A live proof requires a real, successful, uniquely selected exchange."""

import copy
import importlib.util
import json
import sys
import tempfile
import types
import unittest
from pathlib import Path
from unittest.mock import patch

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
