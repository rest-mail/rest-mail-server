#!/usr/bin/env python3
"""Live checks for the scoped API key endpoints, signed in with real 2FA.

Creating and revoking an API key needs a session that signed in with
two-factor authentication, and the primary contract run's fixture admin has
none, so these three operations are proved here instead:

    GET    /api/v1/accounts/{id}/api-keys
    POST   /api/v1/accounts/{id}/api-keys
    DELETE /api/v1/accounts/{id}/api-keys/{credential_id}

The account must be a disposable one on restmail.test (the fixture helper's
--api-keys mode makes it). This script enrolls it in 2FA through the public
API, works out a valid TOTP code with the standard library, confirms the
enrollment, then signs in with one of the recovery codes (a second TOTP code
in the same step would be refused as a replay) to get a session that really
carries the second factor. No token is forged and no server guard is relaxed.

Usage: api_keys_live.py BASE_URL EMAIL PASSWORD DOMAIN_ID
       api_keys_live.py BASE_URL --fixture FIXTURE_JSON
"""

import base64
import hashlib
import hmac
import json
import struct
import sys
import time
import urllib.error
import urllib.request

FIXTURE_DOMAIN = "restmail.test"
API_KEY_PREFIX = "rmk_"


def totp(secret, at=None, step=30, digits=6, algorithm=hashlib.sha1):
    """RFC 6238 code for a base32 secret, as an authenticator app shows it."""
    key = base64.b32decode(secret.upper() + "=" * (-len(secret) % 8))
    counter = int((time.time() if at is None else at) // step)
    digest = hmac.new(key, struct.pack(">Q", counter), algorithm).digest()
    offset = digest[-1] & 0x0F
    code = struct.unpack(">I", digest[offset:offset + 4])[0] & 0x7FFFFFFF
    return str(code % 10 ** digits).zfill(digits)


def require_fixture_account(email):
    """Refuse to touch anything that is not a disposable fixture account."""
    if email.rpartition("@")[2].lower() != FIXTURE_DOMAIN:
        raise SystemExit(f"refusing {email!r}: only {FIXTURE_DOMAIN} accounts are used")


def assert_no_secret(key):
    """A listed or revoked key never carries its secret again."""
    for field in ("token", "password", "secret"):
        if field in key:
            raise AssertionError(f"API key metadata exposes {field!r}: {key!r}")


class Client:
    def __init__(self, base_url):
        self.base_url = base_url.rstrip("/")

    def call(self, method, path, body=None, token=None):
        headers = {"Accept": "application/json"}
        data = None
        if body is not None:
            data = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        if token:
            headers["Authorization"] = f"Bearer {token}"
        req = urllib.request.Request(self.base_url + path, data=data, method=method, headers=headers)
        for attempt in range(3):
            try:
                with urllib.request.urlopen(req, timeout=30) as resp:
                    status, raw = resp.status, resp.read()
            except urllib.error.HTTPError as err:
                status, raw = err.code, err.read()
                if status == 429 and attempt < 2:
                    delay = int(err.headers.get("Retry-After", "1"))
                    if not 0 < delay <= 60:
                        raise AssertionError(f"unexpected throttle delay {delay}")
                    time.sleep(delay + 1)
                    continue
            return status, (json.loads(raw) if raw else None)

    def expect(self, want, method, path, body=None, token=None, code=None):
        status, payload = self.call(method, path, body, token)
        if status != want:
            raise AssertionError(f"{method} {path}: want {want}, got {status}: {payload!r}")
        if code is not None:
            got = (payload or {}).get("error", {}).get("code")
            if got != code:
                raise AssertionError(f"{method} {path}: want error {code!r}, got {payload!r}")
        return payload


def login(client, email, password, **factor):
    body = {"email": email, "password": password, "client": "native", **factor}
    return client.expect(200, "POST", "/api/v1/auth/login", body)["data"]


def mfa_session(client, email, password):
    """Enroll the fixture account in 2FA and sign in with a recovery code."""
    plain = login(client, email, password)
    token = plain["access_token"]
    enrolled = client.expect(200, "POST", "/api/v1/auth/2fa/enroll", {}, token)["data"]
    client.expect(204, "POST", "/api/v1/auth/2fa/confirm", {"totp_code": totp(enrolled["secret"])}, token)
    signed_in = login(client, email, password, recovery_code=enrolled["recovery_codes"][0])
    return signed_in, token


def run(base_url, email, password, domain_id):
    require_fixture_account(email)
    client = Client(base_url)
    session, plain_token = mfa_session(client, email, password)
    token = session["access_token"]
    account = session["user"]["id"]
    keys = f"/api/v1/accounts/{account}/api-keys"
    scope = f"domain:{FIXTURE_DOMAIN}"
    domain_path = f"/api/v1/admin/domains/{int(domain_id)}"
    request = {
        "name": "contract live key",
        "password": password,
        "permissions": [{"capability": "domains:read", "scope": scope}],
    }

    # A session without the second factor cannot create or revoke.
    client.expect(403, "POST", keys, request, plain_token, code="two_factor_required")
    # Creating re-checks the current password.
    client.expect(403, "POST", keys, {**request, "password": password + "-wrong"}, token, code="invalid_password")

    for permissions in ([], [{"capability": "*", "scope": "*"}]):
        client.expect(422, "POST", keys, {**request, "permissions": permissions}, token,
                      code="validation_failed")
    client.expect(403, "POST", keys, {**request, "permissions": [
        {"capability": "domains:read", "scope": "*"}]}, token, code="forbidden")

    created = client.expect(201, "POST", keys, request, token)["data"]
    secret = created["token"]
    if not secret.startswith(API_KEY_PREFIX):
        raise AssertionError(f"created key secret lacks {API_KEY_PREFIX!r}")
    listed = client.expect(200, "GET", keys, token=token)["data"]
    match = [k for k in listed if k["id"] == created["id"]]
    if len(match) != 1:
        raise AssertionError(f"created key {created['id']} not listed once: {listed!r}")
    assert_no_secret(match[0])

    # The raw key works inside its scope and cannot manage credentials.
    client.expect(200, "GET", domain_path, token=secret)
    client.expect(403, "GET", "/api/v1/admin/domains", token=secret)
    client.expect(403, "PATCH", domain_path, {"active": False}, secret)
    # These routes accept JWT sessions only, so a raw key is not a session.
    client.expect(401, "GET", keys, token=secret)
    client.expect(401, "POST", keys, request, secret)

    client.expect(403, "DELETE", f"{keys}/{created['id']}", token=plain_token, code="two_factor_required")
    client.expect(204, "DELETE", f"{keys}/{created['id']}", token=token)
    client.expect(404, "DELETE", f"{keys}/{created['id']}", token=token, code="not_found")
    client.expect(401, "GET", domain_path, token=secret)


def main(argv):
    if len(argv) == 4 and argv[2] == "--fixture":
        with open(argv[3]) as source:
            fixture = json.load(source)
        run(argv[1], fixture["email"], "contract-api-key123!@", fixture["domain_id"])
        print("api key live checks passed")
        return
    if len(argv) != 5:
        raise SystemExit(__doc__)
    run(*argv[1:])
    print("api key live checks passed")


if __name__ == "__main__":
    main(sys.argv)
