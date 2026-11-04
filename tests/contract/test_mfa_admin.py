import io
import json
import sys
import tempfile
import unittest
import urllib.error
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import mfa_admin  # noqa: E402


def refusal(code):
    body = json.dumps({"error": {"code": code, "message": "refused"}}).encode()
    return urllib.error.HTTPError("http://api/api/v1/auth/login", 401, "Unauthorized", {}, io.BytesIO(body))


class EnrollTest(unittest.TestCase):
    def setUp(self):
        self.state = Path(tempfile.mkdtemp()) / "mfa-admin.json"
        self.calls = []
        sleep = mock.patch.object(mfa_admin.time, "sleep")
        sleep.start()
        self.addCleanup(sleep.stop)

    def server(self, enrolled):
        def post(base_url, path, body, token=None):
            self.calls.append(path)
            if path == "/api/v1/auth/login":
                if enrolled:
                    raise refusal("totp_required")
                return {"access_token": "plain"}
            if path == "/api/v1/auth/2fa/enroll":
                return {"secret": "JBSWY3DPEHPK3PXP", "recovery_codes": ["new-code"]}
            return None

        return mock.patch.object(mfa_admin, "_post", side_effect=post)

    def kept(self):
        self.state.write_text(json.dumps({"secret": "OLD", "recovery_codes": ["old-code"], "step": 1}))

    def test_a_kept_enrollment_the_server_still_holds_is_kept(self):
        self.kept()
        with self.server(enrolled=True):
            mfa_admin._enroll("http://api", "a@restmail.test", "pw", self.state)
        self.assertEqual(self.calls, ["/api/v1/auth/login"])
        self.assertEqual(json.loads(self.state.read_text())["secret"], "OLD")

    def test_a_kept_enrollment_the_server_lost_is_made_again(self):
        # A database restored to before the enrollment: the account signs in
        # with its password alone, so the kept codes prove nothing.
        self.kept()
        with self.server(enrolled=False):
            mfa_admin._enroll("http://api", "a@restmail.test", "pw", self.state)
        self.assertEqual(
            self.calls, ["/api/v1/auth/login", "/api/v1/auth/2fa/enroll", "/api/v1/auth/2fa/confirm"]
        )
        state = json.loads(self.state.read_text())
        self.assertEqual((state["secret"], state["recovery_codes"]), ("JBSWY3DPEHPK3PXP", ["new-code"]))

    def test_a_first_enrollment_is_made(self):
        with self.server(enrolled=False):
            mfa_admin._enroll("http://api", "a@restmail.test", "pw", self.state)
        self.assertEqual(json.loads(self.state.read_text())["recovery_codes"], ["new-code"])

    def test_an_enrolled_account_with_no_kept_codes_is_an_error(self):
        # Nothing kept can sign it in, so saying it is enrolled would be wrong.
        with self.server(enrolled=True), self.assertRaises(urllib.error.HTTPError):
            mfa_admin._enroll("http://api", "a@restmail.test", "pw", self.state)
        self.assertFalse(self.state.exists())

    def test_another_refusal_is_an_error(self):
        self.kept()
        with mock.patch.object(mfa_admin, "_post", side_effect=refusal("unauthorized")):
            with self.assertRaises(urllib.error.HTTPError):
                mfa_admin._enroll("http://api", "a@restmail.test", "pw", self.state)


if __name__ == "__main__":
    unittest.main()
