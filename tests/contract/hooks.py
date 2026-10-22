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
LINKABLE_PASSWORD = "password123"

PRINCIPALS = os.environ.get("CONTRACT_PRINCIPALS", ".cache/contract-principals.json")


def principals():
    with open(PRINCIPALS) as f:
        return json.load(f)


def basic(address, password):
    token = base64.b64encode(("%s:%s" % (address, password)).encode()).decode()
    return "Basic " + token


@hooks.before("login")
@hooks.before("refreshToken")
@hooks.before("linkAccount")
@hooks.before("createAppPassword")
@hooks.before("protocolSignIn")
def pace_auth_requests(transaction):
    # These operations share the default 1 request/second auth bucket. Coverage
    # repeats valid requests as well as refusals, so even correct credentials
    # drain its burst. Wait for a token without changing or retrying the probe.
    # Leave a small margin for clock/scheduling differences at the boundary.
    time.sleep(1.1)


def admin_token(origin):
    """Sign in for fixture setup without changing the transaction's principal.

    Basic auth accepts app passwords, not the admin's account password. Cache
    the setup session per origin so repeated probes do not exhaust login limits.
    """
    tokens = hooks.stash.get("setup_admin_tokens", {})
    if origin not in tokens:
        pace_auth_requests(None)
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
    body = hooks.get_json(transaction)
    # Supply only the fixture secret the named examples ask for. Preserve
    # missing/empty fields and coverage mutations so their refusals are reachable.
    if isinstance(body, dict) and body.get("password") == "supplied-by-contract-hook":
        body["password"] = LINKABLE_PASSWORD
        hooks.set_json(transaction, body)
    if not isinstance(body, dict):
        return
    # Each successful link consumes the unlinked state. Restore only that
    # state before a fixture's valid proof; do not alter generated body bytes,
    # malformed values, wrong passwords, or the transaction's principal.
    passwords = {"eve@restmail.test": LINKABLE_PASSWORD, "someone@example.com": "x"}
    address = body.get("address")
    if not isinstance(address, str) or address not in passwords or body.get("password") != passwords[address]:
        return
    headers = transaction["request"].get("headers") or {}
    if not any(key.lower() == "authorization" and value for key, value in headers.items()):
        return
    origin = "%s//%s:%s" % (transaction["protocol"], transaction["host"], transaction["port"])
    if address == "someone@example.com":
        ready = hooks.stash.get("link_fixture_prepared", {})
        if origin not in ready:
            # createAccount probes need this address absent; linkAccount probes
            # need it present with their generated password. Set up the latter
            # only now, after the preceding operation's probes have finished.
            subprocess.run(["chore", "test:contract-link-fixture"], check=True,
                           stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=20)
            ready[origin] = True
            hooks.stash["link_fixture_prepared"] = ready
    ids = hooks.stash.get("link_fixture_ids", {})
    key = origin + "\n" + address
    if key not in ids:
        request = urllib.request.Request(
            origin + "/api/v1/admin/mailboxes?limit=1000",
            headers={"Authorization": "Bearer " + admin_token(origin)},
        )
        with urllib.request.urlopen(request, timeout=10) as response:
            accounts = json.load(response).get("data") or []
        ids[key] = next((account["id"] for account in accounts if account.get("address") == address), None)
        if ids[key] is None:
            raise RuntimeError("contract account-link fixture is missing")
        hooks.stash["link_fixture_ids"] = ids
    request = urllib.request.Request(
        origin + "/api/v1/accounts/" + str(ids[key]), headers=headers, method="DELETE")
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            if response.status != 204:
                raise RuntimeError("could not clear the preceding contract link")
    except urllib.error.HTTPError as error:
        if error.code != 404:  # The first proof has no preceding grant.
            raise


@hooks.after("createDomain")
def keep_the_created_domain(transaction):
    data = hooks.get_json(transaction, "real").get("data") or {}
    # The refused create (name_missing, 422) runs straight after the one that
    # made the domain, and created nothing to keep.
    if not data.get("id"):
        return
    hooks.stash["domain_id"] = data.get("id")
    hooks.stash["domain"] = data.get("name")


@hooks.before("deleteDomain")
def delete_only_the_created_domain(transaction):
    # Coverage's in-range probe names domain 1, the fixture admin's own. The
    # server refuses it while it has accounts (409, #388), which a valid probe
    # counts as a finding, and before that refusal the probe deleted it and
    # every later send failed. Malformed ids are sent: they test the 400.
    domain_id = transaction["fullPath"].split("?")[0].rstrip("/").split("/")[-1]
    if not domain_id.isdecimal() or int(domain_id) < 1:
        return
    if int(domain_id) != hooks.stash.get("domain_id"):
        transaction["skip"] = True


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
    # sendMessage's generated bodies spend the same account's submission
    # budget, even when their sender is refused. Wait out one minute window
    # for fixture setup only; the actual probes must see every refusal.
    for attempt in range(2):
        try:
            with urllib.request.urlopen(request, timeout=10) as response:
                if response.status != 202:
                    raise RuntimeError("could not schedule a contract message: HTTP %d" % response.status)
                return (json.load(response).get("data") or {}).get("id")
        except urllib.error.HTTPError as error:
            retry_after = error.headers.get("Retry-After", "")
            if attempt or error.code != 429 or not retry_after.isdecimal() or not 1 <= int(retry_after) <= 60:
                raise
            error.close()
            time.sleep(int(retry_after))


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


@hooks.before("receiveTlsReport")
def pace_the_tls_reports(transaction):
    # The route is rate-limited per client IP, 1 a second after a burst of 10
    # (DefaultTLSRPTRateLimitRPS, internal/config/config.go), and coverage
    # sends more probes than that: a 429 is the limiter, not the report.
    time.sleep(1)


@hooks.before("receiveTlsReport")
def host_the_reported_policy_domains(transaction):
    # Generated reports name domains other than restmail.test. Host them so
    # a valid report reaches storage; leave every generated byte unchanged.
    body = hooks.get_json(transaction)
    policies = body.get("policies") if isinstance(body, dict) else None
    if not isinstance(policies, list):
        return
    origin = "%s//%s:%s" % (transaction["protocol"], transaction["host"], transaction["port"])
    hosted = set()
    for entry in policies:
        policy = entry.get("policy") if isinstance(entry, dict) else None
        domain = policy.get("policy-domain") if isinstance(policy, dict) else None
        if not isinstance(domain, str) or not domain or domain in hosted:
            continue
        hosted.add(domain)
        headers = {"Authorization": "Bearer " + admin_token(origin)}
        hosted_domain_id(origin, headers, domain.rstrip("."))


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


def sender_rules_path(transaction):
    """The list a sender rule probe adds to, and its domain id, or None for a
    malformed id (left to the probe's own 400)."""
    path = transaction["fullPath"].split("?")[0].rstrip("/")
    domain_id = path.split("/")[-2]
    if not domain_id.isdecimal() or int(domain_id) < 1:
        return None
    return path


def sender_rules(origin, path, headers):
    request = urllib.request.Request(origin + path, headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            return json.load(response).get("data") or []
    except urllib.error.HTTPError as error:
        if error.code in (400, 403, 404):
            return None  # Leave a domain that is not there to the probe.
        raise


@hooks.before("addToAllowlist")
@hooks.before("addToBlocklist")
def remove_a_previous_sender_rule_probe(transaction):
    # The domain stays for the whole run (deleteDomain refuses one with
    # accounts, #388), so probes repeating a pattern meet the entry an
    # earlier one added, a 409. Claim an absent pattern before its first add,
    # then remove only that probe's entry when generated bodies repeat it.
    # Existing entries and all generated request bytes stay untouched.
    body = hooks.get_json(transaction)
    pattern = body.get("pattern") if isinstance(body, dict) else None
    path = sender_rules_path(transaction)
    if not isinstance(pattern, str) or not pattern or path is None:
        return
    origin = "%s//%s:%s" % (transaction["protocol"], transaction["host"], transaction["port"])
    headers = {"Authorization": "Bearer " + admin_token(origin)}
    rules = sender_rules(origin, path, headers)
    if rules is None:
        return
    key = origin + path + "\n" + pattern
    owned = hooks.stash.get("created_sender_rule_probes", {})
    rule = next((r for r in rules if r.get("pattern") == pattern), None)
    if rule is None:
        owned[key] = True
        hooks.stash["created_sender_rule_probes"] = owned
        return
    if key not in owned:
        return
    request = urllib.request.Request(
        origin + path + "/" + str(rule["id"]), headers=headers, method="DELETE")
    with urllib.request.urlopen(request, timeout=10) as response:
        if response.status != 204:
            raise RuntimeError("could not remove the previous sender rule probe")


@hooks.after("addToAllowlist")
@hooks.after("addToBlocklist")
def keep_the_added_sender_rule(transaction):
    # Only the example runs after hooks, and its id is what
    # removeFromAllowlist and removeFromBlocklist are linked to. Probes
    # repeating its pattern remove that entry above, so remember what to
    # restore.
    data = hooks.get_json(transaction, "real").get("data") or {}
    if not data.get("id"):
        return
    path = sender_rules_path(transaction)
    created = hooks.stash.get("created_sender_rules", {})
    created[path] = {"id": data["id"], "body": hooks.get_json(transaction)}
    hooks.stash["created_sender_rules"] = created


@hooks.before("removeFromAllowlist")
@hooks.before("removeFromBlocklist")
def on_the_added_sender_rule(transaction):
    # Follow the linked entry to its current id, adding it again when a probe
    # removed it without a successful add of its own to replace it. Any other
    # id (the 404 examples, path parameter probes) is left alone.
    path, _, query = transaction["fullPath"].partition("?")
    parent, _, entry_id = path.rpartition("/")
    created = hooks.stash.get("created_sender_rules", {}).get(parent)
    if not created or entry_id != str(created["id"]):
        return
    origin = "%s//%s:%s" % (transaction["protocol"], transaction["host"], transaction["port"])
    headers = {"Content-Type": "application/json", "Authorization": "Bearer " + admin_token(origin)}
    pattern = created["body"]["pattern"]
    rule = next((r for r in sender_rules(origin, parent, headers) or [] if r.get("pattern") == pattern), None)
    if rule is None:
        request = urllib.request.Request(
            origin + parent, data=json.dumps(created["body"]).encode(), headers=headers, method="POST")
        with urllib.request.urlopen(request, timeout=10) as response:
            rule = json.load(response)["data"]
    transaction["fullPath"] = parent + "/" + str(rule["id"]) + ("?" + query if query else "")


def hosted_domain_id(origin, headers, name):
    """The id of the domain called name, hosting it first if need be, or None
    when the server will not host it (left to the probe)."""
    request = urllib.request.Request(origin + "/api/v1/admin/domains", headers=headers)
    with urllib.request.urlopen(request, timeout=10) as response:
        domains = json.load(response).get("data") or []
    domain_id = next((d["id"] for d in domains if d.get("name") == name), None)
    if domain_id is not None:
        return domain_id
    request = urllib.request.Request(
        origin + "/api/v1/admin/domains",
        data=json.dumps({"name": name, "server_type": "traditional"}).encode(),
        headers=dict(headers, **{"Content-Type": "application/json"}),
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            return (json.load(response).get("data") or {}).get("id")
    except urllib.error.HTTPError as error:
        if 400 <= error.code < 500:
            return None
        raise


@hooks.before("mozillaAutoconfig")
@hooks.before("mozillaAutoconfigWellKnown")
def an_active_domain_for_autoconfig(transaction):
    # Discovery requires an active hosted domain. updateDomain's example now
    # changes only sending_policy, so it no longer incidentally activates the
    # generated email's domain. Prepare that state without changing the query.
    query = urllib.parse.parse_qs(urllib.parse.urlsplit(transaction["fullPath"]).query)
    address = query.get("emailaddress", [""])[0]
    if address.count("@") != 1:
        return
    local, name = address.split("@")
    if not local or "." not in name:
        return  # Leave malformed addresses to the actual probe.
    origin = "%s//%s:%s" % (transaction["protocol"], transaction["host"], transaction["port"])
    headers = {"Content-Type": "application/json", "Authorization": "Bearer " + admin_token(origin)}
    domain_id = hosted_domain_id(origin, headers, name)
    if domain_id is None:
        return
    path = origin + "/api/v1/admin/domains/" + str(domain_id)
    with urllib.request.urlopen(urllib.request.Request(path, headers=headers), timeout=10) as response:
        domain = json.load(response).get("data") or {}
    if domain.get("active"):
        return
    activate = urllib.request.Request(path, data=b'{"active":true}', headers=headers, method="PATCH")
    try:
        with urllib.request.urlopen(activate, timeout=10):
            return
    except urllib.error.HTTPError as error:
        if error.code != 409 or (json.load(error).get("error") or {}).get("code") != "certificate_required":
            raise
    # Activation uses the production certificate gate. Supply a real fixture
    # certificate only when that precondition is missing, then activate it.
    with tempfile.TemporaryDirectory() as directory:
        key, cert = os.path.join(directory, "tls.key"), os.path.join(directory, "tls.crt")
        openssl("req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "30",
                "-subj", "/CN=" + name, "-addext", "subjectAltName=DNS:" + name,
                "-keyout", key, "-out", cert)
        with open(cert) as cert_file, open(key) as key_file:
            body = {"domain_id": domain_id, "cert_pem": cert_file.read(),
                    "key_pem": key_file.read(), "auto_renew": False}
        request = urllib.request.Request(origin + "/api/v1/admin/certificates",
                                         data=json.dumps(body).encode(), headers=headers, method="POST")
        with urllib.request.urlopen(request, timeout=10):
            pass
    with urllib.request.urlopen(activate, timeout=10):
        pass


def alias_on(origin, headers, domain_id, source):
    request = urllib.request.Request(
        origin + "/api/v1/admin/aliases?domain_id=%d" % domain_id, headers=headers)
    with urllib.request.urlopen(request, timeout=10) as response:
        aliases = json.load(response).get("data") or []
    return next((a for a in aliases if (a.get("source_address") or "").lower() == source.lower()), None)


@hooks.before("createAlias")
def remove_a_previous_alias_probe(transaction):
    # Coverage probes alias an address on a domain the fixture does not host
    # (someone@example.com), which the handler rightly refuses with 404, and
    # repeat it, which meets the alias an earlier probe made (409). Host the
    # domain, claim an absent source before its first create, and remove only
    # that probe's alias when generated bodies repeat it. Existing aliases and
    # all generated request bytes stay untouched.
    body = hooks.get_json(transaction)
    source = body.get("source_address") if isinstance(body, dict) else None
    if not isinstance(source, str) or source.count("@") != 1:
        return
    local, domain = source.split("@")
    if not local or "." not in domain:
        return  # Leave a malformed address to the actual probe.
    origin = "%s//%s:%s" % (transaction["protocol"], transaction["host"], transaction["port"])
    headers = {"Authorization": "Bearer " + admin_token(origin)}
    domain_id = hosted_domain_id(origin, headers, domain.lower())
    if domain_id is None:
        return
    alias = alias_on(origin, headers, domain_id, source)
    owned = hooks.stash.get("created_alias_probes", {})
    key = origin + "\n" + source.lower()
    if alias is None:
        owned[key] = True
        hooks.stash["created_alias_probes"] = owned
        return
    if key not in owned:
        return
    request = urllib.request.Request(
        origin + "/api/v1/admin/aliases/%d" % alias["id"], headers=headers, method="DELETE")
    with urllib.request.urlopen(request, timeout=10) as response:
        if response.status != 204:
            raise RuntimeError("could not remove the previous alias probe")


@hooks.after("createAlias")
def keep_the_created_alias(transaction):
    # Only the example runs after hooks, and its id is what getAlias,
    # updateAlias and deleteAlias are linked to. Probes repeating its source
    # remove that alias above, so remember what to restore.
    data = hooks.get_json(transaction, "real").get("data") or {}
    if not data.get("id"):
        return
    hooks.stash["created_alias"] = {"id": data["id"], "body": hooks.get_json(transaction)}


@hooks.before("getAlias")
@hooks.before("updateAlias")
@hooks.before("deleteAlias")
def on_the_created_alias(transaction):
    # Follow the linked alias to its current id, recreating it when a probe
    # removed it without a successful create of its own to replace it. Any
    # other id (the 404 examples, path parameter probes) is left alone.
    created = hooks.stash.get("created_alias")
    path, _, query = transaction["fullPath"].partition("?")
    parent, _, alias_id = path.rpartition("/")
    if not created or alias_id != str(created["id"]):
        return
    origin = "%s//%s:%s" % (transaction["protocol"], transaction["host"], transaction["port"])
    headers = {"Content-Type": "application/json", "Authorization": "Bearer " + admin_token(origin)}
    source = created["body"]["source_address"]
    domain_id = hosted_domain_id(origin, headers, source.split("@")[1].lower())
    alias = alias_on(origin, headers, domain_id, source)
    if alias is None:
        request = urllib.request.Request(
            origin + "/api/v1/admin/aliases", data=json.dumps(created["body"]).encode(),
            headers=headers, method="POST")
        with urllib.request.urlopen(request, timeout=10) as response:
            alias = json.load(response)["data"]
    transaction["fullPath"] = parent + "/" + str(alias["id"]) + ("?" + query if query else "")


@hooks.before("createAccount")
def host_the_account_probe_domain(transaction):
    # Coverage probes address a domain the fixture does not host
    # (someone@example.com), which the handler rightly refuses with 422.
    # Host the domain, claim an absent address before its first create, and
    # remove only that probe's account when generated bodies repeat it.
    # Existing accounts and all generated request bytes stay untouched.
    from urllib.error import HTTPError

    body = hooks.get_json(transaction)
    address = body.get("address") if isinstance(body, dict) else None
    if not isinstance(address, str) or address.count("@") != 1:
        return
    local, domain = address.split("@")
    if not local or "." not in domain:
        return  # Leave a malformed address to the actual probe.
    origin = "%s//%s:%s" % (transaction["protocol"], transaction["host"], transaction["port"])
    if address.lower() == "someone@example.com":
        # A later create can replace the account whose credential/id the link
        # hook prepared. Rebuild its proof before the next generated link.
        hooks.stash.get("link_fixture_prepared", {}).pop(origin, None)
        hooks.stash.get("link_fixture_ids", {}).pop(origin + "\n" + address.lower(), None)
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
                return  # Not a domain the server can host: leave it to the probe.
            raise
    request = urllib.request.Request(
        origin + "/api/v1/admin/mailboxes?domain_id=%d" % domain_id, headers=headers)
    with urllib.request.urlopen(request, timeout=10) as response:
        accounts = json.load(response).get("data") or []
    account = next((a for a in accounts if (a.get("address") or "").lower() == address.lower()), None)
    owned = hooks.stash.get("created_account_probes", {})
    key = origin + "\n" + address.lower()
    if account is None:
        owned[key] = True
        hooks.stash["created_account_probes"] = owned
        return
    if key not in owned:
        return
    request = urllib.request.Request(
        origin + "/api/v1/admin/mailboxes/%d" % account["id"], headers=headers, method="DELETE")
    with urllib.request.urlopen(request, timeout=10) as response:
        if response.status != 204:
            raise RuntimeError("could not remove the previous account probe")


@hooks.after("createAccount")
def keep_the_created_account(transaction):
    # Only the example runs after hooks, and its id is what getMailbox,
    # updateMailbox and deleteMailbox are linked to. Probes repeating its
    # address remove that account above, so remember what to restore.
    data = hooks.get_json(transaction, "real").get("data") or {}
    if not data.get("id"):
        return
    hooks.stash["created_account"] = {"id": data["id"], "body": hooks.get_json(transaction)}


@hooks.before("getMailbox")
@hooks.before("updateMailbox")
@hooks.before("deleteMailbox")
def on_the_created_account(transaction):
    # Follow the linked account to its current id, recreating it when a
    # probe removed it without a successful create of its own to replace it.
    # Any other id (the 404 examples, path parameter probes) is left alone.
    created = hooks.stash.get("created_account")
    path, _, query = transaction["fullPath"].partition("?")
    parent, _, account_id = path.rpartition("/")
    if not created or account_id != str(created["id"]):
        return
    origin = "%s//%s:%s" % (transaction["protocol"], transaction["host"], transaction["port"])
    headers = {"Content-Type": "application/json", "Authorization": "Bearer " + admin_token(origin)}
    address = created["body"]["address"]
    request = urllib.request.Request(origin + "/api/v1/admin/domains", headers=headers)
    with urllib.request.urlopen(request, timeout=10) as response:
        domains = json.load(response).get("data") or []
    domain_id = next(d["id"] for d in domains if d.get("name") == address.split("@")[1])
    request = urllib.request.Request(
        origin + "/api/v1/admin/mailboxes?domain_id=%d" % domain_id, headers=headers)
    with urllib.request.urlopen(request, timeout=10) as response:
        accounts = json.load(response).get("data") or []
    account = next((a for a in accounts if (a.get("address") or "").lower() == address.lower()), None)
    if account is None:
        request = urllib.request.Request(
            origin + "/api/v1/accounts", data=json.dumps(created["body"]).encode(),
            headers=headers, method="POST")
        with urllib.request.urlopen(request, timeout=10) as response:
            account = json.load(response)["data"]
    transaction["fullPath"] = parent + "/" + str(account["id"]) + ("?" + query if query else "")
