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
import urllib.error
import urllib.parse
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
    # The refused create (name_missing, 422) runs straight after the one that
    # made the domain, and created nothing to keep.
    if not data.get("id"):
        return
    hooks.stash["domain_id"] = data.get("id")
    hooks.stash["domain"] = data.get("name")


def on_the_created_domain(transaction, **fields):
    body = hooks.get_json(transaction)
    body["domain_id"] = hooks.stash.get("domain_id")
    body.update(fields)
    hooks.set_json(transaction, body)


@hooks.before("createPipeline")
def a_pipeline_on_the_created_domain(transaction):
    body = hooks.get_json(transaction)
    # The undecodable create (body_array, 400) is sent as documented: an
    # array, with no object to put the domain in.
    if not isinstance(body, dict):
        return
    fields = {"active": False, "filters": [{"name": "header_validate", "config": {}}]}
    # The refused create (direction_missing, 422) is sent as documented, with
    # no direction.
    if "direction" in body:
        fields["direction"] = "inbound"
    on_the_created_domain(transaction, **fields)


@hooks.after("createPipeline")
def keep_the_created_pipeline(transaction):
    data = hooks.get_json(transaction, "real").get("data") or {}
    # The refused create runs straight after the one that made the pipeline,
    # and created nothing to keep.
    if not data.get("id"):
        return
    hooks.stash["pipeline_id"] = data.get("id")


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
    # queue worker can already have delivered sendMessage's entry when retry
    # runs. The disposable topology prepares a separate deferred entry, held
    # until tomorrow, for the linked queue examples. Only the pending example
    # is redirected; invalid boundary queries and caller credentials stay intact.
    if transaction.get("fullPath", "").endswith("/api/v1/admin/queue?status=pending"):
        transaction["fullPath"] = "/api/v1/admin/queue?recipient=contract-retry%40retry.invalid"


@hooks.before("getMessageTrace")
def wait_for_the_trace(transaction):
    # Traces are written in the background, a batch every 500 ms
    # (internal/trace/recorder.go); the testbed keeps every one
    # (TRACE_SAMPLE_RATE=1 in tasks/api.yml).
    time.sleep(1)


def created_folder_path(transaction):
    body = hooks.get_json(transaction)
    name = body.get("name") if isinstance(body, dict) else None
    if not isinstance(name, str) or not name:
        return None
    return transaction["fullPath"].split("?")[0] + "/" + urllib.parse.quote(name, safe="")


@hooks.before("createFolder")
def remove_a_previous_folder_probe(transaction):
    # Valid probes repeat the example's name. Only reuse destinations that
    # were absent before this operation tried them, leaving requests untouched:
    # rewriting its body would make vertrag skip the boundary being tested.
    path = created_folder_path(transaction)
    parent = transaction["fullPath"].split("?")[0]
    account_id = parent.split("/")[-2]
    if not path or not account_id.isdecimal() or int(account_id) < 1:
        return
    created = hooks.stash.get("created_folders", {})
    origin = "%s//%s:%s" % (transaction["protocol"], transaction["host"], transaction["port"])
    headers = {"Authorization": basic(ADMIN, ADMIN_PASSWORD)}
    if path not in created:
        # Coverage runs before hooks only. Remember an absent destination
        # before sending it, rather than relying on an after hook to run.
        request = urllib.request.Request(origin + parent, headers=headers)
        try:
            with urllib.request.urlopen(request, timeout=10) as response:
                existing = json.load(response).get("data") or []
        except urllib.error.HTTPError as error:
            if error.code in (400, 403, 404):
                return  # Leave an invalid account ID for the real probe.
            raise
        name = hooks.get_json(transaction)["name"]
        if name in ("INBOX", "Sent", "Drafts", "Trash", "Spam", "Archive"):
            return
        if any(folder.get("name") == name for folder in existing):
            return
        created[path] = True
        hooks.stash["created_folders"] = created
        return
    request = urllib.request.Request(
        origin + path,
        headers=headers,
        method="DELETE",
    )
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            if response.status != 204:
                raise RuntimeError("could not remove the previous folder probe")
    except urllib.error.HTTPError as error:
        if error.code != 404:
            raise


@hooks.before("renameFolder")
def give_each_rename_probe_a_folder(transaction):
    # A successful rename consumes its source folder. Each boundary probe
    # needs its own source, or a later valid request sees the earlier one's
    # 404. Hooks see the operation's original name in every phase, so prepare
    # the example too. Keep the body and account ID exactly as generated.
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


@hooks.before("createContact")
def remove_a_previous_contact_probe(transaction):
    # Coverage calls before hooks only. Claim an absent email before its first
    # create, then remove only that probe's contact when generated bodies repeat.
    # Existing contacts and all generated request bytes stay untouched.
    from urllib.error import HTTPError
    from urllib.parse import urlencode

    body = hooks.get_json(transaction)
    email = body.get("email") if isinstance(body, dict) else None
    parent = transaction["fullPath"].split("?")[0]
    account_id = parent.split("/")[-2]
    if not isinstance(email, str) or not email or not account_id.isdecimal() or int(account_id) < 1:
        return
    origin = "%s//%s:%s" % (transaction["protocol"], transaction["host"], transaction["port"])
    headers = {"Authorization": basic(ADMIN, ADMIN_PASSWORD)}
    key = origin + parent + "\n" + email
    owned = hooks.stash.get("created_contact_probes", {})
    offset = 0
    while True:
        query = urlencode({"search": email, "limit": 200, "offset": offset})
        request = urllib.request.Request(origin + parent + "?" + query, headers=headers)
        try:
            with urllib.request.urlopen(request, timeout=10) as response:
                page = json.load(response)
        except HTTPError as error:
            if error.code in (400, 403, 404):
                return  # Leave an invalid account ID to the actual probe.
            raise
        contacts = page.get("data") or []
        contact = next((item for item in contacts if item.get("email") == email), None)
        if contact:
            if key not in owned:
                return
            request = urllib.request.Request(
                origin + parent + "/" + str(contact["id"]), headers=headers, method="DELETE")
            with urllib.request.urlopen(request, timeout=10) as response:
                if response.status != 204:
                    raise RuntimeError("could not remove the previous contact probe")
            return
        if not (page.get("pagination") or {}).get("has_more"):
            owned[key] = True
            hooks.stash["created_contact_probes"] = owned
            return
        offset += len(contacts)
