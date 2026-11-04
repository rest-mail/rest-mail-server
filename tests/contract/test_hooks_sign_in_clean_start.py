"""The sign-in and recovery operations start clean in the own-data run (#1385):
each exchange resets first, refusals keep their requests, and a success that
ends a session, rotates one or starts a second factor acts on an account made
for it after the reset, never on a session or credential the run uses."""

import json
import os
import unittest
from unittest.mock import Mock, patch

from test_hooks_pipeline import load_hooks

OPERATIONS = {
    "login": ("/api/v1/auth/login", "POST"),
    "logout": ("/api/v1/auth/logout", "POST"),
    "refreshToken": ("/api/v1/auth/refresh", "POST"),
    "protocolSignIn": ("/api/v1/auth/protocol-sign-in", "POST"),
    "resetAdmin": ("/api/v1/auth/reset", "POST"),
    "recoverAccount": ("/api/v1/auth/recover", "POST"),
    "ownerChallenge": ("/api/v1/auth/owner/challenge", "POST"),
    "ownerToken": ("/api/v1/auth/owner/token", "POST"),
    "ownerStatus": ("/api/v1/auth/owner/status", "POST"),
    "getTwoFactorStatus": ("/api/v1/auth/2fa", "GET"),
    "enrollTwoFactor": ("/api/v1/auth/2fa/enroll", "POST"),
    "confirmTwoFactor": ("/api/v1/auth/2fa/confirm", "POST"),
    "disableTwoFactor": ("/api/v1/auth/2fa/disable", "POST"),
    "changeOwnPassword": ("/api/v1/auth/password", "POST"),
    "requestRecoveryEmail": ("/api/v1/auth/recover/email", "POST"),
    "redeemRecoveryEmail": ("/api/v1/auth/recover/email/redeem", "POST"),
    "getRecoveryEmail": ("/api/v1/auth/recovery-email", "GET"),
    "registerRecoveryEmail": ("/api/v1/auth/recovery-email", "POST"),
    "recoveryEmailLink": ("/api/v1/auth/recovery-email/link", "GET"),
    "verifyRecoveryEmail": ("/api/v1/auth/recovery-email/verify", "POST"),
}

# The successes that act on an account made for them; every other exchange is
# sent as it was.
MADE_FOR = {
    ("/api/v1/auth/logout", "204"),
    ("/api/v1/auth/refresh", "200"),
    ("/api/v1/auth/2fa/enroll", "200"),
    ("/api/v1/auth/protocol-sign-in", "200"),
    ("/api/v1/auth/protocol-sign-in", "400"),
    ("/api/v1/auth/protocol-sign-in", "422"),
}

SESSION = {"access_token": "made-access", "refresh_token": "made-refresh"}


def transaction(path, name, body=None):
    return {
        "protocol": "http:", "host": "api", "port": "8080",
        "fullPath": path, "name": name,
        "request": {"body": json.dumps(body), "headers": {"Authorization": "Bearer run-session"}},
    }


def exchange(path, method, status, body=None):
    suffix = "" if status == "204" else " > application/json"
    return transaction(path, f"{path} > x > {status}{suffix}", body)


class CleanStart(unittest.TestCase):
    def setUp(self):
        self.hooks = load_hooks()
        self.hook = self.hooks.sign_in_and_recovery_after_a_reset
        self.globals = self.hook.__globals__
        self.patch = patch.dict(self.globals)
        names = self.patch.start()
        self.addCleanup(self.patch.stop)
        self.start_clean = Mock()
        names["start_clean"] = self.start_clean
        self.session = Mock(return_value=dict(SESSION))
        names["a_session_of_its_own"] = self.session
        self.app_password = Mock(return_value=("made@restmail.test", "made-app-password"))
        names["an_app_password_of_its_own"] = self.app_password
        names["urllib"] = Mock(request=Mock(urlopen=Mock(side_effect=AssertionError("no request may be made"))))
        self.stash = names["hooks"].stash
        self.stash.clear()

    def clean(self, on):
        env = patch.dict("os.environ")
        env.start()
        self.addCleanup(env.stop)
        os.environ.pop("CONTRACT_RESET_SEED", None)
        if on:
            os.environ["CONTRACT_RESET_SEED"] = "1"

    def test_each_exchange_resets_first(self):
        self.clean(True)
        for operation, (path, method) in OPERATIONS.items():
            for status in ("200", "204", "401", "409", "422"):
                with self.subTest(operation=operation, status=status):
                    self.start_clean.reset_mock()
                    sent = exchange(path, method, status, {"refresh_token": "contract-invalid-token"})
                    self.hook(sent)
                    self.start_clean.assert_called_once_with(sent)

    def test_refusals_and_reads_keep_their_requests(self):
        self.clean(True)
        for operation, (path, method) in OPERATIONS.items():
            for status in ("200", "204", "401", "403", "409", "422"):
                if (path, status) in MADE_FOR:
                    continue
                with self.subTest(operation=operation, status=status):
                    sent = exchange(path, method, status, {"refresh_token": "fixture-refresh-token"})
                    before = json.dumps(sent, sort_keys=True)
                    self.hook(sent)
                    self.assertEqual(json.dumps(sent, sort_keys=True), before)
        self.session.assert_not_called()
        self.app_password.assert_not_called()

    def test_it_is_each_operations_first_hook(self):
        for operation in OPERATIONS:
            with self.subTest(operation=operation):
                self.assertIs(self.hooks.registered_before[operation][0].__name__, self.hook.__name__)

    def test_nothing_happens_without_a_reset(self):
        self.clean(False)
        for operation, (path, method) in OPERATIONS.items():
            for status in ("200", "204", "422"):
                sent = exchange(path, method, status, {"refresh_token": "fixture-refresh-token"})
                before = json.dumps(sent, sort_keys=True)
                self.hook(sent)
                self.assertEqual(json.dumps(sent, sort_keys=True), before)
        self.start_clean.assert_not_called()
        self.session.assert_not_called()
        self.app_password.assert_not_called()

    def test_the_sign_out_ends_a_session_made_for_it(self):
        self.clean(True)
        sent = exchange("/api/v1/auth/logout", "POST", "204", {"refresh_token": "contract-invalid-token"})
        self.hook(sent)
        self.assertEqual(json.loads(sent["request"]["body"]), {"refresh_token": "made-refresh"})
        # The run's own session is not what it ends.
        self.assertEqual(sent["request"]["headers"]["Authorization"], "Bearer run-session")
        self.assertEqual(self.stash["ended_refresh_token"], {"name": sent["name"], "token": "made-refresh"})

    def test_the_refresh_rotates_a_session_made_for_it(self):
        self.clean(True)
        sent = exchange("/api/v1/auth/refresh", "POST", "200", {"refresh_token": "fixture-refresh-token"})
        self.hook(sent)
        self.assertEqual(json.loads(sent["request"]["body"]), {"refresh_token": "made-refresh"})
        # refresh_the_sign_in, which runs next, leaves a token that is not the
        # placeholder alone and adds no cookie to a native body.
        self.hooks.refresh_the_sign_in(sent)
        self.assertEqual(json.loads(sent["request"]["body"]), {"refresh_token": "made-refresh"})
        self.assertNotIn("Cookie", sent["request"]["headers"])

    def test_the_enrollment_is_started_for_an_account_made_for_it(self):
        self.clean(True)
        sent = exchange("/api/v1/auth/2fa/enroll", "POST", "200", {})
        self.hook(sent)
        self.assertEqual(sent["request"]["headers"]["Authorization"], "Bearer made-access")

    def test_the_mail_client_signs_in_with_an_app_password_made_for_it(self):
        self.clean(True)
        expected = self.globals["basic"]("made@restmail.test", "made-app-password")
        for status in ("200", "400", "422"):
            with self.subTest(status=status):
                sent = exchange("/api/v1/auth/protocol-sign-in", "POST", status, {"protocol": "imap"})
                self.hook(sent)
                self.assertEqual(sent["request"]["headers"]["Authorization"], expected)
                # The principals' hook, which runs next, leaves it after a reset.
                self.hooks.sign_in_a_mail_client(sent)
                self.assertEqual(sent["request"]["headers"]["Authorization"], expected)


class TheSessionItEndedIsOver(unittest.TestCase):
    def setUp(self):
        self.hooks = load_hooks()
        self.after = self.hooks.the_session_it_ended_is_over
        self.globals = self.after.__globals__
        self.patch = patch.dict(self.globals)
        names = self.patch.start()
        self.addCleanup(self.patch.stop)
        names["pace_auth_requests"] = Mock()
        self.urlopen = Mock()
        names["urllib"] = Mock(request=Mock(urlopen=self.urlopen, Request=Mock()), error=Mock(HTTPError=StubHTTPError))
        self.stash = names["hooks"].stash
        self.stash.clear()
        env = patch.dict("os.environ", {"CONTRACT_RESET_SEED": "1"})
        env.start()
        self.addCleanup(env.stop)
        self.name = "/api/v1/auth/logout > x > 204"

    def answered(self, status):
        sent = transaction("/api/v1/auth/logout", self.name)
        sent["real"] = {"statusCode": status}
        return sent

    def refresh_answers(self, status):
        if status >= 400:
            self.urlopen.side_effect = StubHTTPError(status)
        else:
            self.urlopen.return_value.__enter__ = Mock(return_value=Mock(status=status))
            self.urlopen.return_value.__exit__ = Mock(return_value=False)

    def test_a_refused_refresh_passes(self):
        self.stash["ended_refresh_token"] = {"name": self.name, "token": "made-refresh"}
        self.refresh_answers(401)
        sent = self.answered(204)
        self.after(sent)
        self.assertNotIn("fail", sent)
        self.assertNotIn("ended_refresh_token", self.stash)

    def test_a_session_still_alive_fails(self):
        self.stash["ended_refresh_token"] = {"name": self.name, "token": "made-refresh"}
        self.refresh_answers(200)
        sent = self.answered(204)
        self.after(sent)
        self.assertIn("was not ended", sent["fail"])

    def test_nothing_made_fails(self):
        sent = self.answered(204)
        self.after(sent)
        self.assertEqual(sent["fail"], "the sign-out had no session made for it to end")
        self.urlopen.assert_not_called()

    def test_refusals_and_runs_without_a_reset_are_left_alone(self):
        sent = transaction("/api/v1/auth/logout", "/api/v1/auth/logout > x > 422 > application/json")
        sent["real"] = {"statusCode": 422}
        self.after(sent)
        self.assertNotIn("fail", sent)
        os.environ.pop("CONTRACT_RESET_SEED")
        sent = self.answered(204)
        self.after(sent)
        self.assertNotIn("fail", sent)
        self.urlopen.assert_not_called()


class StubHTTPError(Exception):
    def __init__(self, code):
        super().__init__(code)
        self.code = code


if __name__ == "__main__":
    unittest.main()
