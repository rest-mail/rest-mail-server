"""The report's one refuse list stays within what the owner decided on #672."""

import contextlib
import io
import json
import tempfile
import unittest
from pathlib import Path

import authorization_refusals
import yaml

SPEC = {
    "paths": {
        "/api/v1/accounts/{id}": {
            "get": {"responses": {"200": {}, "404": {}}},
        },
        "/api/v1/accounts/{id}/contacts": {
            "get": {"responses": {"200": {}, "404": {}}},
        },
        "/api/v1/accounts/{id}/quota": {
            "get": {"responses": {"200": {}, "403": {}}},
        },
        "/api/v1/accounts": {
            "post": {"responses": {"201": {}, "403": {}, "404": {}}},
        },
        "/api/v1/admin/domains/{id}": {
            "get": {"responses": {"200": {}, "403": {}, "404": {}}},
        },
    }
}


def cell(operation, principal, expected, actual, verdict="pass"):
    return {
        "operation": operation,
        "principal": principal,
        "expected": expected,
        "actual": actual,
        "verdict": verdict,
    }


class Findings(unittest.TestCase):
    def findings(self, *cells):
        report = {"authorization": {"cells": list(cells)}}
        return authorization_refusals.findings(SPEC, report)

    def test_someone_elses_account_may_answer_404_for_a_signed_in_caller(self):
        self.assertEqual(
            self.findings(
                cell("GET /api/v1/accounts/{id}", "ordinary", "403", "404"),
                cell("GET /api/v1/accounts/{id}", "app-password", "403", "404"),
            ),
            [],
        )

    def test_a_query_template_does_not_hide_the_operation(self):
        self.assertEqual(
            self.findings(
                cell(
                    "GET /api/v1/accounts/{id}/contacts{?trust_level*,limit*}",
                    "ordinary",
                    "403",
                    "404",
                )
            ),
            [],
        )

    def test_an_admin_route_without_the_role_stays_403(self):
        found = self.findings(
            cell("GET /api/v1/admin/domains/{id}", "ordinary", "403", "404")
        )
        self.assertEqual(len(found), 1)
        self.assertIn("GET /api/v1/admin/domains/{id} as ordinary", found[0])
        self.assertIn("403", found[0])

    def test_no_credential_stays_401(self):
        found = self.findings(cell("GET /api/v1/accounts/{id}", "none", "401", "404"))
        self.assertEqual(len(found), 1)
        self.assertIn("GET /api/v1/accounts/{id} as none", found[0])
        self.assertIn("401", found[0])

    def test_a_route_naming_no_object_has_nothing_to_hide(self):
        self.assertEqual(
            len(self.findings(cell("POST /api/v1/accounts", "ordinary", "403", "404"))),
            1,
        )

    def test_a_route_that_does_not_document_404_may_not_answer_it(self):
        self.assertEqual(
            len(
                self.findings(
                    cell("GET /api/v1/accounts/{id}/quota", "ordinary", "403", "404")
                )
            ),
            1,
        )

    def test_a_route_the_description_does_not_know_is_a_finding(self):
        self.assertEqual(
            len(self.findings(cell("GET /api/v1/gone/{id}", "ordinary", "403", "404"))),
            1,
        )

    def test_only_a_404_standing_in_for_a_refusal_is_judged(self):
        self.assertEqual(
            self.findings(
                cell("GET /api/v1/admin/domains/{id}", "ordinary", "403", "403"),
                cell("GET /api/v1/accounts/{id}", "none", "401", "401"),
                # A wanted success answered 404 is already Vertrag's failure.
                cell("GET /api/v1/admin/domains/{id}", "admin", "200", "404", "fail"),
                # An unsent cell has no answer to judge.
                cell(
                    "GET /api/v1/admin/domains/{id}", "ordinary", "403", None, "unsent"
                ),
            ),
            [],
        )


class Command(unittest.TestCase):
    def run_command(self, report):
        with tempfile.TemporaryDirectory() as directory:
            spec_path = Path(directory, "openapi.json")
            spec_path.write_text(yaml.safe_dump(SPEC))
            report_path = Path(directory, "authorization.json")
            report_path.write_text(json.dumps(report))
            out = io.StringIO()
            with contextlib.redirect_stdout(out):
                code = authorization_refusals.main(str(spec_path), str(report_path))
        return code, out.getvalue()

    def test_a_finding_fails_and_is_named(self):
        code, out = self.run_command(
            {
                "authorization": {
                    "cells": [
                        cell("GET /api/v1/admin/domains/{id}", "ordinary", "403", "404")
                    ]
                }
            }
        )
        self.assertEqual(code, 1)
        self.assertIn("GET /api/v1/admin/domains/{id} as ordinary", out)

    def test_a_clean_report_passes(self):
        code, _ = self.run_command(
            {
                "authorization": {
                    "cells": [
                        cell("GET /api/v1/accounts/{id}", "ordinary", "403", "404")
                    ]
                }
            }
        )
        self.assertEqual(code, 0)

    def test_a_run_that_sent_no_cell_has_nothing_to_judge(self):
        code, _ = self.run_command({"results": []})
        self.assertEqual(code, 0)


class Wiring(unittest.TestCase):
    def test_the_authorization_run_applies_the_check_to_its_own_report(self):
        root = Path(__file__).resolve().parents[2]
        script = (root / "tests/contract/run.sh").read_text()
        run = script.index(
            '"$CONTRACT_VERTRAG" run --config "$out/vertrag-authorization.yml"'
        )
        check = script.index("tests/contract/authorization_refusals.py")
        report = script.index('echo "vertrag authorization exited')
        self.assertLess(run, check)
        self.assertLess(check, report)
        self.assertIn(
            '"$CONTRACT_SPEC" "$out/authorization.json"', script[check:report]
        )


if __name__ == "__main__":
    unittest.main()
