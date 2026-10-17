"""vertrag hooks for the contract tests (#376).

Only what the spec cannot hold: the fixture's passwords, which no spec example
should carry; the refresh token a sign-in hands out; a fresh key pair; and the
id of the domain the run created, where a request carries it in its body
rather than its path (links fill only parameters). Every other identifier the
run needs comes from the spec's own links.

The principals are the E2E fixture's (tests/e2e/contract_principals_test.go),
read from the file it writes.
"""
import base64
import json
import os
import subprocess
import tempfile
import time
import urllib.request

import vertrag_hooks as hooks

ADMIN = "admin@restmail.test"
ADMIN_PASSWORD = "admin123!@"
# A seeded account that nobody owns, to link (cmd/seed's fixture password).
LINKABLE = "eve@restmail.test"
LINKABLE_PASSWORD = "password123"

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


@hooks.before("createAppPassword")
def confirm_with_the_admin_password(transaction):
    body = hooks.get_json(transaction)
    body["password"] = ADMIN_PASSWORD
    body.pop("expires_at", None)
    hooks.set_json(transaction, body)


@hooks.before("linkAccount")
def link_a_seeded_account(transaction):
    hooks.set_json(transaction, {"address": LINKABLE, "password": LINKABLE_PASSWORD})


@hooks.after("createDomain")
def keep_the_created_domain(transaction):
    data = hooks.get_json(transaction, "real").get("data") or {}
    hooks.stash["domain_id"] = data.get("id")
    hooks.stash["domain"] = data.get("name")


def on_the_created_domain(transaction, **fields):
    body = hooks.get_json(transaction)
    body["domain_id"] = hooks.stash.get("domain_id")
    body.update(fields)
    hooks.set_json(transaction, body)


@hooks.before("createPipeline")
def a_pipeline_on_the_created_domain(transaction):
    on_the_created_domain(transaction, direction="inbound", active=False,
                          filters=[{"name": "header_validate", "config": {}}])


@hooks.after("createPipeline")
def keep_the_created_pipeline(transaction):
    hooks.stash["pipeline_id"] = (hooks.get_json(transaction, "real").get("data") or {}).get("id")


@hooks.before("testPipeline")
def test_the_created_pipeline(transaction):
    hooks.set_json(transaction, {"pipeline_id": hooks.stash.get("pipeline_id"), "email": {}})


@hooks.before("createCustomFilter")
def a_custom_filter_on_the_created_domain(transaction):
    on_the_created_domain(transaction)


def openssl(*args):
    subprocess.run(["openssl"] + list(args), check=True, capture_output=True)


@hooks.before("setDKIMKey")
def a_fresh_signing_key(transaction):
    with tempfile.TemporaryDirectory() as d:
        key = os.path.join(d, "dkim.key")
        openssl("genrsa", "-out", key, "2048")
        hooks.set_json(transaction, {"selector": "contract", "private_key": open(key).read()})


@hooks.before("createCertificate")
def a_self_signed_certificate(transaction):
    name = hooks.stash.get("domain") or "example.org"
    with tempfile.TemporaryDirectory() as d:
        key, cert = os.path.join(d, "tls.key"), os.path.join(d, "tls.crt")
        openssl("req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "30",
                "-subj", "/CN=" + name, "-addext", "subjectAltName=DNS:" + name,
                "-keyout", key, "-out", cert)
        on_the_created_domain(transaction, cert_pem=open(cert).read(), key_pem=open(key).read(),
                              auto_renew=False)


@hooks.before("mtaStsPolicyFile")
def ask_for_the_created_domains_policy(transaction):
    # Served at mta-sts.<domain>: the domain is the Host's, not a parameter.
    transaction["request"]["headers"]["Host"] = "mta-sts." + (hooks.stash.get("domain") or "")


@hooks.before("listQueue")
def list_the_sent_messages_queue_entry(transaction):
    # The example asks for status=pending (the enum's first value), and the
    # queue worker takes the entry sendMessage queued within a second: the
    # list came back empty, and its links had no id. Ask by the example's
    # recipient instead, whatever the entry's status. Only the example: the
    # probes keep their own queries.
    if transaction.get("fullPath", "").endswith("/api/v1/admin/queue?status=pending"):
        transaction["fullPath"] = "/api/v1/admin/queue?recipient=someone%40imap.restmail.test"


@hooks.before("getMessageTrace")
def wait_for_the_trace(transaction):
    # Traces are written in the background, a batch every 500 ms
    # (internal/trace/recorder.go); the testbed keeps every one
    # (TRACE_SAMPLE_RATE=1 in tasks/api.yml).
    time.sleep(1)


@hooks.before("renameFolder")
def give_each_rename_probe_a_folder(transaction):
    # A successful rename consumes its source folder. Each boundary probe
    # needs its own source, or a later valid request sees the earlier one's
    # 404. Keep the probe's body and account ID exactly as generated.
    if not transaction.get("name", "").startswith("coverage: "):
        return
    path = transaction["fullPath"].split("?")[0]
    parent, _, _ = path.rpartition("/")
    account_id = parent.split("/")[-2]
    if not account_id.isdecimal() or int(account_id) < 1:
        return
    serial = hooks.stash.get("rename_probe", 0) + 1
    hooks.stash["rename_probe"] = serial
    folder = "contract-rename-%d" % serial
    origin = "%s//%s:%s" % (transaction["protocol"], transaction["host"], transaction["port"])
    request = urllib.request.Request(
        origin + parent,
        data=json.dumps({"name": folder}).encode(),
        headers={"Content-Type": "application/json", "Authorization": basic(ADMIN, ADMIN_PASSWORD)},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        if response.status != 201:
            raise RuntimeError("could not create a folder for the rename probe")
    transaction["fullPath"] = parent + "/" + folder


AUTODISCOVER_REQUEST = """<?xml version="1.0" encoding="utf-8"?>
<Autodiscover xmlns="http://schemas.microsoft.com/exchange/autodiscover/outlook/requestschema/2006">
  <Request>
    <EMailAddress>eve@restmail.test</EMailAddress>
    <AcceptableResponseSchema>http://schemas.microsoft.com/exchange/autodiscover/outlook/responseschema/2006a</AcceptableResponseSchema>
  </Request>
</Autodiscover>
"""


@hooks.before("microsoftAutodiscover")
def an_outlook_autodiscover_request(transaction):
    # The spec's XML example, which vertrag 0.27 does not send (it sends
    # JSON request examples only).
    transaction["request"]["body"] = AUTODISCOVER_REQUEST


@hooks.before("/api/v1/auth/protocol-sign-in > * > 200 > application/json")
def sign_in_a_mail_client(transaction):
    app = principals()["app_password"]
    transaction["request"]["headers"]["Authorization"] = basic(app["address"], app["secret"])
    hooks.set_json(transaction, {"protocol": "imap"})
