import base64
import json
import unittest

from test_hooks import hooks

LINKED = "/api/v1/accounts/link > Link an existing account by proving its password > {} > application/json"


def session(**claims):
    payload = base64.urlsafe_b64encode(json.dumps(claims).encode()).decode().rstrip("=")
    return "Bearer header." + payload + ".signature"


def sent(authorization, status=201):
    transaction = {
        "name": LINKED.format(status),
        "request": {
            "headers": {"Authorization": authorization} if authorization else {},
            "body": json.dumps({"address": "eve@restmail.test", "password": "x"}),
        },
    }
    hooks.no_link_from_a_password_only_session(transaction)
    return transaction.get("skip", False)


class APasswordOnlySessionLinking(unittest.TestCase):
    """The authorization phase expected 201 from its ordinary principal, a
    password-only session, which the description refuses 403
    two_factor_required (#1942)."""

    def test_its_success_cell_is_skipped(self):
        self.assertTrue(sent(session(address="ordinary@restmail.test", amr=["pwd"])))
        self.assertTrue(sent(session(address="ordinary@restmail.test")))

    def test_a_two_factor_session_is_sent(self):
        self.assertFalse(sent(session(address="mfa@restmail.test", amr=["pwd", "otp"], mfa=True)))
        self.assertFalse(sent(session(address="mfa@restmail.test", amr=["pwd", "otp"])))

    def test_refusals_and_other_credentials_are_sent(self):
        password_only = session(address="ordinary@restmail.test", amr=["pwd"])
        for status in (401, 403, 409, 422):
            self.assertFalse(sent(password_only, status))
        self.assertFalse(sent(None))
        self.assertFalse(sent("Basic " + base64.b64encode(b"ordinary@restmail.test:rm_x_y").decode()))


if __name__ == "__main__":
    unittest.main()
