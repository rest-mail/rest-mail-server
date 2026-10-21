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


def admin_token(origin):
    """Sign in for fixture setup without changing the transaction's principal.

    Basic auth accepts app passwords, not the admin's account password. Cache
    the setup session per origin so repeated probes do not exhaust login limits.
    """
    tokens = hooks.stash.get("setup_admin_tokens", {})
    if origin not in tokens:
        request = urllib.request.Request(
            origin + "/api/v1/auth/login",
            data=json.dumps({"email": ADMIN, "password": ADMIN_PASSWORD, "client": "native"}).encode(),
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        with urllib.request.urlopen(request, timeout=10) as response:
            token = (json.load(response).get("data") or {}).get("access_token")
        if not isinstance(token, str) or not token:
            raise RuntimeError("fixture admin sign-in returned no access token")
        tokens[origin] = token
        hooks.stash["setup_admin_tokens"] = tokens
    return tokens[origin]


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
    fields = {"active": False}
    # The refused creates (direction_missing and filters_missing, 422) are
    # sent as documented, each without the field it is named for.
    if "direction" in body:
        fields["direction"] = "inbound"
    if "filters" in body:
        fields["filters"] = [{"name": "header_validate", "config": {}}]
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
    body = hooks.get_json(transaction)
    # The refused requests (invalid_body, 400, and email_missing, 422) are
    # sent as documented: each is refused before any pipeline is looked up.
    if not isinstance(body, dict) or "email" not in body:
        return
    body["pipeline_id"] = hooks.stash.get("pipeline_id")
    hooks.set_json(transaction, body)


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
    body = hooks.get_json(transaction)
    # Leave malformed and incomplete requests intact: those are the paired
    # 400 examples, and coverage probes must keep their missing fields too.
    if not isinstance(body, dict) or not all(body.get(field) for field in ("domain_id", "cert_pem", "key_pem")):
        return
    name = hooks.stash.get("domain") or "example.org"
    with tempfile.TemporaryDirectory() as d:
        key, cert = os.path.join(d, "tls.key"), os.path.join(d, "tls.crt")
        openssl("req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "30",
                "-subj", "/CN=" + name, "-addext", "subjectAltName=DNS:" + name,
                "-keyout", key, "-out", cert)
        with open(cert) as cert_file, open(key) as key_file:
            on_the_created_domain(transaction, cert_pem=cert_file.read(), key_pem=key_file.read(),
                                  auto_renew=False)


@hooks.before("updateDomain")
def a_certificate_before_going_live(transaction):
    # Setting active to true is refused with 409 certificate_required for a
    # domain with no valid certificate, which every created domain is when
    # the probes reach it. The domain gets a self-signed one through the
    # admin API, once per run; the request is never rewritten.
    body = hooks.get_json(transaction)
    if not isinstance(body, dict) or body.get("active") is not True:
        return
    path = transaction["fullPath"].split("?")[0]
    domain_id = path.rstrip("/").rsplit("/", 1)[-1]
    origin = "%s//%s:%s" % (transaction["protocol"], transaction["host"], transaction["port"])
    ready = hooks.stash.get("certified_domains", {})
    if not domain_id.isdigit() or (origin + "\n" + domain_id) in ready:
        return
    headers = {"Content-Type": "application/json", "Authorization": "Bearer " + admin_token(origin)}

    def call(method, path, body=None):
        data = json.dumps(body).encode() if body is not None else None
        request = urllib.request.Request(origin + path, data=data, headers=headers, method=method)
        with urllib.request.urlopen(request, timeout=10) as response:
            return json.load(response).get("data")

    try:
        name = call("GET", "/api/v1/admin/domains/" + domain_id)["name"]
    except urllib.error.HTTPError as error:
        # No such domain: the probe meets the 404 itself.
        if error.code == 404:
            return
        raise
    with tempfile.TemporaryDirectory() as d:
        key, cert = os.path.join(d, "tls.key"), os.path.join(d, "tls.crt")
        openssl("req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "30",
                "-subj", "/CN=" + name, "-addext", "subjectAltName=DNS:" + name,
                "-keyout", key, "-out", cert)
        with open(cert) as cert_file, open(key) as key_file:
            call("POST", "/api/v1/admin/certificates",
                 {"domain_id": int(domain_id), "cert_pem": cert_file.read(),
                  "key_pem": key_file.read(), "auto_renew": False})
    ready[origin + "\n" + domain_id] = True
    hooks.stash["certified_domains"] = ready


@hooks.before("mtaStsPolicyFile")
def ask_for_the_created_domains_policy(transaction):
    # Served at mta-sts.<domain>: the domain is the Host's, not a parameter.
    transaction["request"]["headers"]["Host"] = "mta-sts." + (hooks.stash.get("domain") or "")


def set_the_admin_domains_policy(transaction, policy):
    """Make restmail.test's sending_policy policy ("" for none), through the
    admin API, unless it already is."""
    if hooks.stash.get("admin_domain_policy", "") == policy:
        return
    origin = "%s//%s:%s" % (transaction["protocol"], transaction["host"], transaction["port"])
    headers = {"Content-Type": "application/json", "Authorization": "Bearer " + admin_token(origin)}
    domain_id = hooks.stash.get("admin_domain_id")
    if domain_id is None:
        request = urllib.request.Request(origin + "/api/v1/admin/domains", headers=headers)
        with urllib.request.urlopen(request, timeout=10) as response:
            domains = json.load(response).get("data") or []
        domain_id = next((d["id"] for d in domains if d.get("name") == ADMIN.split("@")[1]), None)
        if domain_id is None:
            raise RuntimeError("the fixture admin's domain is not listed")
        hooks.stash["admin_domain_id"] = domain_id
    request = urllib.request.Request(
        origin + "/api/v1/admin/domains/%d" % domain_id,
        data=json.dumps({"sending_policy": policy}).encode(),
        headers=headers,
        method="PATCH",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        if response.status != 200:
            raise RuntimeError("could not set the admin domain's sending policy")
    hooks.stash["admin_domain_policy"] = policy


@hooks.before("sendMessage")
def a_sending_policy_for_the_send(transaction):
    # unsubscribe_not_allowed (422) is refused by a transactional sender
    # domain, and restmail.test has no policy: it is made transactional for
    # that request, and the probes built from it, alone. Every other send,
    # coverage probes included (they run no after hooks), finds no policy.
    paired = " > 422 > " in transaction.get("name", "")
    set_the_admin_domains_policy(transaction, "transactional" if paired else "")


@hooks.after("/api/v1/messages/send > * > 422 > application/json")
def no_sending_policy_after_the_refusal(transaction):
    set_the_admin_domains_policy(transaction, "")


@hooks.before("sendTestEmail")
def a_mailbox_at_each_test_send_address(transaction):
    # Coverage probes send from and to an address the fixture has no mailbox
    # for (someone@example.com), which the handler rightly answers 404. Host
    # its domain and give it an account, once, and leave it there: a test
    # send only adds a message. Every generated request byte stays untouched.
    from urllib.error import HTTPError

    body = hooks.get_json(transaction)
    if not isinstance(body, dict):
        return
    origin = "%s//%s:%s" % (transaction["protocol"], transaction["host"], transaction["port"])
    ready = hooks.stash.get("test_send_mailboxes", {})
    for field in ("from", "to"):
        address = body.get(field)
        if not isinstance(address, str) or address.count("@") != 1:
            continue
        local, domain = address.split("@")
        if not local or "." not in domain or (origin + "\n" + address.lower()) in ready:
            continue  # Leave a malformed address to the actual probe.
        headers = {"Content-Type": "application/json", "Authorization": "Bearer " + admin_token(origin)}
        request = urllib.request.Request(origin + "/api/v1/admin/domains", headers=headers)
        with urllib.request.urlopen(request, timeout=10) as response:
            domains = json.load(response).get("data") or []
        domain_id = next((d["id"] for d in domains if d.get("name") == domain), None)
        if domain_id is None:
            request = urllib.request.Request(
                origin + "/api/v1/admin/domains",
                data=json.dumps({"name": domain, "server_type": "traditional"}).encode(),
                headers=headers,
                method="POST",
            )
            try:
                with urllib.request.urlopen(request, timeout=10) as response:
                    domain_id = (json.load(response).get("data") or {}).get("id")
            except HTTPError as error:
                if 400 <= error.code < 500:
                    continue  # Not a domain the server can host: leave it to the probe.
                raise
        request = urllib.request.Request(
            origin + "/api/v1/admin/mailboxes?domain_id=%d" % domain_id, headers=headers)
        with urllib.request.urlopen(request, timeout=10) as response:
            accounts = json.load(response).get("data") or []
        if not any((a.get("address") or "").lower() == address.lower() for a in accounts):
            request = urllib.request.Request(
                origin + "/api/v1/accounts",
                data=json.dumps({"address": address}).encode(),
                headers=headers,
                method="POST",
            )
            with urllib.request.urlopen(request, timeout=10) as response:
                if response.status != 201:
                    raise RuntimeError("could not create the test send probe's account")
        ready[origin + "\n" + address.lower()] = True
        hooks.stash["test_send_mailboxes"] = ready


def schedule_a_message(transaction):
    """Store a scheduled message through the API and return its id, so the
    list and cancel examples have something of their own to act on."""
    origin = "%s//%s:%s" % (transaction["protocol"], transaction["host"], transaction["port"])
    headers = {"Content-Type": "application/json", "Authorization": "Bearer " + admin_token(origin)}
    body = {
        "from": ADMIN,
        "to": [ADMIN],
        "subject": "contract scheduled message",
        "body_text": "Contract fixture.",
        "scheduled_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() + 3600)),
    }
    request = urllib.request.Request(
        origin + "/api/v1/messages/send",
        data=json.dumps(body).encode(),
        headers=headers,
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        if response.status != 202:
            raise RuntimeError("could not schedule a contract message: HTTP %d" % response.status)
        return (json.load(response).get("data") or {}).get("id")


@hooks.before("listScheduledMessages")
def list_has_a_message_to_show(transaction):
    # Boundary probes and the report-only authorization phase do not need a
    # fixture; an empty list is still a valid answer. Only the examples
    # transaction is given one.
    name = transaction.get("name", "")
    if name.startswith("coverage:") or " as " in name:
        return
    schedule_a_message(transaction)


@hooks.before("cancelScheduledMessage")
def cancel_a_message_we_made(transaction):
    # A boundary probe (0, a word, a fraction) must reach the handler with its
    # own id, so its refusal is the one being judged. Only the examples
    # transaction, and the valid probe built from it, get a real message.
    path = transaction.get("fullPath", "").split("?")[0]
    last = path.rsplit("/", 1)[-1]
    if not last.isdigit() or int(last) < 1:
        return
    scheduled_id = schedule_a_message(transaction)
    if isinstance(scheduled_id, int):
        transaction["fullPath"] = "/api/v1/messages/scheduled/%d" % scheduled_id


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
    headers = {"Authorization": "Bearer " + admin_token(origin)}
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
        headers={"Content-Type": "application/json", "Authorization": "Bearer " + admin_token(origin)},
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
    # Vertrag 0.29 pairs the named examples but does not serialize XML.
    # Keep the malformed request on its 400; the 200 needs the valid XML.
    transaction["request"]["body"] = (
        "<Autodiscover><Request>" if " > 400 > " in transaction["name"]
        else AUTODISCOVER_REQUEST
    )


@hooks.before("/api/v1/auth/protocol-sign-in > * > 200 > application/json")
@hooks.before("/api/v1/auth/protocol-sign-in > * > 400 > application/json")
@hooks.before("/api/v1/auth/protocol-sign-in > * > 422 > application/json")
def sign_in_a_mail_client(transaction):
    app = principals()["app_password"]
    transaction["request"]["headers"]["Authorization"] = basic(app["address"], app["secret"])


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
    headers = {"Authorization": "Bearer " + admin_token(origin)}
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
