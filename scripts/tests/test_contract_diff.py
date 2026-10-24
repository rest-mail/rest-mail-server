"""The real OpenAPI 3.1 comparator and REST Mail's deliberate-change gates (#653)."""

import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
BINARY = os.environ.get("OASDIFF")


def description():
    return {
        "openapi": "3.1.0",
        "info": {"title": "Contract gate fixture", "version": "1.0.0"},
        "components": {
            "securitySchemes": {
                "CookieAuth": {"type": "apiKey", "in": "cookie", "name": "session"}
            }
        },
        "paths": {
            "/widgets": {
                "post": {
                    "operationId": "writeWidget",
                    "security": [{"CookieAuth": ["widgets:write"]}],
                    "requestBody": {
                        "required": True,
                        "content": {
                            "application/json": {
                                "schema": {
                                    "type": "object",
                                    "additionalProperties": False,
                                    "properties": {
                                        "name": {"type": ["string", "null"]},
                                        "kind": {"type": "string", "const": "widget"},
                                    },
                                    "required": ["kind"],
                                }
                            }
                        },
                    },
                    "responses": {
                        "200": {
                            "description": "A widget",
                            "content": {
                                "application/json": {
                                    "schema": {
                                        "type": "object",
                                        "properties": {
                                            "id": {"type": "integer"},
                                            "label": {"type": "string"},
                                            "state": {
                                                "type": "string",
                                                "enum": ["ready", "waiting"],
                                            },
                                        },
                                        "required": ["id", "state"],
                                    }
                                }
                            },
                        }
                    },
                }
            }
        },
    }


@unittest.skipUnless(
    BINARY, "OASDIFF names the pinned binary; contract-diff CI installs it"
)
class ContractDiff(unittest.TestCase):
    def run_diff(self, revision, labels=(), base=None):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name, value in (
                ("base", base or description()),
                ("revision", revision),
                (
                    "event",
                    {"pull_request": {"labels": [{"name": label} for label in labels]}},
                ),
            ):
                (root / (name + ".json")).write_text(json.dumps(value))
            result = subprocess.run(
                [
                    sys.executable,
                    str(ROOT / "scripts/contract_diff.py"),
                    str(root / "base.json"),
                    str(root / "revision.json"),
                    "--oasdiff",
                    BINARY,
                    "--event",
                    str(root / "event.json"),
                    "--output",
                    str(root / "report"),
                ],
                capture_output=True,
                text=True,
                check=False,
                env=dict(os.environ, GITHUB_STEP_SUMMARY=str(root / "summary.md")),
                timeout=20,
            )
            path = root / "report/changes.json"
            report = json.loads(path.read_text()) if path.exists() else None
            summary = (root / "summary.md").read_text()
            return result, report, summary

    def test_unchanged_openapi31_nullable_and_const_are_not_breaking(self):
        result, report, summary = self.run_diff(description())
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(report["changes"], [])
        self.assertIn("No consumer-visible contract change", summary)

    def test_new_operation_is_additive_without_a_label(self):
        revision = description()
        revision["paths"]["/widgets"]["get"] = {
            "operationId": "listWidgets",
            "responses": {"204": {"description": "No content"}},
        }
        result, report, _ = self.run_diff(revision)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(
            any(c["classification"] == "additive" for c in report["changes"])
        )

    def test_optional_response_field_removal_needs_contract_label(self):
        revision = description()
        del revision["paths"]["/widgets"]["post"]["responses"]["200"]["content"][
            "application/json"
        ]["schema"]["properties"]["label"]
        result, report, summary = self.run_diff(revision)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(report["missing_labels"], ["contract-change"])
        self.assertIn("writeWidget", summary)
        self.assertEqual(self.run_diff(revision, ["contract-change"])[0].returncode, 0)

    def test_response_enum_removal_and_request_type_widening_need_contract_label(self):
        for kind in ("enum", "type"):
            revision = description()
            operation = revision["paths"]["/widgets"]["post"]
            if kind == "enum":
                operation["responses"]["200"]["content"]["application/json"]["schema"][
                    "properties"
                ]["state"]["enum"] = ["ready"]
            else:
                operation["requestBody"]["content"]["application/json"]["schema"][
                    "properties"
                ]["name"] = {"type": ["string", "number", "null"]}
            with self.subTest(kind=kind):
                result, report, _ = self.run_diff(revision)
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn("contract-change", report["missing_labels"])

    def test_new_required_request_field_needs_contract_label(self):
        revision = description()
        revision["paths"]["/widgets"]["post"]["requestBody"]["content"][
            "application/json"
        ]["schema"]["required"].append("name")
        result, report, _ = self.run_diff(revision)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(report["missing_labels"], ["contract-change"])

    def test_operation_removal_needs_contract_label(self):
        revision = description()
        del revision["paths"]["/widgets"]["post"]
        result, report, _ = self.run_diff(revision)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(report["missing_labels"], ["contract-change"])

    def test_credential_location_change_needs_permission_label(self):
        revision = description()
        revision["components"]["securitySchemes"]["CookieAuth"]["in"] = "header"
        result, report, _ = self.run_diff(revision, ["contract-change"])
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(report["missing_labels"], ["permission-change"])

    def test_permission_loosening_and_anonymous_access_need_permission_label(self):
        for security in ([{"CookieAuth": []}], []):
            revision = description()
            revision["paths"]["/widgets"]["post"]["security"] = security
            with self.subTest(security=security):
                result, report, _ = self.run_diff(revision, ["contract-change"])
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertEqual(report["missing_labels"], ["permission-change"])
                self.assertEqual(
                    self.run_diff(revision, ["permission-change"])[0].returncode, 0
                )

    def test_security_and_response_break_together_need_both_labels(self):
        revision = description()
        operation = revision["paths"]["/widgets"]["post"]
        operation["security"] = []
        del operation["responses"]["200"]["content"]["application/json"]["schema"][
            "properties"
        ]["id"]
        operation["responses"]["200"]["content"]["application/json"]["schema"][
            "required"
        ].remove("id")
        result, report, _ = self.run_diff(revision)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(
            report["missing_labels"], ["contract-change", "permission-change"]
        )
        self.assertEqual(
            self.run_diff(revision, report["missing_labels"])[0].returncode, 0
        )

    def test_external_refs_fail_the_gate_instead_of_fetching_them(self):
        revision = description()
        revision["paths"]["/widgets"]["post"]["requestBody"]["content"][
            "application/json"
        ]["schema"] = {"$ref": "http://127.0.0.1:1/forbidden.json"}
        result, report, summary = self.run_diff(revision)
        self.assertEqual(result.returncode, 2)
        self.assertIsNone(report)
        self.assertIn("comparison failed", summary)


if __name__ == "__main__":
    unittest.main()
