"""recoverAccount's success is sent on an account made and enrolled for each
request, never the fixture admin it names (#1162)."""

import json
import os
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest.mock import call, patch

from api_keys_live import totp
from test_hooks_two_factor_admin import load_hooks

RECOVER = "/api/v1/auth/recover > A recovery code sets a new password and signs in > 200 > application/json"
# Base32, as the server's enrollment returns it.
SECRET = "JBSWY3DPEHPK3PXP"


class FakeAPI(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])) or b"{}")
        self.server.calls.append((self.path, self.headers.get("Authorization"), body))
        if self.path == "/api/v1/accounts":
            self.server.accounts.append(body)
            data = {"id": len(self.server.accounts), "address": body["address"]}
        elif self.path == "/api/v1/auth/login":
            data = {"access_token": "session-of-" + body["email"]}
        elif self.path == "/api/v1/auth/2fa/enroll":
            number = len(self.server.accounts)
            data = {"secret": SECRET, "recovery_codes": [f"code-{number}-a", f"code-{number}-b"]}
        elif self.path == "/api/v1/auth/2fa/confirm":
            self.send_response(204)
            self.end_headers()
            return
        else:
            self.send_response(404)
            self.end_headers()
            return
        payload = json.dumps({"data": data}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


def recovery(status, authorization=None):
    body = {"email": "admin@restmail.test", "recovery_code": "AAAA-BBBB-CCCC",
            "new_password": "a new password of my own", "client": "native", "totp_code": "123456"}
    headers = {} if authorization is None else {"Authorization": authorization}
    return {"name": RECOVER, "protocol": "http:", "host": "127.0.0.1", "port": "0",
            "fullPath": "/api/v1/auth/recover", "expected": {"statusCode": status},
            "request": {"method": "POST", "headers": headers, "body": json.dumps(body)}}


class RecoverAnAccountOfItsOwn(unittest.TestCase):
    def setUp(self):
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), FakeAPI)
        self.server.calls, self.server.accounts = [], []
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.module, self.hooks, self.registered = load_hooks()
        patch.object(self.module, "mfa_admin_token", return_value="two-factor-token").start()
        patch.object(self.module.time, "sleep").start()
        patch.object(self.module.mfa_admin.time, "sleep").start()
        self.addCleanup(patch.stopall)

    def send(self, status, authorization=None):
        t = recovery(status, authorization)
        t["port"] = str(self.server.server_address[1])
        before = json.loads(t["request"]["body"])
        self.module.recover_an_account_of_its_own(t)
        return before, t

    def test_it_runs_after_the_auth_pacing(self):
        self.assertEqual(self.registered["recoverAccount"],
                         ["pace_auth_requests", "recover_an_account_of_its_own"])

    def test_enrollment_and_recovery_share_the_pacing_policy(self):
        for setting in ("false", "true"):
            with self.subTest(setting=setting), patch.dict(
                os.environ, {"RESTMAIL_CONTRACT_LIMITS_RAISED": setting}
            ), patch.object(self.module.rate_limits.time, "sleep") as sleep:
                self.send("200")
                self.assertEqual(sleep.call_args_list,
                                 [] if setting == "true" else [call(1.1)] * 4)

    def test_each_principals_recovery_is_of_an_account_made_and_enrolled_for_it(self):
        bodies = []
        for authorization in (None, "Bearer admin-principal", "Basic app-password"):
            _, t = self.send("200", authorization)
            # The principal's credential is left as it came: the phase would
            # end a request whose credential a hook changed, unsent.
            self.assertEqual(t["request"]["headers"].get("Authorization"), authorization)
            bodies.append(json.loads(t["request"]["body"]))
        addresses = [body["email"] for body in bodies]
        self.assertEqual(len(set(addresses)), 3)
        self.assertEqual([a["address"] for a in self.server.accounts], addresses)
        for number, (body, account) in enumerate(zip(bodies, self.server.accounts), start=1):
            self.assertTrue(body["email"].startswith("recoverable-"))
            self.assertTrue(body["email"].endswith("@restmail.test"))
            self.assertNotEqual(body["email"], self.module.ADMIN)
            self.assertEqual(account["password"], self.module.RECOVERABLE_PASSWORD)
            self.assertEqual(body["recovery_code"], f"code-{number}-a")
            self.assertEqual(body["totp_code"], totp(SECRET))
            self.assertEqual(body["new_password"], self.module.RECOVERED_PASSWORD)
            self.assertEqual(body["client"], "native")
        # Made as the two-factor admin, then enrolled as itself through the
        # public API: sign in, enroll, confirm with the authenticator's code.
        first = [(path, auth) for path, auth, _ in self.server.calls[:4]]
        self.assertEqual(first, [
            ("/api/v1/accounts", "Bearer two-factor-token"),
            ("/api/v1/auth/login", None),
            ("/api/v1/auth/2fa/enroll", "Bearer session-of-" + addresses[0]),
            ("/api/v1/auth/2fa/confirm", "Bearer session-of-" + addresses[0]),
        ])
        self.assertEqual(self.server.calls[1][2]["password"], self.module.RECOVERABLE_PASSWORD)
        self.assertEqual(self.server.calls[3][2], {"totp_code": totp(SECRET)})

    def test_refusals_keep_their_documented_bodies(self):
        for status in ("401", "422", "429", "500"):
            with self.subTest(status=status):
                before, t = self.send(status)
                self.assertEqual(json.loads(t["request"]["body"]), before)
        self.assertEqual(self.server.calls, [])

    def test_a_body_that_is_not_an_object_is_left_alone(self):
        t = recovery("200")
        t["request"]["body"] = "[]"
        self.module.recover_an_account_of_its_own(t)
        self.assertEqual(t["request"]["body"], "[]")
        self.assertEqual(self.server.calls, [])


if __name__ == "__main__":
    unittest.main()
