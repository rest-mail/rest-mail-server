import json
import unittest
from pathlib import Path
from unittest import mock

import assurance_live


class StrictJudgement(unittest.TestCase):
    def test_only_the_exact_refusal_and_error_code_pass(self):
        for status, code, passed in (
            (403, "two_factor_required", True),
            (403, "forbidden", False),
            (403, "totp_required", False),
            (401, "two_factor_required", False),
            (404, "two_factor_required", False),
            (200, "two_factor_required", False),
            (500, "two_factor_required", False),
            (403, None, False),
        ):
            with self.subTest(status=status, code=code):
                payload = {"error": {"code": code}} if code else {}
                self.assertEqual(assurance_live.judge(status, payload, 403,
                                                     "two_factor_required"), passed)

    def test_success_does_not_accept_a_refusal_or_an_error_body(self):
        self.assertTrue(assurance_live.judge(201, {"data": {"id": 1}}, 201))
        self.assertFalse(assurance_live.judge(200, {"data": {"id": 1}}, 201))
        self.assertFalse(assurance_live.judge(403, {"error": {"code": "two_factor_required"}}, 201))
        self.assertFalse(assurance_live.judge(201, {"error": {"code": "forbidden"}}, 201))


class RequiredLiveProof(unittest.TestCase):
    def test_the_live_gate_runs_assurance_through_its_failure_propagating_runner(self):
        script = (Path(__file__).with_name("live_proofs.sh")).read_text()
        command = 'run_live assurance python3 -B tests/contract/assurance_live.py "$endpoint" "$out/assurance/report.json"'
        self.assertIn(command, script)
        self.assertLess(script.index(command), script.index('exit "$status"'))
        self.assertIn('if [ "$code" -ne 0 ]; then status=1; fi', script)


class ExactExchange(unittest.TestCase):
    def setUp(self):
        self.client = mock.Mock()
        self.proof = assurance_live.Proof(self.client)
        patch = mock.patch.object(assurance_live.rate_limits, "pace")
        patch.start()
        self.addCleanup(patch.stop)

    def test_supplies_the_original_credential_and_records_no_secrets(self):
        self.client.call.return_value = (403, {"error": {"code": "two_factor_required"}})
        body = {"password": "fixture-password", "recovery_code": "unused-code"}
        self.proof.expect("link/password-only", 403, "POST", "/api/v1/accounts/link",
                          body, "actual-plain-token", "two_factor_required")
        self.client.call.assert_called_once_with("POST", "/api/v1/accounts/link", body, "actual-plain-token")
        report = json.dumps(self.proof.cells)
        for secret in (*body.values(), "actual-plain-token"):
            self.assertNotIn(secret, report)
        self.assertEqual(self.proof.cells[0]["status"], "pass")

    def test_an_unexpected_success_is_a_failed_cell_not_a_widened_expectation(self):
        self.client.call.return_value = (201, {"data": {"token": "secret"}})
        with self.assertRaisesRegex(AssertionError, "wanted 403/two_factor_required") as error:
            self.proof.expect("link/password-only", 403, "POST", "/api/v1/accounts/link",
                              token="plain", code="two_factor_required")
        self.assertNotIn("secret", str(error.exception))
        self.assertEqual(self.proof.cells[0]["status"], "fail")

    def test_refusal_cannot_mutate_the_callers_grant(self):
        self.client.call.return_value = (200, {"data": [{"id": 17, "mode": "eligible", "activation": {"id": 9}}]})
        with self.assertRaisesRegex(AssertionError, "changed its grant"):
            self.proof.unchanged("human", 17, ("eligible", None))

    def test_counts_down_changing_time_fields_do_not_mask_state_identity(self):
        self.client.call.return_value = (200, {"data": [{"id": 17, "mode": "eligible", "activation": {"id": 9, "seconds_left": 100}}]})
        self.proof.unchanged("human", 17, ("eligible", 9))


if __name__ == "__main__":
    unittest.main()
