import base64
import hashlib
import unittest

import api_keys_live


class TOTPTest(unittest.TestCase):
    # RFC 6238 appendix B, SHA-1 seed, last six digits.
    SECRET = base64.b32encode(b"12345678901234567890").decode().rstrip("=")

    def test_rfc6238_vectors(self):
        for at, want in [(59, "287082"), (1111111109, "081804"), (1234567890, "005924"), (20000000000, "353130")]:
            self.assertEqual(api_keys_live.totp(self.SECRET, at=at), want)

    def test_unpadded_lowercase_secret(self):
        self.assertEqual(api_keys_live.totp(self.SECRET.lower(), at=59), "287082")

    def test_eight_digits(self):
        self.assertEqual(api_keys_live.totp(self.SECRET, at=59, digits=8, algorithm=hashlib.sha1), "94287082")


class GuardTest(unittest.TestCase):
    def test_refuses_other_domains(self):
        for email in ["admin@example.com", "x@restmail.test.evil", "restmail.test@evil.com"]:
            with self.assertRaises(SystemExit):
                api_keys_live.require_fixture_account(email)

    def test_accepts_fixture_domain(self):
        api_keys_live.require_fixture_account("keys@RestMail.test")

    def test_metadata_without_secret(self):
        api_keys_live.assert_no_secret({"id": 1, "name": "k", "permissions": []})
        for field in ("token", "password", "secret"):
            with self.assertRaises(AssertionError):
                api_keys_live.assert_no_secret({"id": 1, field: "rmk_x"})


if __name__ == "__main__":
    unittest.main()
