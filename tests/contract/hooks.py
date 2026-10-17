"""vertrag hooks for the contract tests (#376).

Only what the spec cannot hold: the fixture's passwords, which no spec example
should carry, and the refresh token a sign-in hands out. Every identifier the
run needs comes from the spec's own links instead.

The principals are the E2E fixture's (tests/e2e/contract_principals_test.go),
read from the file it writes.
"""
import base64
import json
import os

import vertrag_hooks as hooks

ADMIN = "admin@restmail.test"
ADMIN_PASSWORD = "admin123!@"

PRINCIPALS = os.environ.get("CONTRACT_PRINCIPALS", ".cache/contract-principals.json")


def principals():
    with open(PRINCIPALS) as f:
        return json.load(f)


def basic(address, password):
    token = base64.b64encode(("%s:%s" % (address, password)).encode()).decode()
    return "Basic " + token


@hooks.before("login")
def sign_in_as_the_fixture_admin(transaction):
    hooks.set_json(transaction, {"email": ADMIN, "password": ADMIN_PASSWORD, "client": "native"})


@hooks.after("login")
def keep_the_refresh_token(transaction):
    data = hooks.get_json(transaction, "real").get("data") or {}
    if data.get("refresh_token"):
        hooks.stash["refresh_token"] = data["refresh_token"]


@hooks.before("refreshToken")
def refresh_the_sign_in(transaction):
    hooks.set_json(transaction, {"refresh_token": hooks.stash.get("refresh_token", ""), "client": "native"})


@hooks.after("refreshToken")
def keep_the_refreshed_token(transaction):
    data = hooks.get_json(transaction, "real").get("data") or {}
    if data.get("refresh_token"):
        hooks.stash["refresh_token"] = data["refresh_token"]


@hooks.before("logout")
def sign_out_the_refreshed_session(transaction):
    hooks.set_json(transaction, {"refresh_token": hooks.stash.get("refresh_token", "")})


@hooks.before("createAppPassword")
def confirm_with_the_admin_password(transaction):
    body = hooks.get_json(transaction)
    body["password"] = ADMIN_PASSWORD
    body.pop("expires_at", None)
    hooks.set_json(transaction, body)


@hooks.before("linkAccount")
def link_the_ordinary_account(transaction):
    ordinary = principals()["ordinary"]
    hooks.set_json(transaction, {"address": ordinary["address"], "password": ordinary["password"]})


@hooks.before("RestMail API > /api/v1/auth/protocol-sign-in > * > 200 > application/json")
def sign_in_a_mail_client(transaction):
    app = principals()["app_password"]
    transaction["request"]["headers"]["Authorization"] = basic(app["address"], app["secret"])
    hooks.set_json(transaction, {"protocol": "imap"})
