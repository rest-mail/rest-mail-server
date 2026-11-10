"""Strict, independent human-assurance proof for the C1 grant/link reports.

This does not reinterpret Vertrag's flat-role matrix or change its verdicts.
Every case uses an actual ordinary caller, a real resource and one exact
status/error code. The password-only session is retained before enrollment;
the second session signs in with an unused recovery code. Grant escalation
also supplies a different, unused code in the request body.
"""

import argparse
import json
import time
from pathlib import Path

import mfa_admin
import rate_limits
from api_keys_live import Client, login, require_fixture_account, totp

PASSWORD = "saffron-glacier-kestrel-violet-73"


def judge(status, payload, expected, code=None):
    actual_code = (payload or {}).get("error", {}).get("code")
    return status == expected and actual_code == code


class Proof:
    def __init__(self, client):
        self.client = client
        self.cells = []

    def expect(self, name, expected, method, path, body=None, token=None, code=None):
        rate_limits.pace(1.1)
        status, payload = self.client.call(method, path, body, token)
        passed = judge(status, payload, expected, code)
        self.cells.append({
            "name": name, "method": method, "path": path,
            "expected": expected, "actual": status,
            "expected_code": code,
            "actual_code": (payload or {}).get("error", {}).get("code"),
            "status": "pass" if passed else "fail",
        })
        if not passed:
            raise AssertionError(f"{name}: wanted {expected}/{code}, got "
                                 f"{status}/{self.cells[-1]['actual_code']}")
        return (payload or {}).get("data")

    def grant_state(self, token, grant_id):
        rows = self.expect("read caller grants", 200, "GET",
                           "/api/v1/admin/grants/mine", token=token)
        grant = next(row for row in rows if row["id"] == grant_id)
        activation = grant.get("activation")
        return grant["mode"], activation["id"] if activation else None

    def unchanged(self, token, grant_id, before):
        if self.grant_state(token, grant_id) != before:
            raise AssertionError("a refused assurance probe changed its grant")


def run(endpoint, output):
    proof = Proof(Client(endpoint))
    output = Path(output)
    output.parent.mkdir(parents=True, exist_ok=True)
    report = {"passed": False, "cells": proof.cells}
    admin = mfa_admin.session(endpoint)
    accounts, grants = [], []
    try:
        nonce = time.time_ns()
        address = f"contract-assurance-{nonce}@restmail.test"
        target_address = f"contract-assurance-target-{nonce}@restmail.test"
        for email in (address, target_address):
            require_fixture_account(email)
            created = proof.expect("create disposable account", 201, "POST",
                                   "/api/v1/accounts",
                                   {"address": email, "password": PASSWORD}, admin)
            accounts.append(created["id"])
        plain = login(proof.client, address, PASSWORD)["access_token"]
        enrolled = proof.expect("enroll ordinary caller", 200, "POST",
                                "/api/v1/auth/2fa/enroll", {}, plain)
        proof.expect("confirm ordinary enrollment", 204, "POST",
                     "/api/v1/auth/2fa/confirm",
                     {"totp_code": totp(enrolled["secret"])}, plain)
        codes = iter(enrolled["recovery_codes"])
        human = login(proof.client, address, PASSWORD,
                      recovery_code=next(codes))["access_token"]
        body = {"address": target_address, "password": PASSWORD}
        proof.expect("link/password-only", 403, "POST", "/api/v1/accounts/link",
                     body, plain, "two_factor_required")
        linked = proof.expect("link/human-2fa", 201, "POST", "/api/v1/accounts/link",
                              body, human)
        if linked["id"] != accounts[1]:
            raise AssertionError("link success named a different account")
        path = f"/api/v1/accounts/{accounts[1]}"
        proof.expect("unlink/password-only", 403, "DELETE", path,
                     token=plain, code="two_factor_required")
        proof.expect("link remains after refusal", 200, "GET", path, token=human)
        proof.expect("unlink/human-2fa", 204, "DELETE", path, token=human)
        proof.expect("link removed", 404, "GET", path, token=human, code="not_found")

        for action, mode, success in (("activate", "eligible", 201),
                                      ("make-eligible", "active", 200)):
            grant = proof.expect("create caller grant", 201, "POST",
                                 "/api/v1/admin/grants", {
                                     "holder": {"kind": "account", "id": accounts[0]},
                                     "role": "admin", "scope": f"domain:assurance-{nonce}-{action}.test",
                                     "mode": mode,
                                 }, admin)
            grant_id = grant["id"]
            grants.append(grant_id)
            path = f"/api/v1/admin/grants/{grant_id}/{action}"
            request = {"reason": "Contract assurance proof", "minutes": 5} if action == "activate" else {}
            before = proof.grant_state(human, grant_id)
            for name, token, factor, code in (
                ("password-only", plain, {}, "two_factor_required"),
                ("human-without-body-factor", human, {}, "totp_required"),
                ("human-invalid-body-factor", human,
                 {"recovery_code": "not-a-real-recovery-code"}, "invalid_totp"),
            ):
                proof.expect(f"{action}/{name}", 403, "POST", path,
                             {**request, **factor}, token, code)
                proof.unchanged(human, grant_id, before)
            proof.expect(f"{action}/human-fresh-body-factor", success, "POST", path,
                         {**request, "recovery_code": next(codes)}, human)
            after = proof.grant_state(human, grant_id)
            if action == "activate":
                if after[0] != "eligible" or after[1] is None:
                    raise AssertionError("successful activation did not switch on the caller's grant")
                path = f"/api/v1/admin/grants/{grant_id}/deactivate"
                proof.expect("deactivate/password-only", 403, "POST", path,
                             token=plain, code="two_factor_required")
                proof.unchanged(human, grant_id, after)
                proof.expect("deactivate/human-2fa", 200, "POST", path, token=human)
                if proof.grant_state(human, grant_id) != ("eligible", None):
                    raise AssertionError("successful deactivation left the grant switched on")
            elif after != ("eligible", None):
                raise AssertionError("successful make-eligible did not change the caller's grant")
        report["passed"] = True
    finally:
        try:
            for grant_id in reversed(grants):
                proof.expect("remove disposable grant", 204, "DELETE",
                             f"/api/v1/admin/grants/{grant_id}", token=admin)
            for account_id in reversed(accounts):
                proof.expect("remove disposable account", 204, "DELETE",
                             f"/api/v1/admin/mailboxes/{account_id}", token=admin)
        except Exception:
            report["passed"] = False
            raise
        finally:
            output.write_text(json.dumps(report, indent=2) + "\n")
    print(f"Strict assurance: {len(proof.cells)} exact exchanges passed; no matrix verdict rewritten")
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("endpoint")
    parser.add_argument("output")
    args = parser.parse_args()
    return run(args.endpoint, args.output)


if __name__ == "__main__":
    raise SystemExit(main())
