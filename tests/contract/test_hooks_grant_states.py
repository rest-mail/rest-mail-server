"""The grant and link examples that need a state of the caller's (#1162):
eligible_grant, active_grant, existing_link and grant_holder. Each is prepared
on a throwaway grant, link or account made just before the request, never on
the run's own accounts, and the request's credential is left as it came."""

import base64
import json
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest.mock import patch

from test_hooks_two_factor_admin import load_hooks

MFA_ADMIN = "contract-mfa-admin@restmail.test"
MFA_ADMIN_ID = 41


def session(address, account_id):
    claims = base64.urlsafe_b64encode(
        json.dumps({"address": address, "account_id": account_id}).encode()
    ).decode().rstrip("=")
    return f"Bearer header.{claims}.signature"


ADMIN_PRINCIPAL = session(MFA_ADMIN, MFA_ADMIN_ID)
ORDINARY = session("user@restmail.test", 3)


class FakeAPI(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def answer(self, status, data=None):
        payload = b"" if data is None else json.dumps({"data": data}).encode()
        self.send_response(status)
        if payload:
            self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])) or b"{}")
        self.server.calls.append(("POST", self.path, self.headers.get("Authorization"), body))
        if self.path == "/api/v1/accounts":
            self.server.accounts.append(body)
            return self.answer(201, {"id": 100 + len(self.server.accounts)})
        if self.path == "/api/v1/admin/grants":
            self.server.grants.append(body)
            return self.answer(201, {"id": 200 + len(self.server.grants)})
        if self.path.startswith("/api/v1/admin/grants/") and self.path.endswith("/activate"):
            return self.answer(201, {"id": int(self.path.split("/")[-2])})
        if self.path == "/api/v1/accounts/link":
            account = next(
                (number for number, made in enumerate(self.server.accounts, start=101)
                 if made["address"] == body["address"]), None)
            return self.answer(201, {"id": account, "address": body["address"]})
        self.answer(404)

    def do_DELETE(self):
        self.server.calls.append(("DELETE", self.path, self.headers.get("Authorization"), None))
        self.answer(204)


def grant_request(action, summary, status, body, authorization=ADMIN_PRINCIPAL):
    path = f"/api/v1/admin/grants/7/{action}"
    headers = {} if authorization is None else {"Authorization": authorization}
    return {
        "name": f"/api/v1/admin/grants/{{id}}/{action} > {summary} > {status} > application/json",
        "protocol": "http:", "host": "127.0.0.1", "port": "0", "fullPath": path,
        "expected": {"statusCode": str(status)},
        "request": {"method": "POST", "headers": headers, "body": json.dumps(body)},
    }


class HookTest(unittest.TestCase):
    def setUp(self):
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), FakeAPI)
        self.server.calls, self.server.accounts, self.server.grants = [], [], []
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.module, self.hooks, self.registered = load_hooks()
        patch.object(self.module, "mfa_admin_token", return_value="two-factor-token").start()
        patch.object(self.module.time, "sleep").start()
        self.factors = iter({"recovery_code": f"unused-{n}"} for n in range(100))
        patch.object(self.module.mfa_admin, "factor", side_effect=lambda: next(self.factors)).start()
        self.addCleanup(patch.stopall)

    def at_the_fake(self, t):
        t["port"] = str(self.server.server_address[1])
        return t


class AGrantOfTheCallersOwn(HookTest):
    def send(self, action, summary, status, body, authorization=ADMIN_PRINCIPAL):
        t = self.at_the_fake(grant_request(action, summary, status, body, authorization))
        self.module.a_grant_of_the_callers_own(t)
        return t

    def test_it_runs_after_the_admin_swap_and_the_missing_grants_factor(self):
        for operation in ("activateGrant", "makeGrantEligible"):
            hooks = self.registered[operation]
            self.assertLess(hooks.index("send_as_the_two_factor_admin"), hooks.index("a_grant_of_the_callers_own"))
            self.assertLess(hooks.index("a_fresh_second_factor_for_a_missing_grant"),
                            hooks.index("a_grant_of_the_callers_own"))
        self.assertIn("a_grant_of_the_callers_own", self.registered["deactivateGrant"])

    def test_each_example_gets_a_grant_of_the_administrators_in_its_state(self):
        cases = [
            ("activate", "An eligible grant is switched on for an hour", 201,
             {"reason": "Rotate the DKIM key", "minutes": 60, "totp_code": "123456"}, "eligible", False, True),
            ("activate", "The grant is on already", 409,
             {"reason": "Rotate the DKIM key", "totp_code": "123456"}, "eligible", True, True),
            ("activate", "The second factor does not match", 403,
             {"reason": "Rotate the DKIM key", "totp_code": "000000"}, "eligible", False, False),
            ("deactivate", "The running activation ends", 200, {}, "eligible", True, None),
            ("deactivate", "Nothing to switch off", 409, {}, "eligible", False, None),
            ("make-eligible", "An active grant becomes eligible", 200, {"totp_code": "123456"}, "active", False, True),
            ("make-eligible", "The second factor does not match", 403, {"totp_code": "000000"}, "active", False, False),
            ("make-eligible", "The grant is eligible already", 409, {"totp_code": "123456"}, "eligible", False, True),
        ]
        for action, summary, status, body, mode, switched_on, fresh in cases:
            with self.subTest(action=action, summary=summary):
                self.server.calls.clear()
                t = self.send(action, summary, status, body)
                made = 200 + len(self.server.grants)
                grant = self.server.grants[-1]
                # Held by the caller, the two-factor administrator, at a domain
                # no one hosts, made with the hooks' own two-factor session.
                self.assertEqual(grant["holder"], {"kind": "account", "id": MFA_ADMIN_ID})
                self.assertEqual(grant["role"], "admin")
                self.assertRegex(grant["scope"], r"^domain:grant-\d+\.test$")
                self.assertEqual(grant["mode"], mode)
                self.assertEqual(self.server.calls[0][2], "Bearer two-factor-token")
                activated = [c for c in self.server.calls if c[1].endswith("/activate")]
                self.assertEqual(len(activated), 1 if switched_on else 0)
                if switched_on:
                    self.assertEqual(activated[0][1], f"/api/v1/admin/grants/{made}/activate")
                    self.assertIn("recovery_code", activated[0][3])
                self.assertEqual(t["fullPath"], f"/api/v1/admin/grants/{made}/{action}")
                self.assertEqual(t["request"]["headers"]["Authorization"], ADMIN_PRINCIPAL)
                sent = json.loads(t["request"]["body"])
                if fresh is True:
                    self.assertNotIn("totp_code", sent)
                    self.assertTrue(sent["recovery_code"].startswith("unused-"))
                elif fresh is False:
                    self.assertEqual(sent["totp_code"], "000000")
                else:
                    self.assertEqual(sent, body)
                self.assertEqual(self.hooks.stash["throwaway_grants"][t["name"]], made)

    def test_the_grant_is_removed_after_the_request(self):
        t = self.send("deactivate", "Nothing to switch off", 409, {})
        made = 200 + len(self.server.grants)
        self.server.calls.clear()
        self.module.remove_the_throwaway_grant(t)
        self.assertEqual(self.server.calls,
                         [("DELETE", f"/api/v1/admin/grants/{made}", "Bearer two-factor-token", None)])
        self.assertNotIn(t["name"], self.hooks.stash["throwaway_grants"])
        # Nothing made, nothing removed.
        self.server.calls.clear()
        self.module.remove_the_throwaway_grant(t)
        self.assertEqual(self.server.calls, [])

    def test_other_principals_and_other_examples_are_sent_as_they_are(self):
        for authorization in (None, ORDINARY, "Basic app-password"):
            t = self.send("activate", "An eligible grant is switched on for an hour", 201,
                          {"reason": "r", "totp_code": "123456"}, authorization)
            self.assertEqual(t["fullPath"], "/api/v1/admin/grants/7/activate")
        t = self.send("activate", "No such grant of the caller's", 404, {"reason": "r"})
        self.assertEqual(t["fullPath"], "/api/v1/admin/grants/7/activate")
        t = self.at_the_fake(grant_request("deactivate", "The running activation ends", 200, {}))
        t["fullPath"] = "/api/v1/admin/grants/2147483647/deactivate"
        self.module.a_grant_of_the_callers_own(t)
        self.assertEqual(t["fullPath"], "/api/v1/admin/grants/2147483647/deactivate")
        self.assertEqual(self.server.calls, [])


class ExistingLink(HookTest):
    def test_unlinked_gives_up_a_link_made_for_it(self):
        t = self.at_the_fake({
            "name": "/api/v1/accounts/{id} > The link is given up > 204",
            "protocol": "http:", "host": "127.0.0.1", "port": "0",
            "fullPath": "/api/v1/accounts/2", "expected": {"statusCode": "204"},
            "request": {"method": "DELETE", "headers": {"Authorization": ADMIN_PRINCIPAL}, "body": ""},
        })
        self.module.unlink_a_link_of_its_own(t)
        account = self.server.accounts[-1]
        self.assertRegex(account["address"], r"^linked-\d+@restmail\.test$")
        self.assertEqual(account["password"], self.module.LINKED_PASSWORD)
        link = [c for c in self.server.calls if c[1] == "/api/v1/accounts/link"]
        self.assertEqual(link, [("POST", "/api/v1/accounts/link", "Bearer two-factor-token",
                                 {"address": account["address"], "password": self.module.LINKED_PASSWORD})])
        self.assertEqual(t["fullPath"], f"/api/v1/accounts/{100 + len(self.server.accounts)}")
        self.assertEqual(t["request"]["headers"]["Authorization"], ADMIN_PRINCIPAL)

    def test_already_linked_names_an_account_linked_just_before(self):
        body = {"address": "eve@restmail.test", "password": "password123"}
        t = self.at_the_fake({
            "name": "/api/v1/accounts/link > The account is linked already > 409 > application/json",
            "protocol": "http:", "host": "127.0.0.1", "port": "0",
            "fullPath": "/api/v1/accounts/link", "expected": {"statusCode": "409"},
            "request": {"method": "POST", "headers": {"Authorization": ADMIN_PRINCIPAL},
                        "body": json.dumps(body)},
        })
        self.module.link_an_account_linked_already(t)
        sent = json.loads(t["request"]["body"])
        self.assertEqual(sent["address"], self.server.accounts[-1]["address"])
        self.assertEqual(sent["password"], self.module.LINKED_PASSWORD)
        self.assertEqual(self.server.calls[-1][1], "/api/v1/accounts/link")
        hooks = self.registered["linkAccount"]
        self.assertLess(hooks.index("send_as_the_two_factor_admin"), hooks.index("link_an_account_linked_already"))
        self.assertLess(hooks.index("link_an_account_linked_already"), hooks.index("link_a_seeded_account"))

    def test_other_principals_keep_the_documented_request(self):
        for authorization in (ORDINARY, "Basic app-password"):
            t = self.at_the_fake({
                "name": "/api/v1/accounts/{id} > The link is given up > 204",
                "protocol": "http:", "host": "127.0.0.1", "port": "0",
                "fullPath": "/api/v1/accounts/2", "expected": {"statusCode": "204"},
                "request": {"method": "DELETE", "headers": {"Authorization": authorization}, "body": ""},
            })
            self.module.unlink_a_link_of_its_own(t)
            self.assertEqual(t["fullPath"], "/api/v1/accounts/2")
        self.assertEqual(self.server.calls, [])


class GrantHolder(HookTest):
    def grant(self, summary, status, authorization):
        body = {"holder": {"kind": "account", "id": 2}, "role": "admin",
                "scope": "domain:example.com", "mode": "eligible"}
        return self.at_the_fake({
            "name": f"/api/v1/admin/grants > {summary} > {status} > application/json",
            "protocol": "http:", "host": "127.0.0.1", "port": "0",
            "fullPath": "/api/v1/admin/grants", "expected": {"statusCode": str(status)},
            "request": {"method": "POST", "headers": {"Authorization": authorization},
                        "body": json.dumps(body)},
        })

    def test_every_principals_grant_names_an_account_made_for_it(self):
        holders = []
        for authorization in (ADMIN_PRINCIPAL, ORDINARY, "Basic app-password"):
            t = self.grant("An eligible admin grant on one domain", 201, authorization)
            self.module.grant_to_an_account_of_its_own(t)
            sent = json.loads(t["request"]["body"])
            holders.append(sent["holder"]["id"])
            self.assertEqual(t["request"]["headers"]["Authorization"], authorization)
            self.assertEqual((sent["role"], sent["scope"], sent["mode"]),
                             ("admin", "domain:example.com", "eligible"))
        self.assertEqual(holders, [101, 102, 103])
        for account in self.server.accounts:
            self.assertRegex(account["address"], r"^grant-holder-\d+@restmail\.test$")
            self.assertNotIn("password", account)
        self.assertIn("grant_to_an_account_of_its_own", self.registered["createGrant"])

    def test_refusals_keep_the_documented_holder(self):
        t = self.grant("The holder does not exist", 422, ADMIN_PRINCIPAL)
        before = t["request"]["body"]
        self.module.grant_to_an_account_of_its_own(t)
        self.assertEqual(t["request"]["body"], before)
        self.assertEqual(self.server.calls, [])


if __name__ == "__main__":
    unittest.main()
