#!/usr/bin/env python3
"""The contract run's two-factor administrator, for setup a password cannot do.

Deleting an account always takes a session signed in with 2FA, and the primary
fixture admin has none. chore test:contract-mfa-admin makes this administrator
(tests/contract/fixtures --mfa-admin) and enrolls it in TOTP here, through the
public API; the hooks and repeat_fixtures.py then sign it in with a real second
factor. No session is forged and no server guard is relaxed.

The enrollment is kept in a disposable cache file, shared by the processes of
one run. A sign-in uses an unused recovery code while one is left, then a TOTP
code for a step not used before (the server refuses a replayed code).

Usage: mfa_admin.py enroll BASE_URL
"""

import json
import os
import sys
import time
import urllib.request
from pathlib import Path

ADDRESS = "contract-mfa-admin@restmail.test"
# A disposable test fixture password (tests/contract/fixtures prepareMFAAdmin).
PASSWORD = "contract-mfa-admin123!@"
STATE = Path(os.environ.get("CONTRACT_MFA_ADMIN", ".cache/contract/mfa-admin.json"))

sys.path.insert(0, str(Path(__file__).resolve().parent))
from api_keys_live import totp  # noqa: E402


def _post(base_url, path, body, token=None):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    request = urllib.request.Request(
        base_url.rstrip("/") + path, data=json.dumps(body).encode(), headers=headers, method="POST"
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        raw = response.read()
    return json.loads(raw).get("data") if raw else None


def _sign_in(base_url, **factor):
    # Sign-ins share the 1 request/second auth bucket.
    time.sleep(1.1)
    body = {"email": ADDRESS, "password": PASSWORD, "client": "native", **factor}
    return _post(base_url, "/api/v1/auth/login", body)


def _step():
    return int(time.time()) // 30


def enroll(base_url):
    """Enroll the administrator in TOTP and keep what signing in needs. The
    E2E suite and the contract run both prepare it; the first one enrolls."""
    if STATE.exists():
        return
    plain = _sign_in(base_url)["access_token"]
    enrolled = _post(base_url, "/api/v1/auth/2fa/enroll", {}, plain)
    _post(base_url, "/api/v1/auth/2fa/confirm", {"totp_code": totp(enrolled["secret"])}, plain)
    STATE.parent.mkdir(parents=True, exist_ok=True)
    STATE.write_text(json.dumps({
        "secret": enrolled["secret"],
        "recovery_codes": enrolled["recovery_codes"],
        "step": _step(),
    }))


def session(base_url):
    """A fresh access token signed in with a second factor."""
    return _sign_in(base_url, **factor())["access_token"]


def sign_in_body():
    """The body of a sign-in with an unused second factor, for a reader that
    makes its own request (secret_scan.py)."""
    return {"email": ADDRESS, "password": PASSWORD, "client": "native", **factor()}


def factor():
    """An unused second factor: a recovery code while one is left, then a TOTP
    code for a step not used before. What it spends is kept."""
    if not STATE.exists():
        raise RuntimeError("the contract MFA administrator is not enrolled (chore test:contract-mfa-admin)")
    state = json.loads(STATE.read_text())
    if state["recovery_codes"]:
        chosen = {"recovery_code": state["recovery_codes"].pop(0)}
    else:
        while _step() <= state["step"]:
            time.sleep(1)
        state["step"] = _step()
        chosen = {"totp_code": totp(state["secret"])}
    STATE.write_text(json.dumps(state))
    return chosen


if __name__ == "__main__":
    if len(sys.argv) != 3 or sys.argv[1] != "enroll":
        sys.exit(__doc__)
    enroll(sys.argv[2])
    print("contract MFA administrator enrolled")
