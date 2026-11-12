"""vertrag hooks for the contract tests (#376).

Only what the spec cannot hold: the fixture's passwords, which no spec example
should carry; the refresh token a sign-in hands out; a fresh key pair; and the
id of the domain the run created, where a request carries it in its body
rather than its path (links fill only parameters). Links supply the other
existing resources. A 404 named after a path parameter's example (getDomain's,
getCertificate's, getCustomFilter's and getAlias's `missing`) is sent with that
example's id, which no link replaces (vertrag 0.30, antimatter-studios/vertrag#85).

The principals are the E2E fixture's (tests/e2e/contract_principals_test.go),
read from the file it writes.
"""

import base64
import datetime
import json
import os
import re
import ssl
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from email.utils import formatdate
from http.cookies import SimpleCookie
from pathlib import Path

import vertrag_hooks as hooks

with (
    Path(__file__).resolve().parent / "fixtureplan" / "defaults.json"
).open() as _source:
    FIXTURE_PLAN = json.load(_source)

ADMIN = FIXTURE_PLAN["principals"]["admin"]["address"]
ADMIN_PASSWORD = "admin123!@"
# A seeded account that nobody owns, to link (cmd/seed's fixture password).
LINKABLE_PASSWORD = "password123"
# The two-factor administrator for setup a password cannot do.
sys.path.insert(0, str(Path(__file__).resolve().parent))
import mfa_admin  # noqa: E402
import rate_limits
import owner_key  # noqa: E402
import reset_seed
from identities import address_key, domain_key
from api_keys_live import totp  # noqa: E402
from queue_entry_hooks import a_disposable_queue_entry
from lost_probes import DOCUMENTED_SENDS  # noqa: E402
from probes import probe_certificate

PRINCIPALS = os.environ.get("CONTRACT_PRINCIPALS", ".cache/contract-principals.json")


def principals():
    with open(PRINCIPALS) as f:
        return json.load(f)


def mfa_admin_token(origin):
    """A session signed in with a real second factor, renewed after ten minutes."""
    cached = hooks.stash.get("mfa_admin_sessions", {}).get(origin)
    if cached and time.time() - cached["at"] < 600:
        return cached["token"]
    sessions = hooks.stash.get("mfa_admin_sessions", {})
    sessions[origin] = {"token": mfa_admin.session(origin), "at": time.time()}
    hooks.stash["mfa_admin_sessions"] = sessions
    return sessions[origin]["token"]


def in_the_coverage_process():
    """Whether this is the coverage run (run.sh sets CONTRACT_COVERAGE on it). A
    before hook cannot tell from the transaction: Vertrag adds the "coverage: "
    prefix only to the result it reports, so a hook sees the source name."""
    return os.environ.get("CONTRACT_COVERAGE") == "1"


def origin_of(transaction):
    return "{}//{}:{}".format(transaction["protocol"], transaction["host"], transaction["port"])


def probe_admin_token(origin):
    """The probe administrator's session, renewed like the two-factor one's."""
    cached = hooks.stash.get("probe_admin_sessions", {}).get(origin)
    if cached and time.time() - cached["at"] < 600:
        return cached["token"]
    sessions = hooks.stash.get("probe_admin_sessions", {})
    sessions[origin] = {"token": mfa_admin.probe_session(origin), "at": time.time()}
    hooks.stash["probe_admin_sessions"] = sessions
    return sessions[origin]["token"]


def session_claims(transaction):
    """The claims of the session the request carries, or None (a credential
    other than a session's, or none)."""
    header = transaction["request"]["headers"].get("Authorization", "")
    if not header.startswith("Bearer "):
        return None
    try:
        payload = header[len("Bearer "):].split(".")[1]
        claims = json.loads(base64.urlsafe_b64decode(payload + "=" * (-len(payload) % 4)))
    except (IndexError, ValueError):
        return None
    return claims if isinstance(claims, dict) else None


def session_address(transaction):
    """The address of the session the request carries, or None (a credential
    other than a session's, or none)."""
    claims = session_claims(transaction)
    return claims.get("address") if claims is not None else None


def with_the_fixture_admins_session(transaction):
    """Whether the request carries the fixture admin's own session. The
    authorization phase's principals and an unauthenticated 401 do not: its
    admin principal is the two-factor administrator (authorization.py)."""
    return session_address(transaction) == ADMIN


def as_the_two_factor_admin(transaction):
    """Send a request made with the fixture admin's session with the two-factor
    administrator's instead, and say whether it was."""
    if not with_the_fixture_admins_session(transaction):
        return False
    transaction["request"]["headers"]["Authorization"] = "Bearer " + mfa_admin_token(origin_of(transaction))
    return True


def sent_as_the_two_factor_admin(transaction):
    """Whether the request goes out as the two-factor administrator: the
    fixture admin's session is swapped for its (as_the_two_factor_admin), or
    it already carries it, as the authorization phase's admin principal does.
    That phase ends a request whose credential a hook changes unsent, so its
    admin signs in as this administrator rather than being swapped."""
    return as_the_two_factor_admin(transaction) or session_address(transaction) == mfa_admin.ADDRESS


# The operations moved to a clean start, and only in the run that asks for it
# (run.sh's own-data phase): a reset in the primary run would wipe what the
# operations still to move depend on. See the README, "Moved to a clean start".
# What a reset does not remove: the setup sessions, whose accounts it keeps with
# their ids and credentials.
SURVIVES_A_RESET = (
    "setup_admin_tokens",
    "mfa_admin_sessions",
)


def asked_for_a_clean_start():
    return os.environ.get("CONTRACT_RESET_SEED") == "1"


def setup_origin(transaction):
    """Where a hook makes its setup calls: the run's own endpoint, which
    run.sh's own-data phase names in CONTRACT_SETUP_ORIGIN, or else the
    exchange's. They differ for the gateway routes, which the topology sends to
    the API's internal listener (vertrag.yml, endpoints): that listener serves
    only those routes, to a client certificate, so a reset or an account is
    made on the public one."""
    return os.environ.get("CONTRACT_SETUP_ORIGIN") or origin_of(transaction)


def start_clean(transaction):
    """Reset and seed before this exchange, and forget what that removed. A
    failed reset raises, so the exchange is reported as an error and not sent."""
    origin = setup_origin(transaction)
    reset_seed.reset_and_seed(origin, admin_token(origin), fixtures=())
    for key in [k for k in hooks.stash if k not in SURVIVES_A_RESET]:
        del hooks.stash[key]


OWN_DOMAIN = "contract-own-domain.test"


def a_domain_of_its_own(transaction):
    """A domain made for this exchange, after its reset. The name cannot meet an
    earlier one, because the reset removed it."""
    return fixture_call(
        transaction,
        "POST",
        "/api/v1/admin/domains",
        {"name": OWN_DOMAIN, "server_type": "traditional"},
    )["id"]


def to_domain(transaction, domain_id):
    """Send the request to that domain: only the id in the path changes, so a
    generated body and the request's own credential stay as they are."""
    path, separator, query = transaction["fullPath"].partition("?")
    parts = path.split("/")
    parts[5] = str(domain_id)
    transaction["fullPath"] = "/".join(parts) + separator + query


@hooks.before("linkAccount")
@hooks.before("unlinkAccount")
@hooks.before("issueClientCredentialsToken")
def account_links_and_the_token_exchange_after_a_reset(transaction):
    # The account link operations and the token exchange, moved to a clean
    # start (the client secret operations reset in a_credential_to_manage):
    # each exchange, refusals included, resets and seeds first. None needs
    # anything made. The link's 201 links the seeded eve@restmail.test, which
    # the reset puts back unlinked, with the seed's password; the refusals keep
    # their ids and bodies: 0 and missing's 2147483647 need nothing to exist,
    # and a body the schema refuses is refused before any lookup. The requests
    # are still sent as the two-factor administrator, which the reset keeps
    # with its credentials, and nothing here revokes or ends that session.
    # Registered ahead of every other hook of these operations. A run with no
    # reset changes nothing here.
    if asked_for_a_clean_start():
        start_clean(transaction)


# The password of the accounts made for a sign-in exchange after its reset.
# Never the fixture admin's or the two-factor administrator's.
SIGN_IN_PASSWORD = "velvet-tundra-compass-otter-47"


@hooks.before("login")
@hooks.before("logout")
@hooks.before("refreshToken")
@hooks.before("openGatewaySession")
@hooks.before("resetAdmin")
@hooks.before("recoverAccount")
@hooks.before("ownerChallenge")
@hooks.before("ownerToken")
@hooks.before("ownerStatus")
@hooks.before("getTwoFactorStatus")
@hooks.before("enrollTwoFactor")
@hooks.before("confirmTwoFactor")
@hooks.before("disableTwoFactor")
@hooks.before("changeOwnPassword")
@hooks.before("requestRecoveryEmail")
@hooks.before("redeemRecoveryEmail")
@hooks.before("getRecoveryEmail")
@hooks.before("registerRecoveryEmail")
@hooks.before("recoveryEmailLink")
@hooks.before("verifyRecoveryEmail")
def sign_in_and_recovery_after_a_reset(transaction):
    # The sign-in and recovery operations, moved to a clean start: each
    # exchange, refusals included, resets and seeds first. Refusals keep their
    # bodies and tokens. A success that ends a session, rotates one or starts a
    # second factor acts on an account made for it after the reset, never on a
    # session or credential the run uses:
    # - logout's 204 ends a session of that account (the_session_it_ended_is_over
    #   checks it is over);
    # - refreshToken's 200 rotates that account's refresh token;
    # - enrollTwoFactor's 200 starts that account's enrollment.
    # openGatewaySession's credential is an app password of an account made
    # for it: the reset removed the principal sign_in_a_mail_client names. The
    # other successes read (getTwoFactorStatus, getRecoveryEmail,
    # recoveryEmailLink), sign the fixture admin in (login), or ask about an
    # address no account holds (requestRecoveryEmail), and need nothing made.
    # Registered ahead of every other hook of these operations. A run with no
    # reset changes nothing here.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    name = transaction.get("name", "")
    path = transaction["fullPath"].partition("?")[0]
    if path == "/api/v1/gateway-sessions":
        if any(f" > {status} > " in name for status in ("201", "422")):
            address, secret = an_app_password_of_its_own(transaction)
            body = hooks.get_json(transaction)
            if isinstance(body, dict):
                body["address"], body["password"] = address, secret
                hooks.set_json(transaction, body)
        return
    if path == "/api/v1/auth/logout" and " > 204" in name:
        refresh_token = a_session_of_its_own(transaction)["refresh_token"]
        body = hooks.get_json(transaction)
        if isinstance(body, dict) and "refresh_token" in body:
            body["refresh_token"] = refresh_token
            hooks.set_json(transaction, body)
            hooks.stash["ended_refresh_token"] = {"name": name, "token": refresh_token}
        return
    if path == "/api/v1/auth/refresh" and " > 200 > " in name:
        body = hooks.get_json(transaction)
        if isinstance(body, dict) and body.get("refresh_token") == "fixture-refresh-token":
            body["refresh_token"] = a_session_of_its_own(transaction)["refresh_token"]
            hooks.set_json(transaction, body)
        return
    if path == "/api/v1/auth/2fa/enroll" and " > 200 > " in name:
        session = a_session_of_its_own(transaction)
        transaction["request"]["headers"]["Authorization"] = "Bearer " + session["access_token"]


def a_session_of_its_own(transaction):
    """A native session of an account made for this exchange, after its reset:
    its access and refresh tokens."""
    origin = origin_of(transaction)
    address = a_made_account(origin, "contract-sign-in", SIGN_IN_PASSWORD)["address"]
    pace_auth_requests(None)
    request = urllib.request.Request(
        origin + "/api/v1/auth/login",
        data=json.dumps({"email": address, "password": SIGN_IN_PASSWORD, "client": "native"}).encode(),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        data = json.load(response).get("data") or {}
    if not data.get("access_token") or not data.get("refresh_token"):
        raise RuntimeError("the account made for this exchange signed in without its tokens")
    # The setup sign-in and the exchange share the auth rate bucket.
    pace_auth_requests(None)
    return data




def an_app_password_of_its_own(transaction):
    """An app password of an account made for this exchange, after its reset,
    minted by the two-factor administrator as the principals' one was."""
    origin = setup_origin(transaction)
    account = a_made_account(origin, "contract-mail-client", SIGN_IN_PASSWORD)
    request = urllib.request.Request(
        origin + f"/api/v1/accounts/{account['id']:d}/credentials",
        data=json.dumps({"name": "Contract mail client", "password": mfa_admin.PASSWORD}).encode(),
        headers={"Content-Type": "application/json", "Authorization": "Bearer " + mfa_admin_token(origin)},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        secret = json.load(response)["data"]["password"]
    return account["address"], secret


@hooks.after("logout")
def the_session_it_ended_is_over(transaction):
    # After a reset, logout's 204 ends a session made for it. Its refresh
    # token must be refused afterwards, or the exchange fails: an answer of 204
    # alone is given for a token that ends nothing too. Refusals and a run with
    # no reset are left alone.
    ended = hooks.stash.pop("ended_refresh_token", None)
    name = transaction.get("name", "")
    real = transaction.get("real") or {}
    if not asked_for_a_clean_start() or " > 204" not in name or str(real.get("statusCode")) != "204":
        return
    if ended is None or ended["name"] != name:
        transaction["fail"] = "the sign-out had no session made for it to end"
        return
    pace_auth_requests(None)
    request = urllib.request.Request(
        origin_of(transaction) + "/api/v1/auth/refresh",
        data=json.dumps({"refresh_token": ended["token"]}).encode(),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            status = response.status
    except urllib.error.HTTPError as error:
        status = error.code
    if status != 401:
        transaction["fail"] = f"the session made for it was not ended: its refresh token answered {status:d}, not 401"


# The invitation listCalendarEvents' 200 lists after its reset: a calendar
# request the seeded admin@restmail.test sends itself, so it is delivered to
# that account with its version recorded.
OWN_INVITATION_UID = "contract-own-invitation@restmail.test"
OWN_INVITATION = {
    "from": ADMIN,
    "to": [ADMIN],
    "subject": "Contract invitation",
    "body_text": "An invitation made for this exchange after its reset.",
    "calendar_event": {
        "uid": OWN_INVITATION_UID,
        "sequence": 0,
        "method": "REQUEST",
        "summary": "Contract invitation",
        "dtstart": "2030-01-01T10:00:00Z",
        "dtend": "2030-01-01T11:00:00Z",
        "all_day": False,
        "organizer": {"address": ADMIN},
        "attendees": [{"address": ADMIN}],
    },
}


@hooks.before("getAccount")
@hooks.before("listCalendarEvents")
@hooks.before("accountEvents")
@hooks.before("respondToCalendar")
@hooks.before("probeServices")
@hooks.before("verifyDelivery")
def the_last_free_operations_after_a_reset(transaction):
    # The last operations step 2 found free, moved to a clean start: each
    # exchange, refusals included, resets and seeds first. Refusals keep their
    # ids and bodies. The successes:
    # - getAccount's and listCalendarEvents' 200 go to the seeded
    #   admin@restmail.test: the reset removed the account their example names
    #   and seeded that address again under another id. Only the id in the
    #   path changes (to_the_seeded_account).
    # - listCalendarEvents' 200 gets an invitation of its own, which the after
    #   hook the_listing_holds_what_was_made requires it to list.
    # - respondToCalendar's 200 and 403 reply to an invitation of their own
    #   (to_the_invitation_made_for_it).
    # - verifyDelivery's 200 asks about an address no mailbox has, with no
    #   wait, and is answered found: false (#388); probeServices' 200 probes
    #   the database. Neither needs anything made.
    # No link starts from any of them, and only listAccounts' link reaches
    # getAccount and listCalendarEvents; listAccounts is not selected.
    # Registered ahead of every other hook of these operations. A run with no
    # reset changes nothing here.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    name = transaction.get("name", "")
    path = transaction["fullPath"].partition("?")[0]
    if path.endswith("/calendar-reply") and (" > 200 > " in name or " > 403 > " in name):
        to_the_invitation_made_for_it(transaction)
    if " > 200 > " not in name:
        return
    if path.startswith("/api/v1/accounts/"):
        to_the_seeded_account(transaction)
    if path.endswith("/calendar-events"):
        fixture_call(transaction, "POST", "/api/v1/messages/send", OWN_INVITATION)
        made_for_the_listing(transaction, "invitation", {"uid": OWN_INVITATION_UID})


# A recipient is suppressed only when mail to it hard-bounces (the queue
# worker's suppress, internal/gateway/queue/suppression.go): a domain that does
# not exist is one (mxHosts answers 550 5.1.2, a bad domain). .invalid never
# exists (RFC 6761), so mail to it bounces on the first try. The testbed's
# DNS answers .invalid NXDOMAIN itself (tests/contract/dnsmasq.conf), never
# asking a resolver outside it.
OWN_SUPPRESSED_DOMAIN = "nowhere.invalid"
# The queue worker polls every 5 seconds by default (QUEUE_POLL_INTERVAL) and
# bounces on its first try, so this is many polls.
SUPPRESSION_WAIT = 90


@hooks.before("clearSuppression")
def clear_a_suppression_made_after_the_reset(transaction):
    # clearSuppression, moved to a clean start: each exchange, refusals
    # included, resets and seeds first, and refusals keep their ids. The 204
    # clears a suppression made after the reset: the fixture admin sends a
    # message to an address at a domain that does not exist, the gateway's
    # queue worker bounces it and suppresses the address for the fixture
    # domain, and the exchange clears that suppression. No link reaches it.
    # Registered ahead of every other clearSuppression hook, whose fixture
    # suppression the reset removed. A run with no reset changes nothing here.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    # A 204 names no media type: its name ends at the status.
    if not transaction.get("name", "").endswith(" > 204"):
        return
    domain = next(
        (d for d in fixture_call(transaction, "GET", "/api/v1/admin/domains") or []
         if d.get("name") == FIXTURE_PLAN["domain"]),
        None,
    )
    if domain is None:
        raise RuntimeError("the seed hosts no {}".format(FIXTURE_PLAN["domain"]))
    address = "contract-suppressed-{}@{}".format(time.time_ns(), OWN_SUPPRESSED_DOMAIN)
    fixture_call(transaction, "POST", "/api/v1/messages/send", {
        "from": ADMIN,
        "to": [address],
        "subject": "Contract suppression",
        "body_text": "Sent after the reset to a domain that does not exist, so it bounces.",
    })
    collection = "/api/v1/admin/domains/{}/suppressions".format(domain["id"])
    deadline = time.monotonic() + SUPPRESSION_WAIT
    while True:
        suppressed = [s for s in fixture_call(transaction, "GET", collection) or []
                      if s.get("address") == address]
        if suppressed:
            break
        if time.monotonic() >= deadline:
            raise RuntimeError(
                "{} was not suppressed within {}s: the gateway's queue worker did not "
                "bounce the message sent to it".format(address, SUPPRESSION_WAIT))
        time.sleep(1)
    query = transaction["fullPath"].partition("?")[2]
    transaction["fullPath"] = collection + "/" + str(suppressed[0]["id"]) + ("?" + query if query else "")


# A DMARC aggregate report is stored only from inbound mail whose From domain
# passes DMARC aligned, addressed to postmaster@ the report's domain
# (internal/pipeline/filters/dmarc_reports_received.go). The gateway hands
# inbound mail to deliverMessage with the client's address, so a report is
# delivered as one from the reporter's mail host would be: SPF passes for the
# reporter from its one permitted address, aligned with the From domain, which
# publishes a DMARC record. The testbed's DNS serves both records
# (tests/contract/dnsmasq.conf), so no third party's DNS decides the result.
OWN_DMARC_REPORTER = "dmarc-sender.test"
OWN_DMARC_CLIENT = ("192.0.2.25", "mail.dmarc-sender.test")
OWN_DMARC_REPORT_ID = "contract-own-report"


def a_dmarc_report_message(domain, stamp):
    """A whole aggregate report mail for domain, as deliverMessage takes it."""
    sender = "noreply-dmarc-support@" + OWN_DMARC_REPORTER
    report = (
        '<?xml version="1.0" encoding="UTF-8"?>\n<feedback>'
        "<report_metadata><org_name>Contract</org_name><email>{sender}</email>"
        "<report_id>{report_id}</report_id>"
        "<date_range><begin>1793577600</begin><end>1793663999</end></date_range>"
        "</report_metadata><policy_published><domain>{domain}</domain>"
        "<adkim>r</adkim><aspf>r</aspf><p>none</p></policy_published>"
        "<record><row><source_ip>192.0.2.1</source_ip><count>12</count>"
        "<policy_evaluated><disposition>none</disposition><dkim>pass</dkim>"
        "<spf>fail</spf></policy_evaluated></row>"
        "<identifiers><header_from>{domain}</header_from></identifiers>"
        "<auth_results><dkim><domain>{domain}</domain><selector>mail</selector>"
        "<result>pass</result></dkim><spf><domain>{domain}</domain>"
        "<result>fail</result></spf></auth_results></record></feedback>\n"
    ).format(sender=sender, report_id=OWN_DMARC_REPORT_ID, domain=domain)
    attachment = base64.encodebytes(report.encode()).decode().replace("\n", "\r\n")
    postmaster = "postmaster@" + domain
    message = (
        f"From: {sender}\r\nTo: {postmaster}\r\n"
        f"Subject: Report domain: {domain}\r\n"
        f"Date: {formatdate(localtime=False)}\r\n"
        f"Message-ID: <contract-dmarc-{stamp}@{OWN_DMARC_REPORTER}>\r\n"
        "MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=report\r\n\r\n"
        "--report\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nA report.\r\n"
        "--report\r\nContent-Type: text/xml\r\n"
        "Content-Disposition: attachment; filename=report.xml\r\n"
        "Content-Transfer-Encoding: base64\r\n\r\n"
        f"{attachment}--report--\r\n"
    )
    return {
        "address": postmaster,
        "sender": sender,
        "recipients_to": [postmaster],
        "subject": f"Report domain: {domain}",
        "client_ip": OWN_DMARC_CLIENT[0],
        "helo_name": OWN_DMARC_CLIENT[1],
        "raw_message": base64.b64encode(message.encode()).decode(),
    }


@hooks.before("getDmarcReport")
def read_a_dmarc_report_delivered_after_the_reset(transaction):
    # getDmarcReport, moved to a clean start: each exchange, refusals
    # included, resets and seeds first, and refusals keep their ids. The 200
    # reads a report delivered after the reset (a_dmarc_report_of_its_own).
    # No link reaches it. Registered ahead of the fixture report's hook, whose
    # report the reset removed. A run with no reset changes nothing here.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    if " > 200 > " not in transaction.get("name", ""):
        return
    report = a_dmarc_report_of_its_own(transaction)
    query = transaction["fullPath"].partition("?")[2]
    transaction["fullPath"] = "/api/v1/admin/dmarc-reports/{}".format(report["id"]) + (
        "?" + query if query else "")


@hooks.before("listDmarcReports")
def list_a_dmarc_report_delivered_after_the_reset(transaction):
    # listDmarcReports, moved to a clean start: each exchange, refusals
    # included, resets and seeds first, and refusals keep their queries. A
    # 200 gets a report delivered after the reset (a_dmarc_report_of_its_own)
    # and notes it for the_listing_holds_what_was_made, by its reporter and
    # report id. A reporter filter names that report's reporter: only the
    # value changes, as a path's id would. A domain filter for any other
    # domain (the example's 4294967295 is none) lists none of it by design,
    # so its hook says so. No link reaches it. A run with no reset changes
    # nothing here.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    if " > 200 > " not in transaction.get("name", ""):
        return
    report = a_dmarc_report_of_its_own(transaction)
    path, _, query = transaction["fullPath"].partition("?")
    params = [
        (key, report["reporter"] if key == "reporter" else value)
        for key, value in urllib.parse.parse_qsl(query, keep_blank_values=True)
    ]
    if params:
        transaction["fullPath"] = path + "?" + urllib.parse.urlencode(params)
    if any(key == "domain_id" and value != str(report["domain_id"]) for key, value in params):
        made_for_the_listing(transaction, "nothing for another domain", None)
        return
    made_for_the_listing(transaction, "DMARC report", {
        "reporter": report["reporter"],
        "report_id": report["report_id"],
    })


def a_dmarc_report_of_its_own(transaction):
    """A DMARC aggregate report delivered after this exchange's reset, as
    stored: the fixture domain gets an inbound pipeline of its own that checks
    SPF, DKIM and DMARC and does not greylist (the default one defers a first
    delivery), and the report mail is delivered to its postmaster through
    deliverMessage."""
    domain = next(
        (d for d in fixture_call(transaction, "GET", "/api/v1/admin/domains") or []
         if d.get("name") == FIXTURE_PLAN["domain"]),
        None,
    )
    if domain is None:
        raise RuntimeError("the seed hosts no {}".format(FIXTURE_PLAN["domain"]))
    fixture_call(transaction, "POST", "/api/v1/admin/pipelines", {
        "domain_id": domain["id"],
        "direction": "inbound",
        "active": True,
        "filters": [
            {"name": "spf_check", "type": "action", "enabled": True},
            {"name": "dkim_verify", "type": "transform", "enabled": True},
            {"name": "dmarc_check", "type": "action", "enabled": True},
        ],
    })
    # The gateway's route takes no credential: never a user's session.
    context = ssl.create_default_context(cafile=f"{CONTRACT_MTLS}/internal-ca.crt")
    context.load_cert_chain(
        f"{CONTRACT_MTLS}/internal-smtp-delivery.crt", f"{CONTRACT_MTLS}/internal-smtp-delivery.key"
    )
    delivery = urllib.request.Request(
        INTERNAL_ORIGIN + "/api/v1/messages/deliver",
        data=json.dumps(a_dmarc_report_message(domain["name"], time.time_ns())).encode(),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(delivery, timeout=30, context=context) as response:
        if response.status != 201:
            raise RuntimeError("the report mail was not delivered: {}".format(response.status))
    reports = [
        r for r in fixture_call(
            transaction, "GET",
            "/api/v1/admin/dmarc-reports?reporter=" + OWN_DMARC_REPORTER) or []
        if r.get("report_id") == OWN_DMARC_REPORT_ID
    ]
    if len(reports) != 1:
        raise RuntimeError(
            "the report delivered from {} was not stored: its DMARC did not pass "
            "aligned".format(OWN_DMARC_REPORTER))
    return reports[0]


# The test data operations (#1334). resetTestData's and restoreTestSnapshot's
# successes wipe the data, so their examples require disposable_data, which
# only this run provides: each exchange resets and seeds first, so the wipe
# takes nothing another exchange needs.
RESET_RESTORE_SNAPSHOT = "the_snapshot_restored"
RESET_LEFTOVER = "the_domain_the_reset_removes"
TEST_DATA_OPERATIONS = (
    "resetTestData",
    "seedTestData",
    "snapshotTestData",
    "restoreTestSnapshot",
)


def snapshot_identities(snapshot):
    """What a snapshot holds, by what names each row rather than by id: the
    domains, mailboxes and aliases, and how many of the other rows."""
    snapshot = snapshot if isinstance(snapshot, dict) else {}

    def rows(key):
        found = snapshot.get(key)
        return found if isinstance(found, list) else []

    return {
        "domains": sorted(row.get("name") for row in rows("domains")),
        "mailboxes": sorted(row.get("address") for row in rows("mailboxes")),
        "aliases": sorted(row.get("source_address") for row in rows("aliases")),
        **{key: len(rows(key)) for key in ("messages", "queue", "pipelines", "bans")},
    }


@hooks.before("resetTestData")
@hooks.before("seedTestData")
@hooks.before("snapshotTestData")
@hooks.before("restoreTestSnapshot")
def the_test_data_operations_after_a_reset(transaction):
    # Each exchange, refusals included, resets and seeds first; a reset's own
    # exchange just runs after it. The successes:
    # - resetTestData's 200 gets a domain made after the reset, which its
    #   after hook requires the reset to have removed.
    # - restoreTestSnapshot's 200 restores a snapshot taken through the API
    #   after the reset (snapshotTestData), and its after hook requires the
    #   data to be what that snapshot held. The example's empty body would
    #   only clear the data.
    # - seedTestData's and snapshotTestData's 200 need nothing made.
    # A run with no reset changes nothing here, and sends none of the wiping
    # exchanges: their examples require disposable_data.
    hooks.stash.pop(RESET_RESTORE_SNAPSHOT, None)
    hooks.stash.pop(RESET_LEFTOVER, None)
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    name = transaction.get("name", "")
    if " > 200 > " not in name:
        return
    path = transaction["fullPath"].partition("?")[0]
    if path.endswith("/admin/test/reset"):
        a_domain_of_its_own(transaction)
        hooks.stash[RESET_LEFTOVER] = {"name": name, "domain": OWN_DOMAIN}
    elif path.endswith("/admin/test/snapshot/restore"):
        snapshot = fixture_call(transaction, "POST", "/api/v1/admin/test/snapshot")
        if not isinstance(snapshot, dict) or not snapshot.get("domains"):
            raise RuntimeError("the snapshot taken after the reset holds no domains")
        hooks.set_json(transaction, snapshot)
        hooks.stash[RESET_RESTORE_SNAPSHOT] = {"name": name, "held": snapshot_identities(snapshot)}


@hooks.after("resetTestData")
@hooks.after("restoreTestSnapshot")
def the_wipe_did_what_it_says(transaction):
    # A reset or a restore answers the same status whatever it did, so the
    # status and the schema alone pass one that changed nothing (or, for the
    # restore, cleared everything). After a 200 the data must show it:
    # the reset removed the domain made before it and seeded restmail.test
    # again; the restore put back what its snapshot held.
    reset = hooks.stash.pop(RESET_LEFTOVER, None)
    restore = hooks.stash.pop(RESET_RESTORE_SNAPSHOT, None)
    real = transaction.get("real") or {}
    name = transaction.get("name", "")
    if (
        not asked_for_a_clean_start()
        or " > 200 > " not in name
        or str(real.get("statusCode")) != "200"  # The status check fails it.
    ):
        return
    made = reset if transaction["fullPath"].partition("?")[0].endswith("/reset") else restore
    if made is None or made["name"] != name:
        transaction["fail"] = "nothing was made for the wipe to be checked against (#1334)"
        return
    now = snapshot_identities(fixture_call(transaction, "POST", "/api/v1/admin/test/snapshot"))
    if made is reset:
        if made["domain"] in now["domains"]:
            transaction["fail"] = f"the reset left {made['domain']}, made before it, in place"
        elif "restmail.test" not in now["domains"]:
            transaction["fail"] = "the reset did not seed restmail.test again"
        return
    if now != made["held"]:
        differ = ", ".join(key for key in sorted(now) if now[key] != made["held"][key])
        transaction["fail"] = f"the restore did not put back what its snapshot held ({differ})"


def to_the_invitation_made_for_it(transaction):
    """respondToCalendar's accepted (200) and foreign_sender (403) need a
    received invitation (x-requires state calendar_invitation, which the
    own-data run provides): the seeded admin@restmail.test sends itself one
    after the reset, and the reply goes to the message it was delivered in.
    Only the message id in the path changes; foreign_sender's from stays an
    address the session cannot send as, so it is refused after the invitation
    is found."""
    fixture_call(transaction, "POST", "/api/v1/messages/send", OWN_INVITATION)
    account = mailbox_id(origin_of(transaction), ADMIN)
    rows = fixture_call(transaction, "GET", f"/api/v1/accounts/{account:d}/calendar-events") or []
    found = [row.get("message_id") for row in rows if row.get("uid") == OWN_INVITATION_UID]
    if not found or not found[0]:
        raise RuntimeError("the invitation made for the reply was not delivered: no message holds it")
    path, separator, query = transaction["fullPath"].partition("?")
    parts = path.split("/")
    parts[4] = str(found[0])
    transaction["fullPath"] = "/".join(parts) + separator + query


@hooks.before("domainDNSCheck")
@hooks.before("getDomainRelay")
@hooks.before("listSuppressions")
@hooks.before("stopDomainOutbound")
@hooks.before("restartDomainOutbound")
def a_domain_setting_of_its_own(transaction):
    # The domain operations that follow createDomain's link, moved to a clean
    # start: each exchange, refusals included, resets and seeds first. The
    # documented success is sent to a domain of its own, since the domain the
    # link names is gone after the reset. Refusals keep their ids: 0,
    # not-an-integer and missing's 2147483647 need nothing to exist, and the
    # relay's 422s name the seeded restmail.test, which the reset keeps as id 1.
    # A run with no reset changes nothing here.
    # entrypoint.py registers this for setDomainRelay as well: this file holds that
    # operation to having no hook of its own that could change a probe's body.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    parts = transaction["fullPath"].partition("?")[0].split("/")
    if (
        " > 200" in transaction.get("name", "")
        and len(parts) > 5
        and positive_id(parts[5])
        and parts[5] != "2147483647"
    ):
        to_domain(transaction, a_domain_of_its_own(transaction))


def a_signing_key(transaction, domain_id):
    """Install the fixture DKIM key on that domain through the admin API."""
    with (Path(__file__).resolve().parent / "fixtures" / "dkim-key.pem").open() as key:
        fixture_call(
            transaction,
            "PUT",
            f"/api/v1/admin/dkim/{domain_id:d}",
            {"selector": "contract", "private_key": key.read()},
        )


@hooks.before("setDKIMKey")
@hooks.before("deleteDKIMKey")
@hooks.before("getDKIMRotation")
@hooks.before("setDKIMRotation")
def a_dkim_domain_of_its_own(transaction):
    # The DKIM operations, moved to a clean start: each exchange, refusals
    # included, resets and seeds first. One aimed at a positive id other than
    # missing's 2147483647 (the id createDomain's or setDKIMKey's link names, which
    # the reset removed) gets a domain of its own, and a delete of the key also
    # gets the key it deletes. 0 and the missing id need nothing made. A run with
    # no reset changes nothing here.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    parts = transaction["fullPath"].partition("?")[0].split("/")
    if len(parts) < 6 or not positive_id(parts[5]) or parts[5] == "2147483647":
        return
    domain_id = a_domain_of_its_own(transaction)
    to_domain(transaction, domain_id)
    # Only the delete documents a 204.
    if " > 204" in transaction.get("name", ""):
        a_signing_key(transaction, domain_id)


@hooks.before("domainDNSCheck")
def the_stale_key_example_on_a_live_domain(transaction):
    # dkim_record_differs names stale.test, id 7, which only the in-process test
    # builds (TestDomainDNSCheckExamples proves its body). The live suite has no
    # such domain, so its valid probes would be lost to a 404. Here the exchange
    # goes to the fixture domain that "found" uses, as a 200 on the same route.
    path, separator, query = transaction.get("fullPath", "").partition("?")
    if path != "/api/v1/admin/domains/7/dns":
        return
    if str(transaction.get("expected", {}).get("statusCode")) != "200":
        return
    transaction["fullPath"] = "/api/v1/admin/domains/1/dns" + (separator + query if separator else "")


@hooks.before("createAccount")
@hooks.before("listMailboxes")
@hooks.before("getMailbox")
@hooks.before("updateMailbox")
@hooks.before("deleteMailbox")
@hooks.before("endMailboxSessions")
def a_mailbox_to_administer(transaction):
    # The mailbox administration operations, moved to a clean start: each
    # exchange, refusals included, resets and seeds first. createAccount's
    # documented create then meets no earlier probe's account, and a list reads
    # the seeded mailboxes. An exchange aimed at a positive mailbox id other
    # than missing's 2147483647 (the found example's 1 or the id createAccount's
    # link names, both gone after the reset) gets an account of its own on the
    # seeded restmail.test, the 422 included, so it is refused for its body and
    # not for a mailbox that is gone. 0, an undecodable id and the missing id
    # need nothing made. Registered before the other hooks of these
    # operations, so they see the clean start. A run with no reset changes
    # nothing here.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    path, separator, query = transaction["fullPath"].partition("?")
    parts = path.split("/")
    if len(parts) < 6 or not positive_id(parts[5]) or parts[5] == "2147483647":
        return
    parts[5] = str(an_account_of_its_own(origin_of(transaction), "contract-own-mailbox"))
    transaction["fullPath"] = "/".join(parts) + separator + query


# The documented script the account settings' hook installs where an exchange
# needs one: the coverage process's sieve hook installs the same.
A_SIEVE_SCRIPT = {"script": "keep;", "active": True}


@hooks.before("getVacation")
@hooks.before("setVacation")
@hooks.before("disableVacation")
@hooks.before("getSieveScript")
@hooks.before("putSieveScript")
@hooks.before("deleteSieveScript")
@hooks.before("validateSieveScript")
@hooks.before("getAccountSendLimits")
@hooks.before("setAccountSendLimits")
@hooks.before("getQuota")
def account_settings_of_its_own(transaction):
    # The account settings operations, moved to a clean start: each exchange,
    # refusals included, resets and seeds first. The examples name account 1,
    # which after a reset is the seeded eve@restmail.test, an account the
    # fixture admin's session does not reach (404). So an exchange aimed at a
    # positive account id other than missing's 2147483647 gets an account of its
    # own on the seeded restmail.test instead, made after the reset. That
    # includes the 422s, so each is refused for its body. 0 and the
    # missing id need nothing made. What an exchange reads or removes is made
    # on that account: the script of a Sieve read and delete, and the vacation
    # reply a disable turns off. The Sieve install's 201 needs none, and its
    # 200 (a replace) is not sent in this run, having no example of its own.
    # Registered before the other hooks of these operations, so they see the
    # clean start. entrypoint.py registers this for getQuietHours and
    # setQuietHours too, after their own hook, which fixes their time zone and
    # leaves the examples' 1 to this one (it finds a live account only in a run
    # with no reset). A run with no reset changes nothing here.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    path, separator, query = transaction["fullPath"].partition("?")
    parts = path.split("/")
    if len(parts) < 6 or not positive_id(parts[4]) or parts[4] == "2147483647":
        return
    account = an_account_of_its_own(origin_of(transaction), "contract-own-settings")
    parts[4] = str(account)
    transaction["fullPath"] = "/".join(parts) + separator + query
    setting = "/".join(parts[5:])
    method = transaction["request"].get("method", "")
    status = str(transaction.get("expected", {}).get("statusCode", ""))
    settings = f"/api/v1/accounts/{account:d}/"
    if setting == "sieve" and (method, status) in (("GET", "200"), ("DELETE", "200")):
        fixture_call(transaction, "PUT", settings + "sieve", A_SIEVE_SCRIPT)
    elif setting == "vacation" and (method, status) == ("DELETE", "204"):
        fixture_call(
            transaction,
            "PUT",
            settings + "vacation",
            {"enabled": True, "subject": "Out of office", "body": "I will reply when I return."},
        )


@hooks.before("listQueue")
@hooks.before("getQueueStats")
@hooks.before("getQueueEntry")
@hooks.before("retryQueueEntry")
@hooks.before("bounceQueueEntry")
@hooks.before("deleteQueueEntry")
@hooks.before("bulkRetryQueue")
@hooks.before("bulkBounceQueue")
@hooks.before("bulkDeleteQueue")
@hooks.before("listHeldMessages")
@hooks.before("releaseHeldMessage")
@hooks.before("listScheduledMessages")
@hooks.before("cancelScheduledMessage")
def queued_and_held_mail_of_its_own(transaction):
    # The delivery queue, held and scheduled mail operations, moved to a clean
    # start: each exchange, refusals included, resets and seeds first, and
    # what it needs is made after that through the API.
    # - A queue entry's success (and the list, whose links lead to one) gets an
    #   entry of its own, a send to a reserved .invalid address. retry's 400
    #   (already_bounced) gets one bounced first, so it is refused for that and
    #   not for an entry that is gone.
    # - A bulk action's empty filter selects every entry, so it gets one to act
    #   on. The documented filter selects nothing by design and needs nothing.
    # - listHeldMessages' 200 (none_held) names an account that holds nothing,
    #   and the reset removed 1: it gets an account of its own.
    # - The scheduled messages' own hooks schedule theirs through the API.
    # 0, an undecodable id and missing's 2147483647 keep theirs, and so do the
    # other refusals. Registered before the other hooks of these operations,
    # so they see the clean start. A run with no reset changes nothing here.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    path, separator, query = transaction["fullPath"].partition("?")
    parts = path.split("/")
    status = transaction.get("expected", {}).get("statusCode")
    if path == "/api/v1/admin/queue" and status == "200":
        # Listed by its recipient, as the run without a reset lists the
        # fixture's: the list's links then lead to this entry, and a
        # delivery attempt cannot change which entries it shows.
        recipient = a_queue_entry_of_its_own(transaction)["recipient"]
        transaction["fullPath"] = path + "?" + urllib.parse.urlencode({"recipient": recipient})
    elif path.startswith("/api/v1/admin/queue/bulk-") and status == "200":
        if hooks.get_json(transaction) == {"filter": {}}:
            # Every entry it makes is pending, which all three actions act
            # on: the after hook holds the answer to them (#1772).
            made = a_queue_entry_of_its_own(transaction)
            hooks.stash["bulk_queue_made"] = {
                "name": transaction.get("name", ""),
                "ids": [made["id"]],
            }
    elif path.startswith("/api/v1/admin/queue/") and len(parts) > 5 and positive_id(parts[5]):
        if parts[5] == "2147483647" or status not in ("200", "204", "400"):
            return
        entry = a_queue_entry_of_its_own(transaction)["id"]
        if status == "400":
            fixture_call(transaction, "POST", f"/api/v1/admin/queue/{entry:d}/bounce")
        parts[5] = str(entry)
        transaction["fullPath"] = "/".join(parts) + separator + query
    elif path.endswith("/held-messages") and status == "200":
        if not positive_id(parts[4]) or parts[4] == "2147483647":
            return
        parts[4] = str(an_account_of_its_own(origin_of(transaction), "contract-own-held"))
        transaction["fullPath"] = "/".join(parts) + separator + query


# What each bulk action does to an entry it acts on, read back through the
# API: retry and bounce leave the entry with a new status, delete removes it.
BULK_QUEUE_OUTCOME = {
    "/api/v1/admin/queue/bulk-retry": "pending",
    "/api/v1/admin/queue/bulk-bounce": "bounced",
    "/api/v1/admin/queue/bulk-delete": None,
}


@hooks.after("bulkRetryQueue")
@hooks.after("bulkBounceQueue")
@hooks.after("bulkDeleteQueue")
def the_bulk_action_changed_the_entries_made(transaction):
    # A bulk action's 200 says only how many entries it acted on, and
    # affected: 0 has the same shape as a real change (#1772). After a reset
    # the empty filter's entries are the ones queued_and_held_mail_of_its_own
    # made, all eligible, so its answer must count exactly those, and each must
    # read back as the action left it. Anything else fails the exchange. The
    # documented filter matches nothing by design and made nothing to check.
    # An empty filter with nothing made for it fails too: its affected: 0
    # would prove only the shape again.
    made = hooks.stash.pop("bulk_queue_made", None)
    if made is not None and made["name"] != transaction.get("name", ""):
        made = None
    real = transaction.get("real") or {}
    if (
        not asked_for_a_clean_start()
        or hooks.get_json(transaction) != {"filter": {}}
        or str(real.get("statusCode")) != "200"  # The status check fails it.
    ):
        return
    if made is None or not made["ids"]:
        transaction["fail"] = (
            "the bulk action's empty filter had no queue entry made for it to act on"
        )
        return
    problems = []
    data = hooks.get_json(transaction, "real").get("data")
    affected = data.get("affected") if isinstance(data, dict) else None
    if affected != len(made["ids"]):
        problems.append(
            f"affected {affected!r}, but the hook made {len(made['ids']):d} eligible "
            f"entr{'y' if len(made['ids']) == 1 else 'ies'}"
        )
    outcome = BULK_QUEUE_OUTCOME[transaction["fullPath"].partition("?")[0]]
    for entry in made["ids"]:
        found = fixture_call(transaction, "GET", f"/api/v1/admin/queue/{entry:d}", expect=(404,))
        if outcome is None and found is not None:
            problems.append(f"entry {entry:d} is still queued ({found.get('status')})")
        elif outcome is not None and (found or {}).get("status") != outcome:
            state = "gone" if found is None else found.get("status")
            problems.append(f"entry {entry:d} is {state}, not {outcome}")
    if problems:
        transaction["fail"] = "the bulk action did not change the entries made for it: " + "; ".join(problems)


def a_queue_entry_of_its_own(transaction):
    """A queue entry made after the reset: the fixture admin sends to a
    reserved .invalid address, which the server queues for the internet. Its
    entry is looked up by that address, which is named by the time it is made."""
    recipient = f"contract-own-queue-{time.time_ns():d}@queue.invalid"
    fixture_call(
        transaction,
        "POST",
        "/api/v1/messages/send",
        {
            "from": ADMIN,
            "to": [recipient],
            "subject": "contract queue entry",
            "body_text": "Contract fixture.",
        },
    )
    listed = fixture_call(
        transaction,
        "GET",
        "/api/v1/admin/queue?" + urllib.parse.urlencode({"recipient": recipient}),
    )
    return listed["items"][0]


def a_credential_of_its_own(transaction, parts):
    """Make the app password, API key or client secret a revoke in parts names,
    on the account in its path, with the two-factor session, and name it in
    parts. Its maker's password is the two-factor administrator's."""
    origin = origin_of(transaction)
    body = {"name": f"contract-own-credential-{time.time_ns()}", "password": mfa_admin.PASSWORD}
    if parts[5] != "credentials":
        body["permissions"] = [{"capability": "mail:read", "scope": "mailbox:" + mailbox_address(origin, parts[4])}]
    rate_limits.pace(1.1)  # making it spends the auth bucket, as the exchange does
    request = urllib.request.Request(
        origin + "/".join(parts[:6]),
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json", "Authorization": "Bearer " + mfa_admin_token(origin)},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        parts[6] = str(json.load(response)["data"]["id"])


@hooks.before("listCredentials")
@hooks.before("createAppPassword")
@hooks.before("revokeCredential")
@hooks.before("listAPIKeys")
@hooks.before("createAPIKey")
@hooks.before("updateAPIKey")
@hooks.before("revokeAPIKey")
@hooks.before("listAdminAPIKeys")
@hooks.before("listClientSecrets")
@hooks.before("createClientSecret")
@hooks.before("revokeClientSecret")
def a_credential_to_manage(transaction):
    # The credential operations, moved to a clean start: each exchange,
    # refusals included, resets and seeds first, so a list or a create meets no
    # earlier probe's credential. An exchange aimed at a positive account id
    # other than missing's 2147483647 (the example's 1 or the id a link names,
    # gone after the reset) gets an account of its own on the seeded
    # restmail.test, the 422s included, so they are refused for their body and
    # not for an account that is gone. A revoke aimed at a positive credential
    # id other than the missing one also gets the app password, API key or
    # client secret it revokes, made on that account, so the run never revokes
    # a credential it signs in with. The edit makes the key it changes already
    # (an_api_key_of_its_own_to_edit). 0, an undecodable id and the missing ids
    # keep theirs. Registered before the operations' other hooks, so they see
    # the clean start and the path it leaves. A run with no reset changes
    # nothing here.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    path, separator, query = transaction["fullPath"].partition("?")
    # /api/v1/accounts/{id}/{credentials|api-keys|client-secrets}[/{credential_id}]
    parts = path.split("/")
    if len(parts) < 6 or parts[3] != "accounts" or not positive_id(parts[4]) or parts[4] == "2147483647":
        return
    parts[4] = str(an_account_of_its_own(origin_of(transaction), "contract-own-credential"))
    if (
        transaction["request"].get("method") == "POST"
        and len(parts) == 6
        and parts[5] == "client-secrets"
        and str(transaction.get("expected", {}).get("statusCode")) == "201"
    ):
        body = hooks.get_json(transaction)
        if isinstance(body, dict) and body.get("permissions") == [
            {"capability": "pipelines:write", "scope": "domain:example.test"}
        ]:
            as_the_two_factor_admin_call(origin_of(transaction), "POST", "/api/v1/admin/grants", {
                "holder": {"kind": "account", "id": int(parts[4])},
                "role": "admin", "scope": "domain:example.test", "mode": "active",
            })
    if (
        transaction["request"].get("method") == "DELETE"
        and len(parts) == 7
        and positive_id(parts[6])
        and parts[6] != "2147483647"
    ):
        a_credential_of_its_own(transaction, parts)
    elif (
        transaction["request"].get("method") == "GET"
        and len(parts) == 6
        and parts[5] == "client-secrets"
        and str(transaction.get("expected", {}).get("statusCode")) == "200"
    ):
        credential_parts = parts + ["0"]
        a_credential_of_its_own(transaction, credential_parts)
        made_for_the_listing(transaction, "client secret", {
            "id": int(credential_parts[6]), "kind": "client_secret",
        })
    transaction["fullPath"] = "/".join(parts) + separator + query


@hooks.before("addInstallationSenderRule")
def a_duplicate_sender_rule_for_authorization(transaction):
    if not authorization_admin_success(transaction, 409):
        return
    path = transaction["fullPath"].partition("?")[0]
    if path != "/api/v1/admin/sender-rules":
        return
    body = hooks.get_json(transaction)
    if (
        not isinstance(body, dict)
        or body.get("list_type") != "block"
        or body.get("pattern") != "@blocked.example"
    ):
        return
    rules = fixture_call(transaction, "GET", path)
    if not isinstance(rules, list):
        raise RuntimeError("could not read the installation sender rules for the duplicate example")
    if any(
        rule.get("pattern") == body["pattern"] and rule.get("list_type") == body["list_type"]
        for rule in rules
    ):
        return
    fixture_call(transaction, "POST", path, body, expect=(201,))


@hooks.before("listBans")
@hooks.before("createBan")
@hooks.before("deleteBan")
@hooks.before("unbanIP")
def a_ban_to_manage(transaction):
    # The ban operations, moved to a clean start: each exchange, refusals
    # included, resets and seeds first, so a list and a create meet no earlier
    # probe's ban. The documented delete gets a ban of its own, since the one
    # createBan's link names is gone after the reset; its 404 keeps its id. The
    # create's and the unban's own hooks give them the state they document. A
    # run with no reset changes nothing here.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    path, separator, query = transaction["fullPath"].partition("?")
    parts = path.split("/")
    if (
        " > 204" not in transaction.get("name", "")
        or len(parts) != 6
        or parts[4] != "bans"
        or not positive_id(parts[5])
    ):
        return
    ban = fixture_call(
        transaction,
        "POST",
        "/api/v1/admin/bans",
        {
            "ip": "192.0.2.77",
            "reason": "Contract fixture",
            "protocol": "all",
            "duration": "1h",
            "created_by": ADMIN,
        },
    )
    parts[5] = str(ban["id"])
    transaction["fullPath"] = "/".join(parts) + separator + query


@hooks.before("getDomain")
def get_a_domain_of_its_own(transaction):
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    # missing (2147483647) and the refused id (0) need nothing to exist.
    if " > 200" in transaction.get("name", ""):
        to_domain(transaction, a_domain_of_its_own(transaction))


@hooks.before("listRoles")
@hooks.before("listCapabilities")
@hooks.before("getServerLimits")
@hooks.before("updateServerLimits")
@hooks.before("getServerCapabilities")
@hooks.before("updateServerCapabilities")
def server_settings_after_a_reset(transaction):
    # The server settings, moved to a clean start: each exchange, refusals
    # included, resets and seeds first. None needs anything made. The roles
    # and capabilities are the installation's own, which a reset keeps; the
    # limits go back to their defaults, and the capability ceiling to every
    # capability, so a change one exchange makes is gone before the next. A
    # run with no reset changes nothing here.
    if asked_for_a_clean_start():
        start_clean(transaction)


# A TLS report of its own, the receiveTlsReport example's stored report on the
# seeded restmail.test.
OWN_TLS_REPORT = {
    "organization-name": "Contract Reporter",
    "contact-info": "tlsrpt@reporter.example.com",
    "report-id": "contract-own-tls-report",
    "date-range": {
        "start-datetime": "2026-10-16T00:00:00Z",
        "end-datetime": "2026-10-16T23:59:59Z",
    },
    "policies": [
        {
            "policy": {
                "policy-type": "sts",
                "policy-string": ["version: STSv1", "mode: testing"],
                "policy-domain": "restmail.test",
                "mx-host": ["mail.restmail.test"],
            },
            "summary": {
                "total-successful-session-count": 12,
                "total-failure-session-count": 1,
            },
        }
    ],
}


@hooks.before("listTlsReports")
@hooks.before("receiveTlsReport")
@hooks.before("listAuditEntries")
@hooks.before("verifyAuditLog")
@hooks.before("getDashboardStats")
def report_and_audit_reads_after_a_reset(transaction):
    # The TLS report and audit reads, the dashboard, and the TLS report
    # intake, moved to a clean start: each exchange, refusals included, resets
    # and seeds first. Registered before the other hooks of these operations,
    # so they see the clean start. A listing of TLS reports that succeeds gets
    # one of its own, received after the reset, so it lists more than the
    # seed. The rest need nothing made: the dashboard reports what is there.
    # The DMARC report list and the delivery log move on their own, with a
    # report delivered and a message sent after their reset. The audit log is append-only and outlives
    # the reset by design (resetRetainedTables), so its reads meet what is in
    # it. The intake hosts the domains its report names in its own hook, after
    # the reset. A run with no reset changes nothing here.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    path = transaction["fullPath"].partition("?")[0]
    if path == "/api/v1/admin/tls-reports" and " > 200" in transaction.get("name", ""):
        # The reset has just refilled the intake's rate limit.
        fixture_call(transaction, "POST", "/.well-known/smtp-tlsrpt", OWN_TLS_REPORT)
        # The intake answers only how many entries it stored, so the report is
        # known in the listing by its reporter and policy domain.
        made_for_the_listing(transaction, "TLS report", {
            "reporting_org": OWN_TLS_REPORT["organization-name"],
            "policy_domain": OWN_TLS_REPORT["policies"][0]["policy"]["policy-domain"],
        })


def made_for_the_listing(transaction, what, match):
    """Note what a listing's before hook made after its reset, for
    the_listing_holds_what_was_made: a row whose fields hold every value in
    match (a dotted field reads a nested object) is the one made. match None
    says the page is not meant to list it."""
    hooks.stash["listing_made"] = {
        "name": transaction.get("name", ""),
        "what": what,
        "match": match,
    }


def row_field(row, field):
    for key in field.split("."):
        row = row.get(key) if isinstance(row, dict) else None
    return row


@hooks.after("listCustomFilters")
@hooks.after("listDomains")
@hooks.after("listAliases")
@hooks.after("listDKIMKeys")
@hooks.after("listCertificates")
@hooks.after("listCalendarEvents")
@hooks.after("listDmarcReports")
@hooks.after("getDeliveryLogs")
@hooks.after("listPipelineLogs")
@hooks.after("listClientSecrets")
@hooks.after("listTlsReports")
@hooks.after("listPipelines")
@hooks.after("listThreads")
@hooks.after("listFolders")
@hooks.after("listMessages")
@hooks.after("searchMessages")
def the_listing_holds_what_was_made(transaction):
    # A listing's 200 has the same shape with no rows as with the one its
    # before hook made after the reset, so the status and the schema alone
    # pass on an empty list (#1785). After a reset the answer must list what
    # was made for it, or the exchange fails. A documented 200 with nothing
    # made for it fails too: it would prove only the shape again. Refusals and
    # a run with no reset are left alone.
    made = hooks.stash.pop("listing_made", None)
    if made is not None and made["name"] != transaction.get("name", ""):
        made = None
    real = transaction.get("real") or {}
    if (
        not asked_for_a_clean_start()
        or " > 200" not in transaction.get("name", "")
        or str(real.get("statusCode")) != "200"  # The status check fails it.
    ):
        return
    if made is None:
        transaction["fail"] = "the listing had nothing made for it to list (#1785, #1789)"
        return
    if made["match"] is None:  # Its before hook says this page lists none of it.
        return
    rows = (hooks.get_json(transaction, "real") or {}).get("data")
    rows = rows if isinstance(rows, list) else []
    if any(
        all(row_field(row, field) == value for field, value in made["match"].items())
        for row in rows
    ):
        return
    wanted = ", ".join(f"{field} {value!r}" for field, value in made["match"].items())
    transaction["fail"] = (
        f"the listing does not hold the {made['what']} made for it ({wanted}): "
        f"it answered {len(rows):d} other row{'' if len(rows) == 1 else 's'}"
    )


@hooks.before("listAPIKeys")
@hooks.before("revokeAPIKey")
@hooks.before("updateServerLimits")
@hooks.before("confirmTwoFactor")
@hooks.before("createGrant")
@hooks.before("deleteGrant")
@hooks.before("activateGrant")
@hooks.before("deactivateGrant")
@hooks.before("makeGrantEligible")
@hooks.before("createAccount")
@hooks.before("linkAccount")
@hooks.before("unlinkAccount")
@hooks.before("revokeCredential")
@hooks.before("updateMailbox")
@hooks.before("deleteMailbox")
@hooks.before("updateDomain")
@hooks.before("deleteDomain")
@hooks.before("inviteAccount")
@hooks.before("restoreDomain")
@hooks.before("listAuditEntries")
@hooks.before("verifyAuditLog")
@hooks.before("releaseQuarantine")
@hooks.before("changeOwnPassword")
@hooks.before("disableTwoFactor")
@hooks.before("createAppPassword")
def send_as_the_two_factor_admin(transaction):
    # These take a session that signed in with a second factor, and some
    # recent proof of one, which the fixture admin has not. The run's requests
    # are sent as the two-factor administrator, a superadmin as the fixture
    # admin is, signed in for real and again before the recent-proof window
    # closes. Its own password is not the examples', so changeOwnPassword's and
    # disableTwoFactor's refusals stay refusals.
    # Confirming its authenticator again is refused (409): it is already on,
    # and the fixture admin's sign-in requirements are left alone.
    as_the_two_factor_admin(transaction)


@hooks.before("createClientSecret")
def an_app_password_from_the_two_factor_admin(transaction):
    # Making a credential also takes the maker's current password.
    if not sent_as_the_two_factor_admin(transaction):
        return
    body = hooks.get_json(transaction)
    if isinstance(body, dict) and "password" in body:
        body["password"] = mfa_admin.PASSWORD
        hooks.set_json(transaction, body)


@hooks.before("activateGrant")
@hooks.before("makeGrantEligible")
def a_fresh_second_factor_for_a_missing_grant(transaction):
    # A grant change checks a fresh second factor before it looks the grant
    # up. The missing example (an id no grant holds), sent as the two-factor
    # administrator (send_as_the_two_factor_admin runs first), gets an unused
    # factor of that administrator's, so the lookup is what refuses it (404).
    path = transaction["fullPath"].partition("?")[0]
    if not path.endswith("/2147483647/activate") and not path.endswith("/2147483647/make-eligible"):
        return
    body = hooks.get_json(transaction)
    if session_address(transaction) != mfa_admin.ADDRESS or not isinstance(body, dict):
        return
    body.pop("totp_code", None)
    body.pop("recovery_code", None)
    body.update(mfa_admin.factor())
    hooks.set_json(transaction, body)


def a_later_request_of_the_coverage_process(transaction):
    """Whether this is a request of the coverage process after a transaction's
    first. The first is its examples phase, whose key the links give to the
    operations after it, on the account they name, so it is made as in any other
    process. Call this once per request."""
    if not in_the_coverage_process():
        return False
    sends = hooks.stash.setdefault("coverage_sends", {})
    name = transaction.get("name", "")
    sends[name] = sends.get(name, 0) + 1
    return sends[name] > 1


def an_api_key_probe_as_the_probe_admin(transaction):
    """Send a coverage request of an API key operation as the probe administrator
    and say whether it was. Vertrag ends a generated probe whose body a hook
    changes, so nothing here touches the body: its password is one the probe
    administrator really holds, and fuzz.pin holds its permissions and expiry
    (probes.py). The account in the path becomes the administrator's own, so the
    key is within both grants; an id that is not one reaches the handler as sent."""
    if not with_the_fixture_admins_session(transaction):
        return False
    origin = origin_of(transaction)
    transaction["request"]["headers"]["Authorization"] = "Bearer " + probe_admin_token(origin)
    path, _, query = transaction["fullPath"].partition("?")
    parts = path.split("/")
    # /api/v1/accounts/{id}/api-keys[/{credential_id}]
    if len(parts) > 4 and positive_id(parts[4]):
        parts[4] = str(mailbox_id(origin, mfa_admin.PROBE_ADDRESS))
        transaction["fullPath"] = "/".join(parts) + ("?" + query if query else "")
    return True


@hooks.before("createAPIKey")
def an_api_key_from_the_two_factor_admin(transaction):
    # Making a key also takes the maker's current password.
    if a_later_request_of_the_coverage_process(transaction):
        an_api_key_probe_as_the_probe_admin(transaction)
        return
    if not sent_as_the_two_factor_admin(transaction):
        return
    body = hooks.get_json(transaction)
    # The body_array refusal sends a JSON array, which has no password to set,
    # and a probe of a refused password keeps it.
    if not isinstance(body, dict) or not isinstance(body.get("password"), str) or not body["password"]:
        return
    body["password"] = mfa_admin.PASSWORD
    # A key holds only what its maker and the account it is for both hold
    # (apiKeyWithinGrants). The example's account holds nothing at the
    # example's robot@example.com, but an account may read its own mailbox:
    # the key is scoped to the address of the account in the path.
    path = transaction["fullPath"].partition("?")[0]
    address = mailbox_address(origin_of(transaction), path.rstrip("/").split("/")[-2])
    if address and isinstance(body.get("permissions"), list):
        for permission in body["permissions"]:
            if not isinstance(permission, dict):
                continue
            # The generator's stand-in is no capability; a probe of a refused
            # value keeps it, and so does a probe of a refused scope.
            if permission.get("capability") == "x":
                permission["capability"] = "mail:read"
            if isinstance(permission.get("scope"), str) and permission["scope"]:
                permission["scope"] = "mailbox:" + address
    hooks.set_json(transaction, body)


def an_api_key_of_its_own_for_a_probe(transaction):
    # Only the path and the session change (an_api_key_probe_as_the_probe_admin).
    # The missing pair's id and an id that is not one keep what they name.
    if not an_api_key_probe_as_the_probe_admin(transaction):
        return
    path, _, query = transaction["fullPath"].partition("?")
    parent, _, credential = path.rstrip("/").rpartition("/")
    if not positive_id(parent.split("/")[-2]) or not positive_id(credential) or credential == "2147483647":
        return
    origin = origin_of(transaction)
    rate_limits.pace(1.1)
    request = urllib.request.Request(
        origin + parent,
        data=json.dumps({
            "name": "contract key to edit " + str(time.time_ns()),
            "password": mfa_admin.PROBE_PASSWORD,
            "permissions": [{"capability": "mail:read", "scope": "mailbox:" + mfa_admin.PROBE_ADDRESS}],
        }).encode(),
        headers={"Content-Type": "application/json", "Authorization": "Bearer " + probe_admin_token(origin)},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        key = json.load(response)["data"]
    transaction["fullPath"] = parent + "/" + str(key["id"]) + ("?" + query if query else "")


@hooks.before("updateAPIKey")
def an_api_key_of_its_own_to_edit(transaction):
    # Each edit makes the key it changes, so it needs nothing from another
    # operation's run.
    if a_later_request_of_the_coverage_process(transaction):
        an_api_key_of_its_own_for_a_probe(transaction)
        return
    if not sent_as_the_two_factor_admin(transaction):
        return
    body = hooks.get_json(transaction)
    # The body_array refusal has no password to set, a probe of an empty one
    # keeps it, and the refused password keeps the one it sends. The generator's
    # stand-in for a password is any other string.
    password = body.get("password") if isinstance(body, dict) else None
    if not isinstance(password, str) or not password or password == "not-the-password":
        return
    body["password"] = mfa_admin.PASSWORD
    origin = origin_of(transaction)
    path, _, query = transaction["fullPath"].partition("?")
    parent, _, credential = path.rstrip("/").rpartition("/")
    address = mailbox_address(origin, parent.split("/")[-2])
    if address and isinstance(body.get("permissions"), list):
        for permission in body["permissions"]:
            if not isinstance(permission, dict):
                continue
            # The generator's stand-in is no capability; a probe of a refused
            # value keeps it, and so does a probe of a refused scope.
            if permission.get("capability") == "x":
                permission["capability"] = "mail:read"
            if isinstance(permission.get("scope"), str) and permission["scope"]:
                permission["scope"] = "mailbox:" + address
    hooks.set_json(transaction, body)
    # The missing pair names a key that is not there, and keeps it.
    if credential == "2147483647" or not address:
        return
    rate_limits.pace(1.1)
    request = urllib.request.Request(
        origin + parent,
        data=json.dumps({
            "name": "contract key to edit " + str(time.time_ns()),
            "password": mfa_admin.PASSWORD,
            "permissions": [{"capability": "mail:read", "scope": "mailbox:" + address}],
        }).encode(),
        headers={"Content-Type": "application/json", "Authorization": "Bearer " + mfa_admin_token(origin)},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        key = json.load(response)["data"]
    transaction["fullPath"] = parent + "/" + str(key["id"]) + ("?" + query if query else "")


# The most rows the server's lists answer in one page. A larger limit is cut
# to it, and an absent one is 50.
LIST_PAGE = 200


def list_rows(origin, path, headers):
    """Every row of the list at path, following its pages to the last.

    The lists answer one page: a lookup that read only the first called a row
    past it absent, so its hook made the object again (409) or failed to find
    its fixture. A list without pagination is answered whole, in one page.
    """
    separator = "&" if "?" in path else "?"
    offset = 0
    while True:
        query = urllib.parse.urlencode({"limit": LIST_PAGE, "offset": offset})
        request = urllib.request.Request(
            origin + path + separator + query, headers=headers
        )
        with urllib.request.urlopen(request, timeout=10) as response:
            page = json.load(response)
        rows = page.get("data") or []
        yield from rows
        if not rows or not (page.get("pagination") or {}).get("has_more"):
            return
        offset += len(rows)


def find_in_list(origin, path, headers, predicate):
    """The first row of the list at path that predicate accepts, or None when
    no page holds one."""
    return next((row for row in list_rows(origin, path, headers) if predicate(row)), None)


def named(name):
    """A predicate for the row whose name is name, in any case."""
    return lambda row: (row.get("name") or "").lower() == name.lower()


def addressed(address):
    """A predicate for the row whose address is address, in any case."""
    return lambda row: (row.get("address") or "").lower() == address.lower()


def mailbox_address(origin, account_id):
    """The address of the account with that id, or None (a probe's id)."""
    found = find_in_list(
        origin,
        "/api/v1/admin/mailboxes",
        {"Authorization": "Bearer " + admin_token(origin)},
        lambda a: str(a.get("id")) == str(account_id),
    )
    return found["address"] if found else None


def mailbox_id(origin, address):
    """The id of the account with that address."""
    found = find_in_list(
        origin,
        "/api/v1/admin/mailboxes",
        {"Authorization": "Bearer " + admin_token(origin)},
        lambda a: a.get("address") == address,
    )
    if found is None:
        raise RuntimeError("no hosted mailbox has the address " + address)
    return found["id"]


@hooks.before("createGrant")
@hooks.before("activateGrant")
@hooks.before("deactivateGrant")
@hooks.before("makeGrantEligible")
@hooks.before("listGrants")
@hooks.before("listMyGrants")
def a_grant_operation_from_a_clean_start(transaction):
    # After a reset (the own-data phase) every exchange, refusals included,
    # starts clean. These examples need nothing made for them: their ids are
    # the refusals' (0 and 2147483647) and the lists read the seeded grants.
    # The two-factor administrator's session survives the reset.
    if asked_for_a_clean_start():
        start_clean(transaction)


@hooks.before("deleteGrant")
def delete_a_grant_made_for_it(transaction):
    # body_boundary names grant 1, which is a fixture principal's own. Delete a
    # grant made for the purpose instead: an eligible admin role on the fixture
    # domain, held by an account made without credentials. Other ids are the
    # refusal examples' and the coverage probes', and are sent as they are.
    # After a reset (the own-data phase) every exchange, refusals included,
    # starts clean, and the grant and its holder are made after it, on the
    # seeded restmail.test.
    if asked_for_a_clean_start():
        start_clean(transaction)
    path, _, query = transaction["fullPath"].partition("?")
    parent, _, grant_id = path.rpartition("/")
    # createGrant's link (#1780) names the grant its exchange made, which the
    # next reset removes, so a documented delete after a reset gets one too.
    linked_after_reset = asked_for_a_clean_start() and " > 204" in transaction.get("name", "")
    if grant_id != "1" and not linked_after_reset:
        return
    origin = origin_of(transaction)
    holder = an_account_without_credentials(origin)
    request = urllib.request.Request(
        origin + "/api/v1/admin/grants",
        data=json.dumps({
            "holder": {"kind": "account", "id": holder},
            "role": "admin",
            "scope": "domain:restmail.test",
            "mode": "eligible",
        }).encode(),
        headers={"Content-Type": "application/json", "Authorization": "Bearer " + mfa_admin_token(origin)},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        grant = json.load(response)["data"]
    transaction["fullPath"] = parent + "/" + str(grant["id"]) + ("?" + query if query else "")


@hooks.before("login")
@hooks.before("refreshToken")
@hooks.before("linkAccount")
@hooks.before("createAppPassword")
@hooks.before("openGatewaySession")
@hooks.before("enrollTwoFactor")
@hooks.before("confirmTwoFactor")
@hooks.before("recoverAccount")
@hooks.before("acceptInvitation")
@hooks.before("resetAdmin")
@hooks.before("issueClientCredentialsToken")
@hooks.before("activateGrant")
@hooks.before("makeGrantEligible")
@hooks.before("updateAPIKey")
@hooks.before("createAPIKey")
def pace_auth_requests(transaction):
    # These operations share the default 1 request/second auth bucket. Coverage
    # repeats valid requests as well as refusals, so even correct credentials
    # drain its burst. Wait for a token without changing or retrying the probe.
    # Leave a small margin for clock/scheduling differences at the boundary.
    rate_limits.pace(1.1)


@hooks.before("ownerChallenge")
@hooks.before("ownerToken")
@hooks.before("ownerStatus")
def pace_owner_requests(transaction):
    # The owner operations share a bucket of their own at login's rate (and
    # describeServer's). The topology lists no owner or status keys, so the
    # gate sends their refusals; coverage repeats them, so pace each. The
    # authorization phase registers a throwaway owner key and signs each
    # success with it (sign_the_status_with_the_runs_owner_key and below), and
    # sends the refusals needing it on a fresh challenge (_owner_refusal).
    rate_limits.pace(1.1)


@hooks.before("testFilter")
@hooks.before("listCustomFilters")
@hooks.before("validateCustomFilter")
def filter_requests_after_a_reset(transaction):
    # The rest of the filter operations, moved to a clean start: each exchange,
    # refusals included, resets and seeds first. A listing of custom filters
    # that succeeds gets one of its own, made after the reset, so it lists more
    # than nothing. The preview and the validation take only a body and need
    # nothing made. Registered ahead of every other hook of these operations. A
    # run with no reset changes nothing here.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    path = transaction["fullPath"].partition("?")[0]
    if path == "/api/v1/admin/custom-filters" and " > 200" in transaction.get("name", ""):
        made_for_the_listing(transaction, "custom filter", {"id": a_custom_filter_of_its_own(transaction)})


@hooks.before("listDomains")
@hooks.before("listAliases")
@hooks.before("listDKIMKeys")
@hooks.before("listCertificates")
@hooks.before("createCertificate")
@hooks.before("getCertificate")
@hooks.before("deleteCertificate")
def domain_administration_after_a_reset(transaction):
    # The domain administration lists and the certificate operations, moved to
    # a clean start: each exchange, refusals included, resets and seeds first.
    # A listing that succeeds gets a row of its own, made after the reset, and
    # notes it for the_listing_holds_what_was_made. A read or delete of a
    # certificate aimed at a positive id other than missing's 2147483647 (the
    # found example's 1, or the id createCertificate's link names, gone after
    # the reset) gets a certificate of its own. A create needs nothing made:
    # its documented domain_id 1 is the seeded restmail.test, which the reset
    # keeps as id 1, and its later hook supplies the certificate. Registered
    # ahead of every other hook of these operations. A run with no reset
    # changes nothing here.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    path, separator, query = transaction["fullPath"].partition("?")
    succeeds = " > 200" in transaction.get("name", "")
    if path == "/api/v1/admin/domains" and succeeds:
        made_for_the_listing(transaction, "domain", {"id": a_domain_of_its_own(transaction)})
    elif path == "/api/v1/admin/aliases" and succeeds:
        made_for_the_listing(transaction, "alias", {"id": an_alias_of_its_own(transaction)})
    elif path == "/api/v1/admin/dkim" and succeeds:
        domain_id = a_domain_of_its_own(transaction)
        a_signing_key(transaction, domain_id)
        made_for_the_listing(
            transaction, "DKIM key", {"domain_id": domain_id, "selector": "contract", "has_key": True}
        )
    elif path == "/api/v1/admin/certificates" and succeeds:
        made_for_the_listing(transaction, "certificate", {"id": a_certificate_of_its_own(transaction)})
    elif path == "/api/v1/admin/certificates" and " > 201" in transaction.get("name", ""):
        body = hooks.get_json(transaction)
        if isinstance(body, dict) and body.get("domain_id") == 1:
            body["domain_id"] = a_domain_of_its_own(transaction)
            hooks.set_json(transaction, body)
    elif path.startswith("/api/v1/admin/certificates/"):
        certificate = path.rsplit("/", 1)[-1]
        if positive_id(certificate) and certificate != "2147483647":
            transaction["fullPath"] = (
                "/api/v1/admin/certificates/" + str(a_certificate_of_its_own(transaction)) + separator + query
            )


def a_certificate_of_its_own(transaction):
    """A self-signed certificate made for this exchange, after its reset, on a
    domain of its own."""
    domain_id = a_domain_of_its_own(transaction)
    with tempfile.TemporaryDirectory() as d:
        key, cert = os.path.join(d, "tls.key"), os.path.join(d, "tls.crt")
        openssl(
            "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "30",
            "-subj", "/CN=" + OWN_DOMAIN, "-addext", "subjectAltName=DNS:" + OWN_DOMAIN,
            "-keyout", key, "-out", cert,
        )
        with open(cert) as cert_file, open(key) as key_file:
            return fixture_call(
                transaction,
                "POST",
                "/api/v1/admin/certificates",
                {
                    "domain_id": domain_id,
                    "cert_pem": cert_file.read(),
                    "key_pem": key_file.read(),
                    "auto_renew": False,
                },
            )["id"]


@hooks.before("getHealth")
@hooks.before("describeServer")
@hooks.before("apiRoot")
@hooks.before("swaggerUI")
@hooks.before("openapiSpec")
@hooks.before("openapiDocument")
@hooks.before("prometheusMetrics")
@hooks.before("mozillaAutoconfig")
@hooks.before("mozillaAutoconfigWellKnown")
@hooks.before("microsoftAutodiscover")
def public_reads_after_a_reset(transaction):
    # The public reads and the mail client discovery reads, moved to a clean
    # start: each exchange, refusals included, resets and seeds first. The
    # public reads need nothing made. The discovery reads need the address's
    # domain hosted and active: the autoconfig reads' later hook prepares it
    # after this reset, and an autodiscover that succeeds gets it here, since
    # it has no hook of its own (its request is sent as generated). Registered
    # ahead of every other hook of these operations. A run with no reset
    # changes nothing here.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    path = transaction["fullPath"].partition("?")[0]
    if path == "/autodiscover/autodiscover.xml" and " > 200 " in transaction.get("name", ""):
        an_active_domain_for_autoconfig(transaction)


@hooks.before("testPipeline")
@hooks.before("testFilter")
@hooks.before("testCustomFilter")
def pace_pipeline_previews(transaction):
    # Pipeline previews share a 2 requests/second bucket in the E2E topology.
    # Pace every probe without changing its body, credential or expected reply.
    rate_limits.pace(0.55)


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
            data=json.dumps(
                {"email": ADMIN, "password": ADMIN_PASSWORD, "client": "native"}
            ).encode(),
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
    body = hooks.get_json(transaction)
    # Replace only the fixture placeholder. Wrong passwords, missing fields
    # and coverage mutations must reach the handler unchanged.
    if isinstance(body, dict) and body.get("password") == "fixture-password":
        body["password"] = ADMIN_PASSWORD
        hooks.set_json(transaction, body)


@hooks.after("login")
def keep_the_refresh_token(transaction):
    data = hooks.get_json(transaction, "real").get("data") or {}
    if data.get("refresh_token"):
        hooks.stash["refresh_token"] = data["refresh_token"]


@hooks.before("refreshToken")
def refresh_the_sign_in(transaction):
    body = hooks.get_json(transaction)
    if isinstance(body, dict) and body.get("refresh_token") == "fixture-refresh-token":
        token = hooks.stash.get("refresh_token", "")
        # The authorization run replays the examples, then sends this request
        # once per principal without after hooks, so the rotated token is not
        # kept and a second send would reuse a spent one. Spend each token once.
        spent = hooks.stash.setdefault("spent_refresh_tokens", [])
        if token and token in spent:
            token = fresh_native_refresh_token(origin_of(transaction))
            hooks.stash["refresh_token"] = token
        if token:
            spent.append(token)
        body["refresh_token"] = token
        hooks.set_json(transaction, body)
    # A schema-valid body without a native token is the browser channel.
    # Supply its cookie only for success probes; named missing/invalid-token
    # refusals must still reach the handler without a credential. Coverage
    # runs no after hooks, so each browser probe needs its own unspent token.
    name = transaction.get("name", "")
    # The runner gives before hooks the source transaction's name, including
    # for coverage, so the 200 response identifies the success channel here.
    if (
        " > 200 > " not in name
        or not isinstance(body, dict)
        or body.get("refresh_token", "") != ""
    ):
        return
    headers = transaction["request"].get("headers", {})
    if any(key.lower() == "cookie" for key in headers):
        return  # Keep an explicitly supplied caller credential intact.
    cookie = fresh_browser_refresh_cookie(origin_of(transaction))
    headers["Cookie"] = cookie
    transaction["request"]["headers"] = headers


def origin_of(transaction):
    return "{}//{}:{}".format(
        transaction["protocol"],
        transaction["host"],
        transaction["port"],
    )


def fresh_native_refresh_token(origin):
    """Sign the fixture admin in again for an unspent native refresh token."""
    pace_auth_requests(None)
    request = urllib.request.Request(
        origin + "/api/v1/auth/login",
        data=json.dumps(
            {"email": ADMIN, "password": ADMIN_PASSWORD, "client": "native"}
        ).encode(),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        token = (json.load(response).get("data") or {}).get("refresh_token")
    if not isinstance(token, str) or not token:
        raise RuntimeError("fixture admin sign-in returned no refresh token")
    # The setup login and the actual refresh share the auth rate bucket.
    pace_auth_requests(None)
    return token


def fresh_browser_refresh_cookie(origin):
    """Prepare one browser session without consuming the native fixture token."""
    pace_auth_requests(None)
    request = urllib.request.Request(
        origin + "/api/v1/auth/login",
        data=json.dumps({"email": ADMIN, "password": ADMIN_PASSWORD}).encode(),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        if response.status != 200:
            raise RuntimeError(
                f"fixture browser sign-in failed: HTTP {response.status:d}"
            )
        cookies = SimpleCookie()
        for header in response.headers.get_all("Set-Cookie", []):
            cookies.load(header)
        token = cookies.get("restmail_refresh")
        if token is None or not token.value:
            raise RuntimeError("fixture browser sign-in returned no refresh cookie")
        cookie = "restmail_refresh=" + token.coded_value
    # The setup login and the actual refresh share the auth rate bucket.
    pace_auth_requests(None)
    return cookie


@hooks.after("refreshToken")
def keep_the_refreshed_token(transaction):
    data = hooks.get_json(transaction, "real").get("data") or {}
    if data.get("refresh_token"):
        hooks.stash["refresh_token"] = data["refresh_token"]


def a_well_formed_app_password_request(transaction, body):
    """Whether a coverage request breaks no rule of the schema. A probe that does
    must reach the handler as drawn: its refusal is what it tests, and Vertrag
    would end it if the password were added.

    One that breaks none still gets the password, and is ended, because the
    description lets it leave the password out (required: [name]) while the
    handler answers 403 reauth_required, which Vertrag would call a disagreement.
    gate.py counts each of those (lost-probes-baseline.txt)."""
    path = transaction.get("fullPath", "").partition("?")[0].split("/")
    return (
        len(path) > 4
        and positive_id(path[4])
        and isinstance(body.get("name"), str)
        and isinstance(body.get("password", ""), str)
        and set(body) <= {"name", "password", "expires_at"}
    )


@hooks.before("createAppPassword")
def confirm_with_the_admin_password(transaction):
    body = hooks.get_json(transaction)
    # The body_array refusal sends a JSON array, which has no password to set.
    if not isinstance(body, dict):
        return
    if in_the_coverage_process() and not a_well_formed_app_password_request(transaction, body):
        return
    # The maker's own current password: the two-factor administrator's when
    # the request is sent as it (send_as_the_two_factor_admin runs first).
    if session_address(transaction) == mfa_admin.ADDRESS:
        body["password"] = mfa_admin.PASSWORD
    else:
        body["password"] = ADMIN_PASSWORD
    body.pop("expires_at", None)
    hooks.set_json(transaction, body)


@hooks.before("linkAccount")
def no_link_from_a_password_only_session(transaction):
    # Linking takes a session that signed in with a second factor (#1332). A
    # password-only session is refused 403 two_factor_required: the
    # description's 403 says so, its two_factor_required example shows it, and
    # two_factor_refusal_examples_test.go proves it in-process. The
    # authorization phase's ordinary principal signs in with a password alone,
    # and its flat roles (none, where linkAccount names no capability) make the
    # phase expect the 201 from it, which the description does not give it
    # (#1942). Its success cell is skipped, so it is reported unsent rather
    # than judged against that. The fixture admin's requests are sent as the
    # two-factor administrator (send_as_the_two_factor_admin runs first), so
    # the examples, the coverage probes and the admin cell are not skipped.
    claims = session_claims(transaction)
    if claims is None or " > 201 > " not in transaction.get("name", ""):
        return
    if claims.get("mfa") or "otp" in (claims.get("amr") or []):
        return
    transaction["skip"] = True


@hooks.before("linkAccount")
def link_an_account_linked_already(transaction):
    # already_linked (existing_link) proves an account the caller holds a link
    # to: the two-factor administrator's request names one linked to it just
    # before, never the seeded eve. It runs before link_a_seeded_account, which
    # then leaves the body alone: it names none of that hook's fixtures.
    if str(transaction.get("expected", {}).get("statusCode", "")) != "409":
        return
    if session_address(transaction) != mfa_admin.ADDRESS:
        return
    body = hooks.get_json(transaction)
    if not isinstance(body, dict):
        return
    address, _ = a_throwaway_link(origin_of(transaction))
    body.update({"address": address, "password": LINKED_PASSWORD})
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
    # A coverage probe named for the 201 carries a schema-valid address and
    # password that no account has; the fixture's credentials make it the link
    # it is named for. Missing, null and empty credentials stay as sent.
    if (
        " > 201 > " in transaction.get("name", "")
        and all(
            isinstance(body.get(field), str) and body[field]
            for field in ("address", "password")
        )
        and body["address"] not in ("eve@restmail.test", "someone@example.com")
    ):
        body["address"], body["password"] = "someone@example.com", "x"
        hooks.set_json(transaction, body)
    # After a reset nothing is linked: the reset put the seeded account back
    # unlinked, so there is no earlier link to undo.
    if asked_for_a_clean_start():
        return
    # Each successful link consumes the unlinked state. Restore only that
    # state before a fixture's valid proof; do not alter generated body bytes,
    # malformed values, wrong passwords, or the transaction's principal.
    passwords = {"eve@restmail.test": LINKABLE_PASSWORD, "someone@example.com": "x"}
    address = body.get("address")
    if (
        not isinstance(address, str)
        or address not in passwords
        or body.get("password") != passwords[address]
    ):
        return
    headers = transaction["request"].get("headers") or {}
    if not any(
        key.lower() == "authorization" and value for key, value in headers.items()
    ):
        return
    origin = "{}//{}:{}".format(
        transaction["protocol"],
        transaction["host"],
        transaction["port"],
    )
    if address == "someone@example.com":
        ready = hooks.stash.get("link_fixture_prepared", {})
        if origin not in ready:
            # createAccount probes need this address absent; linkAccount probes
            # need it present with their generated password. Set up the latter
            # only now, after the preceding operation's probes have finished.
            subprocess.run(
                ["chore", "test:contract-link-fixture"],
                check=True,
                capture_output=True,
                timeout=20,
            )
            ready[origin] = True
            hooks.stash["link_fixture_prepared"] = ready
    ids = hooks.stash.get("link_fixture_ids", {})
    key = origin + "\n" + address
    if key not in ids:
        account = find_in_list(
            origin,
            "/api/v1/admin/mailboxes",
            {"Authorization": "Bearer " + admin_token(origin)},
            lambda a: a.get("address") == address,
        )
        ids[key] = account["id"] if account else None
        if ids[key] is None:
            raise RuntimeError("contract account-link fixture is missing")
        hooks.stash["link_fixture_ids"] = ids
    request = urllib.request.Request(
        origin + "/api/v1/accounts/" + str(ids[key]), headers=headers, method="DELETE"
    )
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            if response.status != 204:
                raise RuntimeError("could not clear the preceding contract link")
    except urllib.error.HTTPError as error:
        # The first proof has no preceding grant, and neither has a caller the
        # route turns away for its credential: the link it is about to send gets
        # the same answer, which is the one the authorization phase wants to see.
        if error.code != 404 and not turned_away_for_its_credential(error, headers):
            raise


def turned_away_for_its_credential(error, headers):
    """Whether a refused unlink says the caller could never have linked: an app
    password's Basic credential is not a session (401), and a password-only
    session has no second factor (403 two_factor_required). Anything else, such
    as a session that has expired, is a failed set-up and stays an error."""
    if error.code == 401:
        return any(
            key.lower() == "authorization" and value.startswith("Basic ")
            for key, value in headers.items()
        )
    if error.code != 403:
        return False
    try:
        return json.load(error)["error"]["code"] == "two_factor_required"
    except (ValueError, KeyError, TypeError):
        return False


@hooks.after("createDomain")
def keep_the_created_domain(transaction):
    data = hooks.get_json(transaction, "real").get("data") or {}
    # The refused create (name_missing, 422) runs straight after the one that
    # made the domain, and created nothing to keep.
    if not data.get("id"):
        return
    hooks.stash["domain_id"] = data.get("id")
    hooks.stash["domain"] = data.get("name")


@hooks.before("restoreDomain")
def an_archived_domain_to_restore(transaction):
    path, separator, query = transaction["fullPath"].partition("?")
    matched = re.fullmatch(r"/api/v1/admin/domains/([1-9][0-9]*)/restore", path)
    if not matched or not any(f" > {status}" in transaction.get("name", "") for status in (200, 403, 409)):
        return
    if asked_for_a_clean_start():
        start_clean(transaction)
    name = f"contract-restore-{time.time_ns()}.test"
    domain = fixture_call(transaction, "POST", "/api/v1/admin/domains", {"name": name})
    as_the_two_factor_admin_call(origin_of(transaction), "DELETE", f"/api/v1/admin/domains/{domain['id']:d}")
    if " > 409" in transaction.get("name", ""):
        fixture_call(transaction, "POST", "/api/v1/admin/domains", {"name": name})
    transaction["fullPath"] = f"/api/v1/admin/domains/{domain['id']:d}/restore" + (separator + query if separator else "")


@hooks.before("deleteDomain")
def delete_only_the_created_domain(transaction):
    # Coverage's in-range probe names domain 1, the fixture admin's own. The
    # server refuses it while it has accounts (409, #388), which a valid probe
    # counts as a finding, and before that refusal the probe deleted it and
    # every later send failed. Malformed ids are sent: they test the 400.
    # Moved to a clean start: every exchange, refusals included, resets first.
    # The documented delete makes the domain it deletes below, and has_accounts
    # names the seeded fixture domain, which the reset keeps.
    if asked_for_a_clean_start():
        start_clean(transaction)
    path, _, query = transaction["fullPath"].partition("?")
    domains, domain_id = path.rstrip("/").rsplit("/", 1)
    if not domain_id.isdecimal() or int(domain_id) < 1:
        return
    # The documented delete gets a domain of its own. Its link fills the
    # created domain, which the deletes of what hangs off it (DKIM, sender
    # rules, pipelines) run after and still need. A coverage probe in range
    # gets one as well, and leaves the created domain alone too.
    if " > 204" in transaction.get("name", ""):
        domain = fixture_call(
            transaction,
            "POST",
            "/api/v1/admin/domains",
            {
                "name": f"contract-delete-{time.time_ns()}.test",
                "server_type": "traditional",
            },
        )
        transaction["fullPath"] = f"{domains}/{domain['id']:d}" + (
            "?" + query if query else ""
        )
        return
    # missing names an id no domain holds: sent as it is, refused 404.
    if domain_id == "2147483647":
        return
    # has_accounts names a domain with accounts: the fixture domain is one,
    # which the server refuses to delete (409), so nothing is removed.
    if domain_id == "6" and " > 409" in transaction.get("name", ""):
        origin = origin_of(transaction)
        fixture = hosted_domain_id(origin, {"Authorization": "Bearer " + admin_token(origin)}, FIXTURE_PLAN["domain"])
        transaction["fullPath"] = f"{domains}/{fixture:d}" + ("?" + query if query else "")
        return
    if int(domain_id) != hooks.stash.get("domain_id"):
        transaction["skip"] = True


def on_the_created_domain(transaction, **fields):
    body = hooks.get_json(transaction)
    # Before the run has created its domain (operations run in path order, and
    # /admin/custom-filters comes before /admin/domains), the pair's own domain
    # stands: a null would make a valid create a validation refusal.
    if hooks.stash.get("domain_id") is not None:
        body["domain_id"] = hooks.stash["domain_id"]
    body.update(fields)
    hooks.set_json(transaction, body)


@hooks.before("createPipeline")
def a_pipeline_on_the_created_domain(transaction):
    if asked_for_a_clean_start():
        start_clean(transaction)
    body = hooks.get_json(transaction)
    # The undecodable creates (body_array and null_body) are sent as documented,
    # with no object to put the domain in.
    if not isinstance(body, dict):
        return
    fields = {}
    # A created pipeline stays inactive. The wrong-typed create
    # (active_wrong_type, 422) keeps the "yes" it is named for.
    if isinstance(body.get("active", False), bool):
        fields["active"] = False
    # The refused creates (direction_missing and filters_missing, 422) are
    # sent as documented, each without the field it is named for.
    if "direction" in body:
        fields["direction"] = "inbound"
    if "filters" in body:
        fields["filters"] = [{"name": "header_validate", "config": {}}]
    if asked_for_a_clean_start():
        # Its own domain, made after the reset, as the run's domain is in the
        # primary run.
        fields["domain_id"] = a_domain_of_its_own(transaction)
    on_the_created_domain(transaction, **fields)


def a_pipeline_of_its_own(transaction, domain_id):
    """An inactive pipeline on that domain, made for this exchange."""
    return fixture_call(
        transaction,
        "POST",
        "/api/v1/admin/pipelines",
        {
            "domain_id": domain_id,
            "direction": "inbound",
            "filters": [{"name": "header_validate", "config": {}}],
            "active": False,
        },
    )["id"]


# A pipeline's id sits where a domain's does: /api/v1/admin/pipelines/{id}.
to_pipeline = to_domain


@hooks.before("testPipeline")
def test_the_created_pipeline(transaction):
    if asked_for_a_clean_start():
        start_clean(transaction)
    body = hooks.get_json(transaction)
    # Replace only the documented successful request's placeholder. Missing
    # IDs, invalid bodies and coverage probes must retain their own target.
    if (
        not isinstance(body, dict)
        or "email" not in body
        or type(body.get("pipeline_id")) is not int
        or body["pipeline_id"] != 1
    ):
        return
    # Each preview tests a pipeline of its own, inactive: after a reset on a
    # domain of its own, otherwise on the run's domain. Before the run has a
    # domain, it is sent as before.
    if asked_for_a_clean_start():
        domain_id = a_domain_of_its_own(transaction)
    else:
        domain_id = hooks.stash.get("domain_id")
        if domain_id is None:
            return
    body["pipeline_id"] = a_pipeline_of_its_own(transaction, domain_id)
    hooks.set_json(transaction, body)


@hooks.before("deletePipeline")
def delete_a_pipeline_of_its_own(transaction):
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    # missing (2147483647) needs nothing to exist.
    if " > 200" in transaction.get("name", ""):
        domain_id = a_domain_of_its_own(transaction)
        to_pipeline(transaction, a_pipeline_of_its_own(transaction, domain_id))


@hooks.before("updatePipeline")
def update_a_pipeline_of_its_own(transaction):
    # updatePipeline, moved to a clean start: each exchange, refusals included,
    # resets and seeds first. One aimed at a positive id other than missing's
    # 2147483647 updates an inactive pipeline of its own, on a domain of its
    # own, made after the reset; that includes the body refusals (422), so they
    # meet a pipeline and not a 404. 0 and an undecodable id keep theirs. A run
    # with no reset changes nothing here.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    parts = transaction["fullPath"].partition("?")[0].split("/")
    if len(parts) < 6 or not positive_id(parts[5]) or parts[5] == "2147483647":
        return
    domain_id = a_domain_of_its_own(transaction)
    to_pipeline(transaction, a_pipeline_of_its_own(transaction, domain_id))


@hooks.before("listPipelines")
@hooks.before("listPipelineLogs")
@hooks.before("getPipelineAnalytics")
def pipeline_reads_after_a_reset(transaction):
    # The pipeline reads, moved to a clean start: each exchange, refusals
    # included, resets and seeds first. A listing of pipelines that succeeds
    # gets one of its own, inactive, on a domain of its own, made after the
    # reset, so it lists more than the seed. The logs get a send of their own
    # in the_pipeline_log_holds_a_send; the analytics need nothing made:
    # nothing earlier is left for them to report. A run with no reset changes
    # nothing here.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    path = transaction["fullPath"].partition("?")[0]
    if path == "/api/v1/admin/pipelines" and " > 200" in transaction.get("name", ""):
        pipeline = a_pipeline_of_its_own(transaction, a_domain_of_its_own(transaction))
        made_for_the_listing(transaction, "pipeline", {"id": pipeline})


# The delivery log and the pipeline log (#1785). Each lists what a send wrote.
# A message to a remote recipient is queued (outbound_queue, which the delivery
# log reads); the recipient's domain is a .test name nothing hosts, so the
# queue only defers it. A message to a local mailbox is delivered through the
# inbound pipeline, whose trace (message_traces, which the pipeline log reads)
# is what the documented 200 asks for: Vertrag sends the declared defaults,
# direction=inbound, action=continue and outcome=delivered. The testbed keeps
# every trace (TRACE_SAMPLE_RATE=1).
OWN_LOG_DOMAIN = "contract-remote.test"


def a_message_for_the_logs(transaction, recipient):
    """Send one message from the fixture admin to recipient, after the reset,
    and return the sent message."""
    sent = fixture_call(
        transaction,
        "POST",
        "/api/v1/messages/send",
        {
            "from": ADMIN,
            "to": [recipient],
            "subject": "Contract log",
            "body_text": "Sent by the contract for the delivery and pipeline logs.",
        },
    )
    if not isinstance(sent, dict) or not sent.get("message_id"):
        raise RuntimeError("the fixture send returned no message")
    return sent


@hooks.before("getDeliveryLogs")
def the_delivery_log_after_a_reset(transaction):
    # getDeliveryLogs, moved to a clean start: each exchange, refusals
    # included, resets and seeds first. A listing that succeeds gets a message
    # queued for a remote recipient of its own after the reset, and
    # the_listing_holds_what_was_made fails it unless the answer lists that
    # recipient. A run with no reset changes nothing here.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    if " > 200" not in transaction.get("name", ""):
        return
    # The example documents limit=20 only; Vertrag adds the status enum's first
    # value, pending. The topology's queue worker can claim the entry (and
    # defer or bounce it) before the listing is sent, so the status is dropped:
    # the queued row stays in the log whatever its status, as listQueue's hook
    # does for the same race.
    path, _, query = transaction["fullPath"].partition("?")
    kept = [
        (name, value)
        for name, value in urllib.parse.parse_qsl(query, keep_blank_values=True)
        if name != "status"
    ]
    transaction["fullPath"] = path + ("?" + urllib.parse.urlencode(kept) if kept else "")
    recipient = f"contract-log-{time.time_ns():d}@{OWN_LOG_DOMAIN}"
    a_message_for_the_logs(transaction, recipient)
    made_for_the_listing(transaction, "queued message", {"recipient": recipient})


@hooks.before("listPipelineLogs")
def the_pipeline_log_holds_a_send(transaction):
    # Registered after pipeline_reads_after_a_reset, which resets first. A
    # listing that succeeds gets the inbound trace of a message the fixture
    # admin sends itself after the reset, and the_listing_holds_what_was_made
    # fails it unless the answer lists that trace, by its Message-ID. A run
    # with no reset changes nothing here.
    if not asked_for_a_clean_start() or " > 200" not in transaction.get("name", ""):
        return
    sent = a_message_for_the_logs(transaction, ADMIN)
    # Traces are written in the background, a batch every 500 ms
    # (internal/trace/recorder.go).
    time.sleep(1)
    made_for_the_listing(
        transaction,
        "delivery trace",
        {"direction": "inbound", "rfc_message_id": sent["message_id"]},
    )


@hooks.before("createCustomFilter")
def a_custom_filter_on_the_created_domain(transaction):
    # After a reset (the own-data phase) a create is sent as documented: no
    # earlier exchange's filter is left, and the created example names no
    # domain, so it needs none made.
    if asked_for_a_clean_start():
        start_clean(transaction)
        return
    on_the_created_domain(transaction)


def the_probe_domain_id(transaction):
    """A disposable domain's id, made once per run, for what probes may change."""
    if hooks.stash.get("probe_domain_id") is None:
        domain = fixture_call(
            transaction,
            "POST",
            "/api/v1/admin/domains",
            {
                "name": f"contract-probe-{time.time_ns()}.test",
                "server_type": "traditional",
            },
        )
        hooks.stash["probe_domain_id"] = domain["id"]
    return hooks.stash["probe_domain_id"]


def a_row_for_the_coverage_placeholder(transaction, collection, row):
    # Coverage sends a success example and its valid probes to the description's
    # placeholder id, 1, which names nothing: the examples process has gone, and
    # with it whatever its links made. Only the path changes, because a changed
    # body ends a probe uncounted (README, "A hook cannot change a generated
    # body"). Every request gets a new row, since one before it may have deleted
    # the row or rewritten it. The rows sit on a disposable domain, inactive, so
    # that a probe's values can never reach the fixture admin's mail.
    if os.environ.get("CONTRACT_COVERAGE") != "1" or " > 200" not in transaction.get("name", ""):
        return
    path, _, query = transaction["fullPath"].partition("?")
    parts = path.split("/")
    if len(parts) < 6 or parts[4] != collection or parts[5] != "1":
        return
    created = fixture_call(
        transaction,
        "POST",
        "/api/v1/admin/" + collection,
        dict(row, domain_id=the_probe_domain_id(transaction)),
    )
    parts[5] = str(created["id"])
    transaction["fullPath"] = "/".join(parts) + ("?" + query if query else "")


@hooks.before("updatePipeline")
@hooks.before("deletePipeline")
def a_pipeline_for_the_coverage_placeholder(transaction):
    a_row_for_the_coverage_placeholder(
        transaction,
        "pipelines",
        {
            "direction": "inbound",
            "filters": [{"name": "header_validate", "config": {}}],
            "active": False,
        },
    )


# A disabled custom filter whose script testCustomFilter can run: it refuses a
# config without one.
CUSTOM_FILTER = {
    "filter_type": "action",
    "direction": "inbound",
    "config": {"script": 'function filter(email) {\n  return { action: "continue" };\n}\n'},
    "enabled": False,
}


def a_custom_filter_of_its_own(transaction):
    """A custom filter made for this exchange, with no domain, as the documented
    create makes one. Every exchange gets a new one, since one before it may
    have deleted or rewritten the last, so nothing is carried between them."""
    return fixture_call(
        transaction,
        "POST",
        "/api/v1/admin/custom-filters",
        dict(CUSTOM_FILTER, name=f"contract-own-filter-{time.time_ns()}"),
    )["id"]


@hooks.before("getCustomFilter")
@hooks.before("updateCustomFilter")
@hooks.before("testCustomFilter")
@hooks.before("deleteCustomFilter")
def a_custom_filter_for_the_coverage_placeholder(transaction):
    a_row_for_the_coverage_placeholder(
        transaction,
        "custom-filters",
        {
            **CUSTOM_FILTER,
            "name": f"contract-probe-{time.time_ns()}",
        },
    )


@hooks.before("getCustomFilter")
@hooks.before("updateCustomFilter")
@hooks.before("deleteCustomFilter")
@hooks.before("testCustomFilter")
def on_a_custom_filter_of_its_own(transaction):
    """Send each exchange to a custom filter that exists, made for it.

    After a reset (the own-data phase) that is every exchange with a positive
    id, refusals included, so a 422 is refused for its body and not for a
    filter the reset removed. Without one (the primary run) it is the success
    pair's placeholder 1, as before. Missing's 2147483647, a non-positive or
    undecodable id and every documented 404 keep their ids, and coverage's
    placeholder has a hook of its own above."""
    if asked_for_a_clean_start():
        start_clean(transaction)
    if os.environ.get("CONTRACT_COVERAGE") == "1":
        return
    path, separator, query = transaction["fullPath"].partition("?")
    prefix = "/api/v1/admin/custom-filters/"
    if not path.startswith(prefix):
        return
    resource, slash, suffix = path[len(prefix):].partition("/")
    name = transaction.get("name", "")
    if asked_for_a_clean_start():
        if not positive_id(resource) or resource == "2147483647" or " > 404" in name:
            return
    elif resource != "1" or " > 200 > " not in name:
        return
    transaction["fullPath"] = (
        prefix + str(a_custom_filter_of_its_own(transaction)) + slash + suffix + separator + query
    )


def openssl(*args):
    subprocess.run(["openssl"] + list(args), check=True, capture_output=True)


def the_admin_domain_id(transaction):
    """restmail.test's id, through the admin API once per run, or None."""
    if hooks.stash.get("admin_domain_id") is None:
        origin = origin_of(transaction)
        domain = find_in_list(
            origin, "/api/v1/admin/domains",
            {"Authorization": "Bearer " + admin_token(origin)},
            lambda hosted: hosted.get("name") == ADMIN.split("@")[1],
        )
        hooks.stash["admin_domain_id"] = domain["id"] if domain else None
    return hooks.stash["admin_domain_id"]


@hooks.before("setDKIMKey")
@hooks.before("deleteDKIMKey")
@hooks.before("setDKIMRotation")
def off_the_admin_domain(transaction):
    # Coverage probes keep the documented example's id, 1: restmail.test, the
    # fixture admin's domain. Their key deletes and replacements left it
    # signing with selector `contract`, a key DNS does not publish, and their
    # updates left it at dmarc_policy reject (and rewrote active and
    # sending_policy), so the authorization run's self-addressed draft send
    # met its own DMARC reject (503 deferred). A probe aimed at restmail.test
    # goes to a disposable
    # domain instead, made once per run; malformed and other ids reach the
    # handler as generated. Only coverage probes move: the ownership run sends
    # restmail.test's id to the other tenant on purpose, and the examples
    # follow links to the run's own domain. updateDomain's hook calls this
    # before it certifies the domain the request names. A hook never sees a
    # "coverage: " name (README), so the coverage process says so itself.
    if not in_the_coverage_process():
        return
    path, _, query = transaction["fullPath"].partition("?")
    parts = path.split("/")
    # /api/v1/admin/dkim/{id}[/rotation] and /api/v1/admin/domains/{id}
    if len(parts) < 6 or not positive_id(parts[5]):
        return
    if int(parts[5]) != the_admin_domain_id(transaction):
        return
    parts[5] = str(the_probe_domain_id(transaction))
    transaction["fullPath"] = "/".join(parts) + ("?" + query if query else "")


@hooks.before("setDKIMKey")
def a_fresh_signing_key(transaction):
    body = hooks.get_json(transaction)
    # Only substitute the spec's explicit key placeholder. Refused examples
    # and coverage mutations must retain their bodies, selectors and types.
    if not isinstance(body, dict) or body.get("private_key") != "fixture-signing-key":
        return
    with tempfile.TemporaryDirectory() as d:
        key = os.path.join(d, "dkim.key")
        openssl("genrsa", "-out", key, "2048")
        with open(key) as source:
            body["private_key"] = source.read()
        hooks.set_json(transaction, body)


@hooks.before("requestRecoveryEmail")
def a_fresh_recovery_address(transaction):
    if os.environ.get("CONTRACT_AUTHORIZATION") != "1":
        return
    body = hooks.get_json(transaction)
    if (
        str(transaction.get("expected", {}).get("statusCode")) != "200"
        or not isinstance(body, dict)
        or body.get("email") != "unknown@example.test"
    ):
        return
    body["email"] = f"contract-recovery-{time.time_ns()}@example.test"
    hooks.set_json(transaction, body)


def authorization_admin_success(transaction, status):
    return (
        os.environ.get("CONTRACT_AUTHORIZATION") == "1"
        and str(transaction.get("expected", {}).get("statusCode")) == str(status)
        and session_address(transaction) in (ADMIN, mfa_admin.ADDRESS)
    )


@hooks.before("addInstallationSenderRule")
def an_unused_sender_rule_for_authorization(transaction):
    if not authorization_admin_success(transaction, 201):
        return
    if transaction["fullPath"].partition("?")[0] != "/api/v1/admin/sender-rules":
        return
    body = hooks.get_json(transaction)
    if not isinstance(body, dict) or body.get("list_type") not in ("allow", "block"):
        return
    pattern = body.get("pattern")
    if pattern not in ("x", "@blocked.example"):
        return
    unique = f"contract-sender-{time.time_ns()}"
    body["pattern"] = f"@{unique}.example.test" if pattern.startswith("@") else f"{unique}@example.test"
    hooks.set_json(transaction, body)


@hooks.before("deleteBan")
def a_ban_for_the_authorization_admin(transaction):
    if not authorization_admin_success(transaction, 204):
        return
    path, separator, query = transaction["fullPath"].partition("?")
    parent, _, target = path.rpartition("/")
    if parent != "/api/v1/admin/bans" or target != "1":
        return
    ban = fixture_call(
        transaction,
        "POST",
        parent,
        {
            "ip": "192.0.2.77",
            "reason": "Contract authorization fixture",
            "protocol": "all",
            "duration": "1h",
            "created_by": ADMIN,
        },
    )
    transaction["fullPath"] = f"{parent}/{ban['id']}" + separator + query


@hooks.before("createCertificate")
def a_domain_for_the_authorization_certificate(transaction):
    if not authorization_admin_success(transaction, 201):
        return
    body = hooks.get_json(transaction)
    if not isinstance(body, dict) or not (
        body.get("cert_pem") == "certificate" and body.get("key_pem") == "key"
    ):
        return
    domain = fixture_call(
        transaction,
        "POST",
        "/api/v1/admin/domains",
        {"name": f"contract-certificate-{time.time_ns()}.test", "server_type": "traditional"},
    )
    body["domain_id"] = domain["id"]
    hooks.set_json(transaction, body)


@hooks.before("updateDraft")
def a_draft_for_the_authorization_admin(transaction):
    if not authorization_admin_success(transaction, 200):
        return
    path, separator, query = transaction["fullPath"].partition("?")
    parent, _, target = path.rpartition("/")
    if parent != "/api/v1/messages/draft" or not positive_id(target):
        return
    body = hooks.get_json(transaction)
    if not isinstance(body, dict):
        return
    request = urllib.request.Request(
        origin_of(transaction) + parent,
        data=json.dumps({"from": body.get("from", ""), "subject": "Contract authorization draft"}).encode(),
        headers={
            "Content-Type": "application/json",
            "Authorization": transaction["request"]["headers"]["Authorization"],
        },
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        draft = json.load(response)["data"]
    transaction["fullPath"] = f"{parent}/{draft['id']}" + separator + query


# The issuer of the certificate probes.py pins, as the API lists it, which is how the
# hooks tell a probe's certificate from every other.
PROBE_CERTIFICATE_ISSUER = "CN=contract-probe-certificate"
CERTIFICATE_PAGE = 100


@hooks.before("createCertificate")
def a_self_signed_certificate(transaction):
    body = hooks.get_json(transaction)
    # Replace only the documented material placeholders, and, in a probing process,
    # the pinned material of the documented sends named for the 201. Preserve
    # incomplete, invalid and refusal bodies and the caller's renewal policy.
    if not isinstance(body, dict):
        return
    documented = body.get("cert_pem") == "certificate" and body.get("key_pem") == "key"
    succeeds = " > 201 > " in transaction.get("name", "") and all(
        isinstance(body.get(field), str) and body[field]
        for field in ("cert_pem", "key_pem")
    )
    if (
        not isinstance(body.get("domain_id"), int)
        or isinstance(body["domain_id"], bool)
        or body["domain_id"] < 1
        or not (documented or succeeds)
    ):
        return
    if not documented:
        # A probing process sends each transaction's documented request first, and
        # what follows those is a probe: its body goes out as drawn, or it is not sent
        # at all (lost_probes.py). Its pinned material lands on its isolated
        # .invalid domain until remove_the_probe_certificates. The documented
        # sends keep their own certificate on the run's domain, for getCertificate's
        # link to find.
        sends = hooks.stash.setdefault("documented_certificates", {})
        sends[transaction["name"]] = sends.get(transaction["name"], 0) + 1
        if sends[transaction["name"]] > DOCUMENTED_SENDS:
            hooks.stash["probe_certificates_installed"] = True
            return
    if body.get("domain_id") == 1:
        body["domain_id"] = (
            a_standing_domain_for_the_certificate(transaction)
            if hooks.stash.get("domain_id") is not None
            else FIXTURE_PLAN["certificate_domain"]["id"]
        )
    # The server refuses a certificate that does not name the domain it is
    # uploaded for (#1704), so it is made for that domain's own name.
    name = certificate_name_for(transaction, body["domain_id"])
    body["cert_pem"], body["key_pem"] = probe_certificate(name)
    hooks.set_json(transaction, body)


def a_standing_domain_for_the_certificate(transaction):
    """The domain the run made, or one of the certificate's own once that is
    gone. The authorization matrix sends its cells after the examples, and
    deleteDomain's example has removed the run's domain by then: the upload met
    404 Domain not found, which says nothing about who may call it (#376). Only
    the upload moves: what else reads the run's domain keeps its id."""
    domain_id = hooks.stash["domain_id"]
    try:
        standing = fixture_call(
            transaction,
            "GET",
            "/api/v1/admin/domains/%d" % domain_id,
            expect=PROBE_REFUSALS,
        )
    except (urllib.error.URLError, OSError, ValueError):
        return domain_id  # A fault the probe injects: it meets its refusal first.
    if standing:
        return domain_id
    if hooks.stash.get("certificate_domain_id") is None:
        hooks.stash["certificate_domain_id"] = fixture_call(
            transaction,
            "POST",
            "/api/v1/admin/domains",
            {
                "name": "contract-certificate-%d.test" % time.time_ns(),
                "server_type": "traditional",
            },
        )["id"]
    return hooks.stash["certificate_domain_id"]


def certificate_name_for(transaction, domain_id):
    """The name of the domain a certificate is uploaded for. A domain that
    does not answer (missing, or a fault the probe injects) keeps the
    placeholder name: that probe meets its refusal before any name check."""
    if domain_id == hooks.stash.get("domain_id") and hooks.stash.get("domain"):
        return hooks.stash["domain"]
    try:
        domain = fixture_call(
            transaction,
            "GET",
            "/api/v1/admin/domains/%d" % domain_id,
            expect=PROBE_REFUSALS,
        )
    except (urllib.error.URLError, OSError, ValueError):
        domain = None
    return (domain or {}).get("name") or "example.org"


@hooks.before("*")
def remove_the_probe_certificates(transaction):
    # Coverage runs no after hook, so what follows createCertificate's probes removes
    # what they installed. Nothing else carries this issuer.
    if (
        not hooks.stash.get("probe_certificates_installed")
        or transaction.get("operationId") == "createCertificate"
    ):
        return
    hooks.stash["probe_certificates_installed"] = False
    held = []
    offset = 0
    while True:
        page = fixture_call(
            transaction,
            "GET",
            f"/api/v1/admin/certificates?limit={CERTIFICATE_PAGE:d}&offset={offset:d}",
        )
        if not page:
            break
        held += [c["id"] for c in page if c.get("issuer") == PROBE_CERTIFICATE_ISSUER]
        offset += len(page)
    for certificate_id in held:
        fixture_call(transaction, "DELETE", f"/api/v1/admin/certificates/{certificate_id:d}")


@hooks.before("updateDomain")
def a_certificate_before_going_live(transaction):
    # Setting active to true is refused with 409 certificate_required for a
    # domain with no valid certificate, which every created domain is when
    # the probes reach it. The domain gets a self-signed one through the
    # admin API unless the API already lists a valid one for it; the request
    # is never rewritten beyond the id below. Whether a domain has one is read
    # from the API, not carried between exchanges in the stash.
    # After a reset (the own-data phase) every exchange, refusals included,
    # starts clean, and one aimed at a positive id other than missing's
    # 2147483647 is sent to a domain of its own: the reset removed the domain
    # createDomain's link names. A refusal for its body is then refused for its
    # body, not for a domain that is gone.
    if asked_for_a_clean_start():
        start_clean(transaction)
        parts = transaction["fullPath"].partition("?")[0].split("/")
        if len(parts) > 5 and positive_id(parts[5]) and parts[5] != "2147483647":
            to_domain(transaction, a_domain_of_its_own(transaction))
    off_the_admin_domain(transaction)
    body = hooks.get_json(transaction)
    if not isinstance(body, dict) or body.get("active") is not True:
        return
    path = transaction["fullPath"].split("?")[0]
    domain_id = path.rstrip("/").rsplit("/", 1)[-1]
    if not domain_id.isdigit():
        return
    origin = origin_of(transaction)
    headers = {
        "Content-Type": "application/json",
        "Authorization": "Bearer " + admin_token(origin),
    }

    def call(method, path, body=None):
        data = json.dumps(body).encode() if body is not None else None
        request = urllib.request.Request(
            origin + path, data=data, headers=headers, method=method
        )
        with urllib.request.urlopen(request, timeout=10) as response:
            return json.load(response).get("data")

    try:
        name = call("GET", "/api/v1/admin/domains/" + domain_id)["name"]
    except urllib.error.HTTPError as error:
        # No such domain, or an id out of range: the probe meets the refusal itself.
        if error.code in PROBE_REFUSALS:
            return
        raise
    if a_valid_certificate(
        list_rows(origin, "/api/v1/admin/certificates", headers), int(domain_id)
    ):
        return
    with tempfile.TemporaryDirectory() as d:
        key, cert = os.path.join(d, "tls.key"), os.path.join(d, "tls.crt")
        openssl(
            "req",
            "-x509",
            "-newkey",
            "rsa:2048",
            "-nodes",
            "-days",
            "30",
            "-subj",
            "/CN=" + name,
            "-addext",
            "subjectAltName=DNS:" + name,
            "-keyout",
            key,
            "-out",
            cert,
        )
        with open(cert) as cert_file, open(key) as key_file:
            call(
                "POST",
                "/api/v1/admin/certificates",
                {
                    "domain_id": int(domain_id),
                    "cert_pem": cert_file.read(),
                    "key_pem": key_file.read(),
                    "auto_renew": False,
                },
            )


def a_valid_certificate(certificates, domain_id):
    """Whether the listed certificates hold one for the domain that is valid now."""
    now = datetime.datetime.now(datetime.timezone.utc)
    for certificate in certificates or ():
        if certificate.get("domain_id") != domain_id:
            continue
        try:
            not_before = datetime.datetime.fromisoformat(certificate["not_before"].replace("Z", "+00:00"))
            not_after = datetime.datetime.fromisoformat(certificate["not_after"].replace("Z", "+00:00"))
        except (KeyError, TypeError, ValueError):
            continue
        if not_before <= now < not_after:
            return True
    return False


@hooks.before("setMtaStsPolicy")
@hooks.before("getMtaStsPolicy")
@hooks.before("deleteMtaStsPolicy")
def prepare_mta_sts_policy_example(transaction):
    if asked_for_a_clean_start():
        start_clean(transaction)
    name = transaction.get("name", "")
    if not any(f" > {status}" in name for status in (200, 201, 204)):
        return
    path, _, query = transaction["fullPath"].partition("?")
    parts = path.split("/")
    if len(parts) != 7 or parts[-1] != "mta-sts" or not positive_id(parts[5]):
        return
    creating = " > 201" in name
    if asked_for_a_clean_start():
        # Its own domain, which holds no policy until this says so. Refusals
        # above need nothing to exist; the absent-policy 404 makes its own domain
        # (policy_lookup_hooks.py), after this reset.
        to_domain(transaction, a_domain_of_its_own(transaction))
        if not creating:
            fixture_call(
                transaction,
                "PUT",
                transaction["fullPath"].partition("?")[0],
                {
                    "mode": "testing",
                    "mx_hosts": "mail.example.com",
                    "max_age": 604800,
                    "active": True,
                },
            )
        return
    if creating and parts[5] == "2":
        domains = hooks.stash.get("mta_sts_create_domains", {})
        origin = origin_of(transaction)
        if origin not in domains:
            domain = fixture_call(
                transaction,
                "POST",
                "/api/v1/admin/domains",
                {
                    "name": f"contract-mta-sts-{time.time_ns()}.test",
                    "server_type": "traditional",
                },
            )
            domains[origin] = domain["id"]
            hooks.stash["mta_sts_create_domains"] = domains
        parts[5] = str(domains[origin])
        path = "/".join(parts)
        transaction["fullPath"] = path + ("?" + query if query else "")
    if creating:
        fixture_call(transaction, "DELETE", path, expect=(204, 404))
    else:
        fixture_call(
            transaction,
            "PUT",
            path,
            {
                "mode": "testing",
                "mx_hosts": "mail.example.com",
                "max_age": 604800,
                "active": True,
            },
            expect=PROBE_REFUSALS,
        )


# The policy the found pair's response is the file of, byte for byte.
FOUND_POLICY = {
    "mode": "enforce",
    "mx_hosts": "mail.example.com",
    "max_age": 604800,
    "active": True,
}


def a_domain_with_the_found_policy(transaction):
    """The name of a domain of this hook's own whose active policy is
    FOUND_POLICY, through the admin API: made once per server, and its policy
    put again before each request, so nothing sent earlier (an update, a
    delete, a reset) leaves it other than active. A domain a reset removed is
    made again, as is one the stash forgot."""
    origin = origin_of(transaction)
    domains = hooks.stash.get("found_policy_domains", {})
    for _ in range(2):
        if origin not in domains:
            domain = fixture_call(
                transaction,
                "POST",
                "/api/v1/admin/domains",
                {
                    "name": f"contract-mta-sts-active-{time.time_ns()}.test",
                    "server_type": "traditional",
                },
            )
            domains[origin] = {"id": domain["id"], "name": domain["name"]}
            hooks.stash["found_policy_domains"] = domains
        domain = domains[origin]
        if fixture_call(
            transaction,
            "PUT",
            f"/api/v1/admin/domains/{domain['id']:d}/mta-sts",
            FOUND_POLICY,
            expect=(404,),
        ):
            return domain["name"]
        del domains[origin]
    raise RuntimeError("could not give a domain the found MTA-STS policy")


@hooks.before("mtaStsPolicyFile")
def ask_for_the_created_domains_policy(transaction):
    # Served at mta-sts.<domain>: the domain is the Host's, not a parameter.
    # found (200) needs an active policy (x-requires state active_policy, which
    # vertrag.yml provides), and its response is FOUND_POLICY's file byte for byte.
    if asked_for_a_clean_start():
        # Moved to a clean start: reset, and ask for a domain of its own with the
        # found policy, active for the 200 and inactive for the 404 (the
        # documented inactive_policy).
        start_clean(transaction)
        domain_id = a_domain_of_its_own(transaction)
        fixture_call(
            transaction,
            "PUT",
            f"/api/v1/admin/domains/{domain_id:d}/mta-sts",
            dict(FOUND_POLICY, active=" > 200" in transaction.get("name", "")),
        )
        transaction["request"]["headers"]["Host"] = "mta-sts." + OWN_DOMAIN
        return
    # With no reset asked for, the found request asks for a domain that has the
    # policy. Every other request asks for the domain the run created, as before.
    if " > 200" in transaction.get("name", ""):
        transaction["request"]["headers"]["Host"] = (
            "mta-sts." + a_domain_with_the_found_policy(transaction)
        )
        return
    transaction["request"]["headers"]["Host"] = "mta-sts." + (
        hooks.stash.get("domain") or ""
    )


def set_the_admin_domains_policy(transaction, policy):
    """Make restmail.test's sending_policy policy ("" for none), through the
    admin API, unless it already is."""
    if hooks.stash.get("admin_domain_policy", "") == policy:
        return
    origin = "{}//{}:{}".format(
        transaction["protocol"],
        transaction["host"],
        transaction["port"],
    )
    headers = {
        "Content-Type": "application/json",
        "Authorization": "Bearer " + admin_token(origin),
    }
    domain_id = hooks.stash.get("admin_domain_id")
    if domain_id is None:
        domain = find_in_list(
            origin,
            "/api/v1/admin/domains",
            headers,
            lambda d: d.get("name") == ADMIN.split("@")[1],
        )
        domain_id = domain["id"] if domain else None
        if domain_id is None:
            raise RuntimeError("the fixture admin's domain is not listed")
        hooks.stash["admin_domain_id"] = domain_id
    request = urllib.request.Request(
        origin + f"/api/v1/admin/domains/{domain_id:d}",
        data=json.dumps({"sending_policy": policy}).encode(),
        headers=headers,
        method="PATCH",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        if response.status != 200:
            raise RuntimeError("could not set the admin domain's sending policy")
    hooks.stash["admin_domain_policy"] = policy


def a_signing_mailbox_at(transaction, address):
    """Host address's domain, with a DKIM key, and give address a mailbox,
    through the admin API, once per run. A malformed address, or a domain the
    server will not host, is left alone for the request itself to meet."""
    if not isinstance(address, str) or address.count("@") != 1:
        return
    local, domain = address.split("@")
    origin = "{}//{}:{}".format(
        transaction["protocol"],
        transaction["host"],
        transaction["port"],
    )
    ready = hooks.stash.get("sender_mailboxes", {})
    if not local or "." not in domain or (origin + "\n" + address_key(address)) in ready:
        return
    headers = {
        "Content-Type": "application/json",
        "Authorization": "Bearer " + admin_token(origin),
    }

    def call(method, path, body=None):
        data = json.dumps(body).encode() if body is not None else None
        request = urllib.request.Request(
            origin + path, data=data, headers=headers, method=method
        )
        with urllib.request.urlopen(request, timeout=10) as response:
            return json.load(response).get("data")

    hosted = find_in_list(
        origin, "/api/v1/admin/domains", headers,
        lambda hosted: domain_key(hosted.get("name") or "") == domain_key(domain),
    )
    domain_id = hosted["id"] if hosted else None
    if domain_id is None:
        try:
            domain_id = call(
                "POST",
                "/api/v1/admin/domains",
                {"name": domain, "server_type": "traditional"},
            )["id"]
        except urllib.error.HTTPError as error:
            if 400 <= error.code < 500:
                return
            raise
    # An https unsubscribe target is refused for a domain that cannot sign it
    # (dkim_required). The domain may already be hosted without a key (the
    # createAccount hook hosts probe domains too), so a domain with no key
    # gets one; a key already in place is kept.
    if not any(
        k.get("domain_id") == domain_id for k in call("GET", "/api/v1/admin/dkim") or []
    ):
        with tempfile.TemporaryDirectory() as d:
            key = os.path.join(d, "dkim.key")
            openssl("genrsa", "-out", key, "2048")
            with open(key) as key_file:
                try:
                    call(
                        "PUT",
                        f"/api/v1/admin/dkim/{domain_id:d}",
                        {"selector": "contract", "private_key": key_file.read()},
                    )
                except urllib.error.HTTPError as error:
                    if error.code != 422:
                        raise
                    try:
                        refusal = json.load(error)
                    except ValueError:
                        raise error
                    detail = refusal.get("error") if isinstance(refusal, dict) else None
                    details = detail.get("details") if isinstance(detail, dict) else None
                    fields = details.get("fields") if isinstance(details, dict) else None
                    reason = fields.get("private_key") if isinstance(fields, dict) else None
                    if (
                        isinstance(detail, dict)
                        and detail.get("code") == "validation_failed"
                        and isinstance(reason, str)
                        and reason.startswith(f"does not sign for {domain}: invalid domain:")
                    ):
                        error.close()
                        return
                    raise
    if not find_in_list(
        origin,
        f"/api/v1/admin/mailboxes?domain_id={domain_id:d}",
        headers,
        lambda mailbox: address_key(mailbox.get("address") or "") == address_key(address),
    ):
        # Making an account takes a second factor.
        headers["Authorization"] = "Bearer " + mfa_admin_token(origin)
        call("POST", "/api/v1/accounts", {"address": address})
    ready[origin + "\n" + address_key(address)] = True
    hooks.stash["sender_mailboxes"] = ready


@hooks.before("sendMessage")
def a_sending_policy_for_the_send(transaction):
    # After a reset (the own-data phase) every send, refusals included, starts
    # clean. What follows makes the sender's signing domain and mailbox and
    # sets restmail.test's policy again after it: the reset cleared
    # sender_mailboxes, admin_domain_id and admin_domain_policy, and seeded
    # restmail.test again, with no policy and maybe under another id.
    if asked_for_a_clean_start():
        start_clean(transaction)
    # unsubscribe_not_allowed (422) is refused by a transactional sender
    # domain, and restmail.test has no policy: it is made transactional for
    # that request, and the probes built from it, alone. Every other send,
    # coverage probes included (they run no after hooks), finds no policy.
    paired = " > 422 > " in transaction.get("name", "")
    set_the_admin_domains_policy(transaction, "transactional" if paired else "")
    # Coverage probes send from a generated address (someone@example.com)
    # that no mailbox holds, which the handler rightly refuses (404). The
    # sender gets a hosted, signing domain and a mailbox; the request's bytes
    # and credentials stay as generated.
    body = hooks.get_json(transaction)
    # foreign_sender sends from an address no mailbox holds, which the
    # handler refuses (404): it gets none.
    if isinstance(body, dict) and body.get("from") != "someone@other.test":
        a_signing_mailbox_at(transaction, body.get("from"))
        # A recipient with a mailbox here is delivered here; one without is
        # queued for the outside world. Named examples send from the fixture
        # admin and mean what they name (valid's queue entry is a link target).
        # Anything else that has a sender here is generated, and stays inside
        # the topology; without one the handler refuses it before any delivery.
        sender = address_key(str(body.get("from")))
        ready = hooks.stash.get("sender_mailboxes", {})
        if body.get("from") != ADMIN and origin_of(transaction) + "\n" + sender in ready:
            for field in ("to", "cc", "bcc"):
                recipients = body.get(field)
                for address in recipients if isinstance(recipients, list) else []:
                    a_signing_mailbox_at(transaction, address)


@hooks.before("sendMessage")
def a_scheduled_send_an_hour_ahead(transaction):
    # A send must be scheduled a minute to a year ahead (handlers/messages.go),
    # so no date written into the scheduled example stays valid: it said
    # 2026-10-27 and was refused from that day on (#1942). The run sends it an
    # hour from now. Only the 202 is moved: the scheduled_at_* refusals keep
    # their dates, and the coverage run's probes are not touched, as Vertrag
    # abandons a probe whose body a hook changed. future_times.py lists this
    # example as moved by the run, and its test checks that it is.
    if in_the_coverage_process() or " > 202 > " not in transaction.get("name", ""):
        return
    body = hooks.get_json(transaction)
    if not isinstance(body, dict) or not isinstance(body.get("scheduled_at"), str):
        return
    body["scheduled_at"] = time.strftime(
        "%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() + 3600)
    )
    hooks.set_json(transaction, body)


@hooks.after("/api/v1/messages/send > * > 422 > application/json")
def no_sending_policy_after_the_refusal(transaction):
    set_the_admin_domains_policy(transaction, "")


@hooks.before("sendTestEmail")
def a_mailbox_at_each_test_send_address(transaction):
    # Coverage probes send from and to an address the fixture has no mailbox
    # for (someone@example.com), which the handler rightly answers 404. Host
    # its domain and give it an account, once, and leave it there: a test
    # send only adds a message. Every generated request byte stays untouched.
    # After a reset (the own-data phase) every test send, refusals included,
    # starts clean, and the accounts are made again after it: the reset
    # cleared test_send_mailboxes along with the accounts it named.
    from urllib.error import HTTPError

    if asked_for_a_clean_start():
        start_clean(transaction)

    body = hooks.get_json(transaction)
    if not isinstance(body, dict):
        return
    origin = "{}//{}:{}".format(
        transaction["protocol"],
        transaction["host"],
        transaction["port"],
    )
    ready = hooks.stash.get("test_send_mailboxes", {})
    for field in ("from", "to"):
        address = body.get(field)
        if not isinstance(address, str) or address.count("@") != 1:
            continue
        local, domain = address.split("@")
        if not local or "." not in domain or (origin + "\n" + address.lower()) in ready:
            continue  # Leave a malformed address to the actual probe.
        # Making an account takes a second factor.
        headers = {
            "Content-Type": "application/json",
            "Authorization": "Bearer " + mfa_admin_token(origin),
        }
        hosted = find_in_list(
            origin, "/api/v1/admin/domains", headers, lambda d: d.get("name") == domain
        )
        domain_id = hosted["id"] if hosted else None
        if domain_id is None:
            request = urllib.request.Request(
                origin + "/api/v1/admin/domains",
                data=json.dumps(
                    {"name": domain, "server_type": "traditional"}
                ).encode(),
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
        if not find_in_list(
            origin,
            f"/api/v1/admin/mailboxes?domain_id={domain_id:d}",
            headers,
            addressed(address),
        ):
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


def schedule_a_message(transaction, address=None, authorization=None):
    """Store a scheduled message through the API and return its id, so the
    list and cancel examples have something of their own to act on."""
    origin = "{}//{}:{}".format(
        transaction["protocol"],
        transaction["host"],
        transaction["port"],
    )
    address = ADMIN if address is None else address
    headers = {
        "Content-Type": "application/json",
        "Authorization": authorization if authorization is not None else "Bearer " + admin_token(origin),
    }
    body = {
        "from": address,
        "to": [address],
        "subject": "contract scheduled message",
        "body_text": "Contract fixture.",
        "scheduled_at": time.strftime(
            "%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() + 3600)
        ),
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
                    raise RuntimeError(
                        f"could not schedule a contract message: HTTP {response.status:d}"
                    )
                return (json.load(response).get("data") or {}).get("id")
        except urllib.error.HTTPError as error:
            retry_after = error.headers.get("Retry-After", "")
            if (
                attempt
                or error.code != 429
                or not retry_after.isdecimal()
                or not 1 <= int(retry_after) <= 60
            ):
                raise
            error.close()
            time.sleep(int(retry_after))


@hooks.before("listScheduledMessages")
def list_has_a_message_to_show(transaction):
    # Boundary probes and the report-only authorization phase do not need a
    # fixture; an empty list is still a valid answer. Only the examples
    # transaction is given one.
    if in_the_coverage_process() or " as " in transaction.get("name", ""):
        return
    schedule_a_message(transaction)


@hooks.before("cancelScheduledMessage")
def cancel_a_message_we_made(transaction):
    # A boundary probe (0, a word, a fraction) must reach the handler with its
    # own id, so its refusal is the one being judged, and so must the missing
    # example's id that no message holds. Only the found example's message
    # (52), and the valid probe built from it, get a real message.
    path = transaction.get("fullPath", "").split("?")[0]
    target = path.rsplit("/", 1)[-1]
    if (
        os.environ.get("CONTRACT_AUTHORIZATION") == "1"
        and " > 204" in transaction.get("name", "")
        and positive_id(target)
        and target != "2147483647"
        and session_address(transaction) in (ADMIN, mfa_admin.ADDRESS)
    ):
        scheduled_id = schedule_a_message(
            transaction,
            address=session_address(transaction),
            authorization=transaction["request"]["headers"]["Authorization"],
        )
        if isinstance(scheduled_id, int):
            transaction["fullPath"] = f"/api/v1/messages/scheduled/{scheduled_id:d}"
        return
    if path.rsplit("/", 1)[-1] != "52":
        return
    scheduled_id = schedule_a_message(transaction)
    if isinstance(scheduled_id, int):
        transaction["fullPath"] = f"/api/v1/messages/scheduled/{scheduled_id:d}"


def an_imported_message(transaction, folder, subject):
    """The id of a message imported into the fixture admin's mailbox, looked
    up by address, for this exchange alone."""
    origin = origin_of(transaction)
    raw = (
        f"Message-ID: <{folder.rsplit(' ', 1)[-1].lower()}-{time.time_ns()}@restmail.test>\r\n"
        "From: sender@example.test\r\n"
        f"To: {ADMIN}\r\n"
        f"Date: {formatdate(usegmt=True)}\r\n"
        f"Subject: {subject}\r\n\r\nA contract fixture.\r\n"
    ).encode()
    request = urllib.request.Request(
        origin + f"/api/v1/accounts/{mailbox_id(origin, ADMIN):d}/messages/import",
        data=json.dumps(
            {
                "raw_message": base64.b64encode(raw).decode(),
                "folder": folder,
                "internal_date": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            }
        ).encode(),
        headers={"Content-Type": "application/json", "Authorization": "Bearer " + admin_token(origin)},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        message_id = (json.load(response).get("data") or {}).get("id")
    if type(message_id) is not int or message_id < 1:
        raise RuntimeError("the fixture import returned no message")
    return message_id


def a_sent_message(transaction):
    """The id of a message the fixture admin sends itself, for an exchange that
    needs what only a send makes (a delivery trace)."""
    sent = fixture_call(
        transaction,
        "POST",
        "/api/v1/messages/send",
        {
            "from": ADMIN,
            "to": [ADMIN],
            "subject": "Contract trace",
            "body_text": "Sent by the contract.",
        },
    )
    message_id = (sent or {}).get("id")
    if type(message_id) is not int or message_id < 1:
        raise RuntimeError("the fixture send returned no message")
    return message_id


# How long a sent message's trace may take to be written: the recorder writes
# a batch every 500 ms (internal/trace/recorder.go), more on a loaded host.
TRACE_WAIT_SECONDS = 10


def a_traced_message(transaction):
    """The id of a message the fixture admin sends itself, once its trace has
    been written. Traces are written in the background, so the send's answer
    comes before its trace; this asks for the trace until it is there. A trace
    that never comes raises: the API keeps a delivered message's trace only at
    TRACE_SAMPLE_RATE, 0.1 unless set (internal/config/config.go), and the
    contract needs every one kept, TRACE_SAMPLE_RATE=1 as tasks/api.yml sets."""
    message_id = a_sent_message(transaction)
    deadline = time.monotonic() + TRACE_WAIT_SECONDS
    while True:
        trace = fixture_call(
            transaction, "GET", f"/api/v1/admin/messages/{message_id:d}/trace", expect=(404,)
        )
        if trace is not None:
            return message_id
        if time.monotonic() >= deadline:
            raise RuntimeError(
                f"no trace was written for message {message_id:d} within "
                f"{TRACE_WAIT_SECONDS:d} s: run the API with TRACE_SAMPLE_RATE=1, "
                "since it keeps 1 in 10 delivered messages' traces by default"
            )
        time.sleep(0.25)


@hooks.before("getMessage")
@hooks.before("updateMessage")
@hooks.before("getRawMessage")
@hooks.before("forwardMessage")
@hooks.before("listAttachments")
@hooks.before("getMessageTrace")
@hooks.before("deleteMessage")
@hooks.before("verifyMessageSeal")
def on_a_message_of_its_own(transaction):
    # These follow sendMessage's link (and getMessage's and getRawMessage's).
    # After a reset (the own-data phase) every exchange, refusals included,
    # starts clean, and the message the link names went with the reset. So an
    # exchange aimed at a positive message id gets a message of its own, made
    # after the reset: imported into the seeded admin's mailbox, or for a trace
    # sent, since only a send writes one, and read once its trace is written.
    # A refusal is then refused for its body and not for a message that is
    # gone. Missing's 2147483647, a non-positive or undecodable id and every
    # documented 404 keep their ids.
    # Without a reset the link's message is still there, and nothing changes.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    path, _, query = transaction["fullPath"].partition("?")
    parts = path.split("/")
    # /api/v1/messages/{id}..., and the trace's /api/v1/admin/messages/{id}.
    traced = path.startswith("/api/v1/admin/messages/")
    segment = 5 if traced else 4
    if len(parts) <= segment:
        return
    message = parts[segment]
    if not positive_id(message) or message == "2147483647":
        return
    if " > 404" in transaction.get("name", ""):
        return
    if traced:
        message_id = a_traced_message(transaction)
    else:
        message_id = an_imported_message(transaction, "Contract own", "Contract own message")
    parts[segment] = str(message_id)
    transaction["fullPath"] = "/".join(parts) + ("?" + query if query else "")


@hooks.before("getMessage")
def a_message_for_the_coverage_read(transaction):
    if not in_the_coverage_process() or asked_for_a_clean_start():
        return
    if " > 200" not in transaction.get("name", ""):
        return
    path, separator, query = transaction["fullPath"].partition("?")
    parts = path.split("/")
    if (
        len(parts) != 5
        or parts[1:4] != ["api", "v1", "messages"]
        or not positive_id(parts[4])
        or parts[4] == "2147483647"
    ):
        return
    parts[4] = str(an_imported_message(transaction, "Contract coverage", "Contract coverage message"))
    transaction["fullPath"] = "/".join(parts) + separator + query


# The bytes of the attachment getAttachment's examples read after a reset:
# more than the 10 the single range asks for, fewer than the 1000 the
# unsatisfiable range starts at, and text, so the answer reads back as sent.
ATTACHMENT_BYTES = b"REST Mail contract attachment: the bytes its ranges are read from.\n"

# getAttachment's statuses that read an attachment which is there: the whole
# file, a range, the bodyless 304 and 412, and the unsatisfiable range's 416.
ATTACHMENT_READS = ("200", "206", "304", "412", "416")


def an_attachment_of_its_own(transaction):
    """The id of an attachment holding ATTACHMENT_BYTES, on a message imported
    into the fixture admin's mailbox through the API, for this exchange."""
    origin = origin_of(transaction)
    boundary = f"contract-{time.time_ns():d}"
    raw = (
        f"Message-ID: <attachment-{time.time_ns():d}@restmail.test>\r\n"
        "From: sender@example.test\r\n"
        f"To: {ADMIN}\r\n"
        f"Date: {formatdate(usegmt=True)}\r\n"
        "Subject: Contract attachment\r\n"
        "MIME-Version: 1.0\r\n"
        f'Content-Type: multipart/mixed; boundary="{boundary}"\r\n\r\n'
        f"--{boundary}\r\n"
        "Content-Type: text/plain; charset=utf-8\r\n\r\n"
        "A contract fixture with an attachment.\r\n"
        f"--{boundary}\r\n"
        "Content-Type: application/octet-stream\r\n"
        'Content-Disposition: attachment; filename="contract.bin"\r\n'
        "Content-Transfer-Encoding: base64\r\n\r\n"
        f"{base64.b64encode(ATTACHMENT_BYTES).decode()}\r\n"
        f"--{boundary}--\r\n"
    ).encode()
    message = fixture_call(
        transaction,
        "POST",
        f"/api/v1/accounts/{mailbox_id(origin, ADMIN):d}/messages/import",
        {
            "raw_message": base64.b64encode(raw).decode(),
            "folder": "INBOX",
            "internal_date": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        },
    )
    message_id = (message or {}).get("id")
    if type(message_id) is not int or message_id < 1:
        raise RuntimeError("the fixture import returned no message")
    attachments = fixture_call(transaction, "GET", f"/api/v1/messages/{message_id:d}/attachments")
    ids = [row.get("id") for row in attachments or () if isinstance(row, dict)]
    if len(ids) != 1 or type(ids[0]) is not int or ids[0] < 1:
        raise RuntimeError(f"the imported message holds no single attachment: {ids!r}")
    return ids[0]


def expected_status(transaction):
    """The status in a transaction's name: "<path> > <summary> > 206 > ..."."""
    for part in transaction.get("name", "").split(" > ")[1:]:
        if len(part) == 3 and part.isdecimal():
            return part
    return ""


def prepare_attachment_read(transaction):
    path, separator, query = transaction["fullPath"].partition("?")
    parts = path.split("/")
    if len(parts) != 5 or not positive_id(parts[4]) or parts[4] == "2147483647":
        return
    if expected_status(transaction) not in ATTACHMENT_READS:
        return
    parts[4] = str(an_attachment_of_its_own(transaction))
    transaction["fullPath"] = "/".join(parts) + separator + query
    hooks.stash["attachment_made"] = transaction.get("name", "")


@hooks.before("getAttachment")
def primary_attachment_fixture(transaction):
    if not asked_for_a_clean_start():
        prepare_attachment_read(transaction)


@hooks.before("getAttachment")
def on_an_attachment_of_its_own(transaction):
    # getAttachment, moved to a clean start: each exchange, refusals included,
    # resets and seeds first. Its examples name attachment 1, which the reset
    # does not make. An exchange that reads an attachment which is there (the
    # whole file, a range, the 304 and 412 conditionals and the 416 range) is
    # sent to one of its own, imported after the reset with ATTACHMENT_BYTES,
    # and its after hook checks the answer against those bytes. Refusals keep
    # their ids (0, missing's 2147483647 and the others' own), and so does every
    # documented 404. No link reaches it. A run with no reset changes nothing.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    prepare_attachment_read(transaction)


def header_of(message, name):
    """A header of a request or answer, by name in any case, or ""."""
    for key, value in ((message or {}).get("headers") or {}).items():
        if key.lower() == name.lower():
            return ", ".join(value) if isinstance(value, list) else str(value)
    return ""


@hooks.after("getAttachment")
def the_attachment_answers_its_bytes(transaction):
    # After a reset, a read of the attachment made for it must answer that
    # attachment's bytes, so the status and the headers alone do not pass on
    # another one: the whole file, each range the request asks for with its
    # Content-Range, and the unsatisfiable range's "bytes */<length>". The
    # bodyless 304 and 412 need the attachment to be there, which their
    # status shows. A status other than the one documented fails on its own.
    made = hooks.stash.pop("attachment_made", None)
    status = expected_status(transaction)
    real = transaction.get("real") or {}
    if (
        (not asked_for_a_clean_start() and made is None)
        or status not in ATTACHMENT_READS
        or str(real.get("statusCode")) != status
    ):
        return
    if made != transaction.get("name", ""):
        transaction["fail"] = "no attachment was made for this read"
        return
    total = len(ATTACHMENT_BYTES)
    body = real.get("body") or ""
    body = body.encode("latin-1") if isinstance(body, str) else bytes(body)
    ranges = header_of(transaction.get("request"), "Range").partition("=")[2]
    spans = []
    for span in filter(None, (s.strip() for s in ranges.split(","))):
        first, _, last = span.partition("-")
        if first.isdecimal():
            spans.append((int(first), min(int(last) if last else total - 1, total - 1)))
    problems = []
    if status == "200" and body != ATTACHMENT_BYTES:
        problems.append(f"the body ({len(body):d} bytes) is not the {total:d} bytes attached")
    elif status == "206" and len(spans) == 1:
        first, last = spans[0]
        if body != ATTACHMENT_BYTES[first : last + 1]:
            problems.append(f"the body is not bytes {first:d}-{last:d} of the attachment")
        if header_of(real, "Content-Range") != f"bytes {first:d}-{last:d}/{total:d}":
            problems.append(
                f"Content-Range is {header_of(real, 'Content-Range')!r}, "
                f"not 'bytes {first:d}-{last:d}/{total:d}'"
            )
    elif status == "206":
        # A part per range: its headers, Content-Range among them, then its bytes.
        for first, last in spans:
            header = f"Content-Range: bytes {first:d}-{last:d}/{total:d}\r\n".encode()
            start = body.find(header)
            start = body.find(b"\r\n\r\n", start) + 4 if start >= 0 else -1
            want = ATTACHMENT_BYTES[first : last + 1]
            if start < 4 or body[start : start + len(want)] != want:
                problems.append(f"no part holds bytes {first:d}-{last:d} of the attachment")
    elif status == "416" and header_of(real, "Content-Range") != f"bytes */{total:d}":
        problems.append(
            f"Content-Range is {header_of(real, 'Content-Range')!r}, not 'bytes */{total:d}'"
        )
    if problems:
        transaction["fail"] = "the attachment made for it was not answered: " + "; ".join(problems)


@hooks.before("getThread")
def on_a_thread_of_its_own(transaction):
    # getThread follows getMessage's link, with its mailbox and thread. After a
    # reset both went, so a positive mailbox id gets the thread of a message of
    # its own, in the seeded admin's mailbox, both read back from the message.
    # A non-positive or undecodable id, missing's and a documented 404 keep
    # theirs. Without a reset nothing changes.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    path, _, query = transaction["fullPath"].partition("?")
    parts = path.split("/")
    if len(parts) < 7 or not positive_id(parts[4]) or parts[4] == "2147483647":
        return
    if " > 404" in transaction.get("name", ""):
        return
    message = fixture_call(
        transaction,
        "GET",
        "/api/v1/messages/{:d}".format(
            an_imported_message(transaction, "Contract thread", "Contract thread")
        ),
    )
    parts[4] = str(message["mailbox_id"])
    parts[6] = urllib.parse.quote(message["thread_id"], safe="")
    transaction["fullPath"] = "/".join(parts) + ("?" + query if query else "")


def a_thread_awaiting_a_reply(transaction, account):
    """A message from the fixture admin, imported into its Sent folder, so the
    account has a thread whose last message is its own. Answers its subject,
    which names it by the time it is made."""
    subject = f"Contract awaiting {time.time_ns():d}"
    a_message_in(transaction, account, "Sent", ADMIN, subject, "Waiting for a reply.")
    return subject


def a_message_in(transaction, account, folder, sender, subject, body):
    """Import a message from sender with that subject and body into the
    account's folder, through the API."""
    raw = (
        f"Message-ID: <contract-{time.time_ns()}@restmail.test>\r\n"
        f"From: {sender}\r\n"
        f"To: {ADMIN if sender != ADMIN else 'someone@example.test'}\r\n"
        f"Date: {formatdate(usegmt=True)}\r\n"
        f"Subject: {subject}\r\n\r\n{body}\r\n"
    ).encode()
    fixture_call(
        transaction,
        "POST",
        f"/api/v1/accounts/{account:d}/messages/import",
        {
            "raw_message": base64.b64encode(raw).decode(),
            "folder": folder,
            "internal_date": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        },
    )


@hooks.before("listAccountAddresses")
def report_addresses_of_the_callers_own_account(transaction):
    # The documented success names account 10, which is only an id: which
    # account holds it depends on how many were made before, and a domain's
    # provisioned postmaster takes one too (e2e stage 2's throwaway domain's
    # postmaster is 10 and is retired with it). Sent by an administrator, in
    # any run, it asks for the caller's own account, looked up by address.
    if (
        " > 200 > " not in transaction.get("name", "")
        or transaction.get("fullPath") != "/api/v1/accounts/10/addresses"
    ):
        return
    address = session_address(transaction)
    if address not in (ADMIN, mfa_admin.ADDRESS):
        return
    account = mailbox_id(origin_of(transaction), address)
    transaction["fullPath"] = f"/api/v1/accounts/{account:d}/addresses"


@hooks.before("listThreads")
@hooks.before("setThreadDue")
@hooks.before("snoozeThread")
@hooks.before("importMessage")
def on_the_admins_mailbox_after_a_reset(transaction):
    # The thread and import operations, moved to a clean start: each exchange,
    # refusals included, resets and seeds first. Their examples name account 1
    # as the fixture admin's own mailbox; an exchange aimed at a positive
    # account other than missing's 2147483647 goes to the fixture admin's
    # mailbox, looked up by address, so it does not depend on which ids the
    # reset keeps. A list of threads awaiting a reply gets one of its own. The
    # due date's and the snooze's thread is imported after the reset by their
    # own hooks (thread_due_hooks.py, thread_snooze_hooks.py), registered after
    # this one, into the account this leaves in the path. 0, an undecodable id
    # and every documented 404 keep their ids. A run with no reset changes
    # nothing here.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    path, separator, query = transaction["fullPath"].partition("?")
    parts = path.split("/")
    if len(parts) < 5 or not positive_id(parts[4]) or parts[4] == "2147483647":
        return
    if " > 404" in transaction.get("name", ""):
        return
    account = mailbox_id(origin_of(transaction), ADMIN)
    parts[4] = str(account)
    transaction["fullPath"] = "/".join(parts) + separator + query
    if parts[-1] == "threads" and " > 200" in transaction.get("name", ""):
        subject = a_thread_awaiting_a_reply(transaction, account)
        if "cursor" in urllib.parse.parse_qs(query):
            # The page after the example's cursor holds older threads only,
            # and is empty by design: the thread made now is not on it.
            made_for_the_listing(transaction, "nothing past the cursor", None)
        else:
            made_for_the_listing(transaction, "thread", {"last_message.subject": subject})


@hooks.before("listFolders")
@hooks.before("listMessages")
@hooks.before("searchMessages")
def a_mailbox_read_after_a_reset(transaction):
    # The mailbox reads listAccounts' link reaches, moved to a clean start:
    # each exchange, refusals included, resets and seeds first. Their examples
    # name account 1, the fixture admin's mailbox, which the reset seeds again
    # under another id; an exchange aimed at a positive account other than
    # missing's 2147483647 goes to that mailbox, looked up by address. A
    # listing that succeeds gets something of its own to list, made after the
    # reset: the folder list a folder, the folder's messages a message in it,
    # and a search a message its query finds. 0, an undecodable id and every
    # documented 404 keep their ids. A run with no reset changes nothing here.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    path, separator, query = transaction["fullPath"].partition("?")
    parts = path.split("/")
    if len(parts) < 5 or not positive_id(parts[4]) or parts[4] == "2147483647":
        return
    if " > 404" in transaction.get("name", ""):
        return
    account = mailbox_id(origin_of(transaction), ADMIN)
    parts[4] = str(account)
    transaction["fullPath"] = "/".join(parts) + separator + query
    if " > 200" not in transaction.get("name", ""):
        return
    if parts[-1] == "folders":
        name = f"Contract own folder {time.time_ns():d}"
        fixture_call(transaction, "POST", f"/api/v1/accounts/{account:d}/folders", {"name": name})
        made_for_the_listing(transaction, "folder", {"name": name})
    elif parts[-1] == "messages" and len(parts) > 6:
        folder = urllib.parse.unquote(parts[6])
        subject = f"Contract listed {time.time_ns():d}"
        a_message_in(transaction, account, folder, "sender@example.test", subject, "Listed.")
        made_for_the_listing(transaction, "message", {"subject": subject, "folder": folder})
    elif parts[-1] == "search":
        # The query's words in the subject, so the full-text search finds it.
        words = urllib.parse.parse_qs(query).get("q", [""])[0]
        subject = f"{words} contract found {time.time_ns():d}".strip()
        a_message_in(transaction, account, "INBOX", "sender@example.test", subject, "Found.")
        made_for_the_listing(transaction, "message", {"subject": subject})


@hooks.before("updateDraft")
def on_a_draft_of_its_own(transaction):
    # updateDraft follows saveDraft's link. After a reset (the own-data phase)
    # every exchange starts clean, and one aimed at a positive draft id gets a
    # draft of its own, from the seeded admin, made after the reset.
    # Missing's 2147483647, a non-positive or undecodable id and every
    # documented 404 keep their ids. Without a reset nothing changes.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    path, _, query = transaction["fullPath"].partition("?")
    parent, _, draft = path.rpartition("/")
    if not positive_id(draft) or draft == "2147483647":
        return
    if " > 404" in transaction.get("name", ""):
        return
    made = fixture_call(
        transaction,
        "POST",
        "/api/v1/messages/draft",
        {
            "from": ADMIN,
            "to": [ADMIN],
            "subject": "Contract draft",
            "body_text": "Not sent yet.",
        },
    )
    transaction["fullPath"] = f"{parent}/{made['id']:d}" + ("?" + query if query else "")


@hooks.before("deleteMessage")
def delete_a_message_we_made(transaction):
    # The example's message, 51, is a number and not a fixture: which row it
    # names depends on how many messages the run has made so far, and that row
    # may belong to someone else or be gone. Coverage sends the example with its
    # own id before its valid probes, so the fixture admin's request gets a
    # message of its own to delete. Another principal's request, the refusal
    # examples' ids and the probes' ids are sent as they are. Only the path
    # changes, so Vertrag does not abandon the probe.
    path, _, query = transaction.get("fullPath", "").partition("?")
    if path.rsplit("/", 1)[-1] != "51" or not with_the_fixture_admins_session(transaction):
        return
    message_id = an_imported_message(transaction, "Contract delete", "Delete contract fixture")
    transaction["fullPath"] = f"/api/v1/messages/{message_id:d}" + ("?" + query if query else "")


@hooks.before("listQueue")
def list_the_sent_messages_queue_entry(transaction):
    # The example asks for status=pending (the enum's first value), and the
    # queue worker can already have delivered sendMessage's entry when retry
    # runs. The disposable topology prepares a separate deferred entry, held
    # until tomorrow, for the linked queue examples. Only the pending example
    # is redirected; invalid boundary queries and caller credentials stay intact.
    # Vertrag 0.31.1 also sends the declared defaults (limit=50, offset=0), so
    # the pending example is recognised by its query, not by its exact text.
    path, _, query = transaction.get("fullPath", "").partition("?")
    # After a reset the fixture entry is gone: queued_and_held_mail_of_its_own
    # lists an entry of its own.
    if not path.endswith("/api/v1/admin/queue") or asked_for_a_clean_start():
        return
    # The example's own page size (limit=20) counts as well: any positive limit
    # and non-negative offset, so the boundary probes (limit=0, offset=-1) stay.
    params = urllib.parse.parse_qs(query, keep_blank_values=True)

    def paging(name, value):
        if name not in ("limit", "offset") or len(value) != 1:
            return False
        number = value[0]
        return number.isdecimal() and (name == "offset" or int(number) > 0)

    if params.get("status") != ["pending"] or any(
        name != "status" and not paging(name, value) for name, value in params.items()
    ):
        return
    transaction["fullPath"] = (
        path
        + "?"
        + urllib.parse.urlencode(
            {"recipient": FIXTURE_PLAN["queue"]["retry"]["recipient"]}
        )
    )


@hooks.before("getMessageTrace")
def wait_for_the_trace(transaction):
    # Traces are written in the background, a batch every 500 ms
    # (internal/trace/recorder.go); the testbed keeps every one
    # (TRACE_SAMPLE_RATE=1 in tasks/api.yml). After a reset the message is one
    # of its own, read only once its trace is there (a_traced_message).
    if asked_for_a_clean_start():
        return
    if in_the_coverage_process():
        path, separator, query = transaction["fullPath"].partition("?")
        parts = path.split("/")
        if (
            len(parts) == 7
            and parts[1:5] == ["api", "v1", "admin", "messages"]
            and parts[6] == "trace"
            and positive_id(parts[5])
            and parts[5] != "2147483647"
            and " > 404" not in transaction.get("name", "")
        ):
            parts[5] = str(a_traced_message(transaction))
            transaction["fullPath"] = "/".join(parts) + (separator + query if separator else "")
        return
    time.sleep(1)


@hooks.before("receiveTlsReport")
def pace_the_tls_reports(transaction):
    # The route is rate-limited per client IP, 1 a second after a burst of 10
    # (DefaultTLSRPTRateLimitRPS, internal/config/config.go), and coverage
    # sends more probes than that: a 429 is the limiter, not the report.
    rate_limits.pace(1)


@hooks.before("receiveTlsReport")
def host_the_reported_policy_domains(transaction):
    # Generated reports name domains other than restmail.test. Host them so
    # a valid report reaches storage; leave every generated byte unchanged.
    # The 422 is the report that names no hosted domain: leave it unhosted.
    if " > 422 > " in transaction["name"]:
        return
    body = hooks.get_json(transaction)
    policies = body.get("policies") if isinstance(body, dict) else None
    if not isinstance(policies, list):
        return
    # A probe's policy entries name no domain at all, and a report naming none
    # hosted is the 422: its first entry names the fixtures' domain.
    named = [
        entry["policy"].get("policy-domain")
        for entry in policies
        if isinstance(entry, dict) and isinstance(entry.get("policy"), dict)
    ]
    if policies and isinstance(policies[0], dict) and not any(named):
        policies[0]["policy"] = {"policy-domain": "restmail.test"}
        hooks.set_json(transaction, body)
    origin = "{}//{}:{}".format(
        transaction["protocol"],
        transaction["host"],
        transaction["port"],
    )
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
    return (
        transaction["fullPath"].split("?")[0] + "/" + urllib.parse.quote(name, safe="")
    )


@hooks.before("createFolder")
def remove_a_previous_folder_probe(transaction):
    # After a reset (the own-data phase) a create is sent as documented, to a
    # mailbox of its own: the reset removed every folder an earlier exchange
    # made, so there is nothing to remove first.
    if asked_for_a_clean_start():
        start_clean(transaction)
        to_a_mailbox_of_its_own(transaction)
        return
    # Valid probes repeat the example's name. Only reuse destinations that
    # were absent before this operation tried them, leaving requests untouched:
    # rewriting its body would make vertrag skip the boundary being tested.
    path = created_folder_path(transaction)
    parent = transaction["fullPath"].split("?")[0]
    account_id = parent.split("/")[-2]
    if not path or not account_id.isdecimal() or int(account_id) < 1:
        return
    created = hooks.stash.get("created_folders", {})
    origin = "{}//{}:{}".format(
        transaction["protocol"],
        transaction["host"],
        transaction["port"],
    )
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


OWN_MAILBOX = "contract-own-folders@"


def a_mailbox_of_its_own(transaction):
    """A mailbox made for this exchange after its reset, on a domain of its
    own. Making one takes a session signed in with a second factor."""
    a_domain_of_its_own(transaction)
    origin = origin_of(transaction)
    request = urllib.request.Request(
        origin + "/api/v1/accounts",
        data=json.dumps({"address": OWN_MAILBOX + OWN_DOMAIN}).encode(),
        headers={
            "Content-Type": "application/json",
            "Authorization": "Bearer " + mfa_admin_token(origin),
        },
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        return json.load(response)["data"]["id"]


def to_a_mailbox_of_its_own(transaction):
    """After a reset, send a folder exchange to a mailbox made for it: the reset
    removed the mailbox its example names. Only the account id in the path
    changes. A non-positive or undecodable id and missing's 2147483647 keep
    theirs, so they are refused as documented."""
    path, separator, query = transaction["fullPath"].partition("?")
    parts = path.split("/")
    if len(parts) < 5 or not positive_id(parts[4]) or parts[4] == "2147483647":
        return
    parts[4] = str(a_mailbox_of_its_own(transaction))
    transaction["fullPath"] = "/".join(parts) + separator + query


def a_folder_of_its_own(transaction, parent, prefix):
    """A folder made for this exchange in the account parent names. Each one has
    a new name, so nothing is carried between exchanges."""
    folder = f"{prefix}-{time.time_ns():d}"
    fixture_call(transaction, "POST", parent, {"name": folder}, expect=())
    return folder


@hooks.before("renameFolder")
def give_each_rename_probe_a_folder(transaction):
    # A successful rename consumes its source folder. Each boundary probe
    # needs its own source, or a later valid request sees the earlier one's
    # 404. Hooks see the operation's original name in every phase, so prepare
    # the example too. Keep the body and account ID exactly as generated.
    # After a reset (the own-data phase) every exchange, refusals included,
    # starts clean first and is sent to a mailbox of its own.
    if asked_for_a_clean_start():
        start_clean(transaction)
        to_a_mailbox_of_its_own(transaction)
    path = transaction["fullPath"].split("?")[0]
    parent, _, _ = path.rpartition("/")
    account_id = parent.split("/")[-2]
    if not account_id.isdecimal() or int(account_id) < 1:
        return
    try:
        folder = a_folder_of_its_own(transaction, parent, "contract-rename")
    except urllib.error.HTTPError as error:
        if error.code in (400, 403, 404):
            # An account the run cannot reach, such as a generated ID no
            # account has: the real probe answers that refusal itself, as
            # createFolder's hook leaves it to (#1163).
            return
        raise
    origin = "{}//{}:{}".format(
        transaction["protocol"],
        transaction["host"],
        transaction["port"],
    )
    transaction["fullPath"] = parent + "/" + folder
    # A rename onto a folder that exists is 409 already_exists (#757), and the
    # earlier probes left the destination behind: remove it, as createFolder's
    # hook does.
    body = hooks.get_json(transaction)
    target = body.get("name") if isinstance(body, dict) else None
    if not isinstance(target, str) or not target.strip():
        return
    if target.upper() == "INBOX" or target in (
        "Sent",
        "Drafts",
        "Trash",
        "Spam",
        "Archive",
    ):
        return
    request = urllib.request.Request(
        origin + parent + "/" + urllib.parse.quote(target, safe=""),
        headers={"Authorization": "Bearer " + admin_token(origin)},
        method="DELETE",
    )
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            if response.status != 204:
                raise RuntimeError("could not remove the previous rename's folder")
    except urllib.error.HTTPError as error:
        if error.code not in (400, 404):
            raise


@hooks.before("deleteFolder")
def a_folder_to_delete_of_its_own(transaction):
    # deleteFolder follows renameFolder's link, which names the folder the
    # rename made. After a reset (the own-data phase) that folder and its
    # mailbox are gone, so every exchange starts clean and is sent to a mailbox
    # of its own, and a delete documented to succeed gets a folder of its own.
    # The refusals keep their folders: a missing one (404) and a system folder
    # (422). A run with no reset, which is the primary run, changes nothing here.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    to_a_mailbox_of_its_own(transaction)
    if " > 204" not in transaction.get("name", ""):
        return
    path, _, query = transaction["fullPath"].partition("?")
    parent, _, _ = path.rpartition("/")
    account_id = parent.split("/")[-2]
    if not positive_id(account_id) or account_id == "2147483647":
        return
    folder = a_folder_of_its_own(transaction, parent, "contract-delete")
    transaction["fullPath"] = parent + "/" + folder + ("?" + query if query else "")


# The internal listener the gateways call, and the IMAP gateway's client
# certificate for it (chore test:contract-mtls copies them; vertrag.yml sends the
# session routes there with the same one).
INTERNAL_ORIGIN = os.environ.get("CONTRACT_INTERNAL_ENDPOINT") or "https://10.99.0.20:8443"
CONTRACT_MTLS = os.environ.get("CONTRACT_MTLS_DIR", ".cache/contract-mtls")


@hooks.before("/api/v1/gateway-sessions > * > 201 > application/json")
@hooks.before("/api/v1/gateway-sessions > * > 422 > application/json")
def sign_in_a_mail_client(transaction):
    """The IMAP gateway opens a session for the app password principal: its
    address and secret go in the body, the only place a mail client's password
    is sent (#1586). The pair's protocol stays, so 422 still names another."""
    # After a reset the principal is gone: sign_in_and_recovery_after_a_reset
    # gives the exchange an app password of an account made for it.
    if asked_for_a_clean_start():
        return
    app = principals()["app_password"]
    body = hooks.get_json(transaction)
    body["address"], body["password"] = app["address"], app["secret"]
    hooks.set_json(transaction, body)


def open_gateway_session():
    """A session the IMAP gateway opens for the app password principal, as
    the gateway does at a mail client's LOGIN."""
    app = principals()["app_password"]
    context = ssl.create_default_context(cafile=f"{CONTRACT_MTLS}/internal-ca.crt")
    context.load_cert_chain(f"{CONTRACT_MTLS}/internal-imap.crt", f"{CONTRACT_MTLS}/internal-imap.key")
    pace_auth_requests(None)
    request = urllib.request.Request(
        INTERNAL_ORIGIN + "/api/v1/gateway-sessions",
        data=json.dumps({"protocol": "imap", "address": app["address"], "password": app["secret"]}).encode(),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=10, context=context) as response:
        token = (json.load(response).get("data") or {}).get("token")
    if not isinstance(token, str) or not token.startswith("rmgs_"):
        raise RuntimeError("the IMAP gateway could not open a session")
    return token


@hooks.before("/api/v1/gateway-sessions/current > * > 204")
def close_a_mail_clients_session(transaction):
    """The gateway closes a session it opened, with that session's token."""
    transaction["request"]["headers"]["Authorization"] = "Bearer " + open_gateway_session()


def a_leftover_to_remove(transaction):
    """Whether a create may remove an object of the same value that this
    process did not make.

    The authorization and ownership runs (tests/contract/run.sh sets
    CONTRACT_REMOVE_LEFTOVERS) come after the examples and coverage runs of the
    same job, in vertrag processes of their own, so the objects those runs made
    from the documented examples are still there and this process's stash does
    not know them: new-account@restmail.test, created@example.com and
    sales@restmail.test answered 409, and every check linked from them went
    unsent (#657, #376). Only a create documented to succeed (201) clears its way;
    a 409 example names an object on purpose (admin@restmail.test), and every
    other run leaves objects it did not make alone.
    """
    if os.environ.get("CONTRACT_REMOVE_LEFTOVERS") != "1":
        return False
    parts = transaction.get("name", "").split(" > ")
    return len(parts) > 2 and parts[2].split(" ")[0] == "201"


def to_the_seeded_account(transaction):
    """After a reset, send a contact exchange to the seeded admin@restmail.test:
    the reset removed the mailbox its example names (1) and seeded that address
    again under another id. Only the account id in the path changes. A
    non-positive or undecodable id and missing's 2147483647 keep theirs, so
    they are refused as documented."""
    path, separator, query = transaction["fullPath"].partition("?")
    parts = path.split("/")
    if len(parts) < 5 or not positive_id(parts[4]) or parts[4] == "2147483647":
        return
    parts[4] = str(mailbox_id(origin_of(transaction), ADMIN))
    transaction["fullPath"] = "/".join(parts) + separator + query


def a_contact_exchange_from_a_clean_start(transaction):
    """With a reset asked for, reset before this contact exchange, refusals
    included, and send it to the seeded account."""
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    to_the_seeded_account(transaction)


@hooks.before("listContacts")
@hooks.before("importContacts")
@hooks.before("suggestContacts")
@hooks.before("getContactReplyTimeSetting")
@hooks.before("setContactReplyTimeSetting")
def a_contact_operation_from_a_clean_start(transaction):
    # These examples need nothing made for them: after a reset they read,
    # import into and set the seeded account's empty contacts and setting.
    a_contact_exchange_from_a_clean_start(transaction)


# The contact updateContact and deleteContact are sent to after a reset: the
# one createContact's link names went with it.
OWN_CONTACT = {"email": "contract-own-contact@example.test"}


@hooks.before("updateContact")
@hooks.before("deleteContact")
def on_a_contact_of_its_own(transaction):
    # After a reset (the own-data phase) every exchange, refusals included,
    # starts clean and is sent to the seeded account, and one aimed at a
    # positive contact id gets a contact of its own there, made after the
    # reset, so a refusal is refused for its body and not for a contact that
    # is gone. Missing's 2147483647, a non-positive or undecodable id, every
    # documented 404 and an account that is not there keep their ids. Without
    # a reset the link's contact is still there, and nothing changes.
    if not asked_for_a_clean_start():
        return
    a_contact_exchange_from_a_clean_start(transaction)
    path, _, query = transaction["fullPath"].partition("?")
    parent, _, contact_id = path.rpartition("/")
    account_id = parent.split("/")[-2]
    if not positive_id(contact_id) or contact_id == "2147483647":
        return
    if not positive_id(account_id) or account_id == "2147483647":
        return
    if " > 404" in transaction.get("name", ""):
        return
    contact = fixture_call(transaction, "POST", parent, OWN_CONTACT)
    transaction["fullPath"] = (
        parent + "/" + str(contact["id"]) + ("?" + query if query else "")
    )


@hooks.before("createContact")
def remove_a_previous_contact_probe(transaction):
    # Coverage calls before hooks only. Claim an absent email before its first
    # create, then remove only that probe's contact when generated bodies repeat.
    # Existing contacts and all generated request bytes stay untouched, but
    # for a later run's leftovers (a_leftover_to_remove).
    # After a reset (the own-data phase) the create is sent to the seeded
    # account, where no earlier probe's contact is left, so
    # created_contact_probes, which the reset clears, starts empty.
    a_contact_exchange_from_a_clean_start(transaction)
    from urllib.error import HTTPError
    from urllib.parse import urlencode

    body = hooks.get_json(transaction)
    email = body.get("email") if isinstance(body, dict) else None
    parent = transaction["fullPath"].split("?")[0]
    account_id = parent.split("/")[-2]
    if (
        not isinstance(email, str)
        or not email
        or not account_id.isdecimal()
        or int(account_id) < 1
    ):
        return
    origin = "{}//{}:{}".format(
        transaction["protocol"],
        transaction["host"],
        transaction["port"],
    )
    headers = {"Authorization": "Bearer " + admin_token(origin)}
    key = origin + parent + "\n" + email
    owned = hooks.stash.get("created_contact_probes", {})
    try:
        contact = find_in_list(
            origin,
            parent + "?" + urlencode({"search": email}),
            headers,
            lambda item: item.get("email") == email,
        )
    except HTTPError as error:
        if error.code in (400, 403, 404):
            return  # Leave an invalid account ID to the actual probe.
        raise
    if contact is None:
        owned[key] = True
        hooks.stash["created_contact_probes"] = owned
        return
    if key not in owned and not a_leftover_to_remove(transaction):
        return
    request = urllib.request.Request(
        origin + parent + "/" + str(contact["id"]),
        headers=headers,
        method="DELETE",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        if response.status != 204:
            raise RuntimeError("could not remove the previous contact probe")


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
    # After a reset (the own-data phase) the add is sent to a domain of its own
    # (on_a_domain_of_its_own), where no earlier probe's entry is left, so
    # created_sender_rule_probes, which the reset clears, starts empty.
    on_a_domain_of_its_own(transaction)
    body = hooks.get_json(transaction)
    pattern = body.get("pattern") if isinstance(body, dict) else None
    path = sender_rules_path(transaction)
    if not isinstance(pattern, str) or not pattern or path is None:
        return
    origin = "{}//{}:{}".format(
        transaction["protocol"],
        transaction["host"],
        transaction["port"],
    )
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
        origin + path + "/" + str(rule["id"]), headers=headers, method="DELETE"
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        if response.status != 204:
            raise RuntimeError("could not remove the previous sender rule probe")


def on_a_domain_of_its_own(transaction):
    """With a reset asked for, reset before this sender rule exchange, refusals
    included, and send one aimed at a positive domain id other than missing's
    2147483647 to a domain of its own: the reset removed the domain
    createDomain's link names. A refusal for its body or entry is then refused
    for that, not for a domain that is gone."""
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    parts = transaction["fullPath"].partition("?")[0].split("/")
    if len(parts) > 5 and positive_id(parts[5]) and parts[5] != "2147483647":
        to_domain(transaction, a_domain_of_its_own(transaction))


@hooks.before("listAllowlist")
@hooks.before("listBlocklist")
def list_sender_rules_from_a_clean_start(transaction):
    # The list needs nothing in it: after a reset it reads a domain of its own.
    on_a_domain_of_its_own(transaction)


# The entry removeFromAllowlist and removeFromBlocklist are sent to when the id
# they carry has none: the add's link names one that a probe repeating its
# pattern or the documented remove may since have removed, and a reset removes
# it too.
OWN_SENDER_RULE = {"pattern": "@contract-own-rule.test"}


@hooks.before("removeFromAllowlist")
@hooks.before("removeFromBlocklist")
def on_a_sender_rule_of_its_own(transaction):
    # Each exchange the link (or coverage's placeholder 1) reaches is sent to an
    # entry that exists: after a reset always one of its own, on a domain of its
    # own, and otherwise one of its own only when the list has no entry with the
    # id it carries. The refusals keep their ids: missing's 2147483647, a
    # non-positive or undecodable id, every exchange documented to answer 404,
    # and a domain that is not there (left to the probe).
    on_a_domain_of_its_own(transaction)
    path, _, query = transaction["fullPath"].partition("?")
    parent, _, entry_id = path.rpartition("/")
    if not positive_id(entry_id) or entry_id == "2147483647":
        return
    if " > 404" in transaction.get("name", ""):
        return
    if sender_rules_path({"fullPath": parent}) is None:
        return
    origin = origin_of(transaction)
    rules = sender_rules(origin, parent, {"Authorization": "Bearer " + admin_token(origin)})
    if rules is None:
        return
    if any(str(r.get("id")) == entry_id for r in rules):
        return
    rule = next((r for r in rules if r.get("pattern") == OWN_SENDER_RULE["pattern"]), None)
    if rule is None:
        rule = fixture_call(transaction, "POST", parent, OWN_SENDER_RULE)
    transaction["fullPath"] = (
        parent + "/" + str(rule["id"]) + ("?" + query if query else "")
    )


def hosted_domain_id(origin, headers, name):
    """The id of the domain called name, hosting it first if need be, or None
    when the server will not host it (left to the probe)."""
    domain = find_in_list(
        origin, "/api/v1/admin/domains", headers,
        lambda hosted: domain_key(hosted.get("name") or "") == domain_key(name),
    )
    if domain is not None:
        return domain["id"]
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
    an_active_domain(transaction, name)


@hooks.before("restmailDiscover")
def an_active_domain_for_discovery_coverage(transaction):
    # Coverage's valid probe generates a domain from the schema and reads any 4xx
    # as a disagreement, but a schema cannot say which domains are hosted. Host the
    # generated one without changing the query. Only coverage: fuzz draws its own
    # names, and hosting them would hide what it finds.
    if not in_the_coverage_process():
        return
    query = urllib.parse.parse_qs(urllib.parse.urlsplit(transaction["fullPath"]).query)
    name = query.get("domain", [""])[0]
    if "." not in name:
        return  # Leave what the schema forbids to the actual probe.
    an_active_domain(transaction, name)


def an_active_domain(transaction, name):
    """Host the domain called name and make it active, unless the server will not
    host it, which is left to the probe."""
    origin = "{}//{}:{}".format(
        transaction["protocol"],
        transaction["host"],
        transaction["port"],
    )
    headers = {
        "Content-Type": "application/json",
        "Authorization": "Bearer " + admin_token(origin),
    }
    domain_id = hosted_domain_id(origin, headers, name)
    if domain_id is None:
        return
    path = origin + "/api/v1/admin/domains/" + str(domain_id)
    with urllib.request.urlopen(
        urllib.request.Request(path, headers=headers), timeout=10
    ) as response:
        domain = json.load(response).get("data") or {}
    if domain.get("active"):
        return
    activate = urllib.request.Request(
        path, data=b'{"active":true}', headers=headers, method="PATCH"
    )
    try:
        with urllib.request.urlopen(activate, timeout=10):
            return
    except urllib.error.HTTPError as error:
        if (
            error.code != 409
            or (json.load(error).get("error") or {}).get("code")
            != "certificate_required"
        ):
            raise
    # Activation uses the production certificate gate. Supply a real fixture
    # certificate only when that precondition is missing, then activate it.
    with tempfile.TemporaryDirectory() as directory:
        key, cert = (
            os.path.join(directory, "tls.key"),
            os.path.join(directory, "tls.crt"),
        )
        openssl(
            "req",
            "-x509",
            "-newkey",
            "rsa:2048",
            "-nodes",
            "-days",
            "30",
            "-subj",
            "/CN=" + name,
            "-addext",
            "subjectAltName=DNS:" + name,
            "-keyout",
            key,
            "-out",
            cert,
        )
        with open(cert) as cert_file, open(key) as key_file:
            body = {
                "domain_id": domain_id,
                "cert_pem": cert_file.read(),
                "key_pem": key_file.read(),
                "auto_renew": False,
            }
        request = urllib.request.Request(
            origin + "/api/v1/admin/certificates",
            data=json.dumps(body).encode(),
            headers=headers,
            method="POST",
        )
        with urllib.request.urlopen(request, timeout=10):
            pass
    with urllib.request.urlopen(activate, timeout=10):
        pass


def alias_on(origin, headers, domain_id, source):
    return find_in_list(
        origin,
        f"/api/v1/admin/aliases?domain_id={domain_id:d}",
        headers,
        lambda alias: address_key(alias.get("source_address") or "") == address_key(source),
    )


@hooks.before("createAlias")
def remove_a_previous_alias_probe(transaction):
    # Coverage probes alias an address on a domain the fixture does not host
    # (someone@example.com), which the handler rightly refuses with 404, and
    # repeat it, which meets the alias an earlier probe made (409). Host the
    # domain, claim an absent source before its first create, and remove only
    # that probe's alias when generated bodies repeat it. Existing aliases and
    # all generated request bytes stay untouched, but for a later run's
    # leftovers (a_leftover_to_remove).
    # After a reset (the own-data phase) no earlier probe's alias is left, so
    # created_alias_probes, which the reset clears, starts empty.
    if asked_for_a_clean_start():
        start_clean(transaction)
    body = hooks.get_json(transaction)
    source = body.get("source_address") if isinstance(body, dict) else None
    if not isinstance(source, str) or source.count("@") != 1:
        return
    local, domain = source.split("@")
    if not local or "." not in domain:
        return  # Leave a malformed address to the actual probe.
    # domain_missing's domain is one no one hosts, so it stays unhosted.
    if source.lower() == "sales@missing.test":
        return
    origin = "{}//{}:{}".format(
        transaction["protocol"],
        transaction["host"],
        transaction["port"],
    )
    headers = {"Authorization": "Bearer " + admin_token(origin)}
    domain_id = hosted_domain_id(origin, headers, domain.lower())
    if domain_id is None:
        return
    alias = alias_on(origin, headers, domain_id, source)
    # duplicate's source already has an alias: make it first, and keep it.
    if source.lower() == "duplicate@restmail.test":
        if alias is None:
            request = urllib.request.Request(
                origin + "/api/v1/admin/aliases",
                data=json.dumps({"source_address": source, "destination_address": "new@destination.test"}).encode(),
                headers=dict(headers, **{"Content-Type": "application/json"}),
                method="POST",
            )
            with urllib.request.urlopen(request, timeout=10):
                pass
        return
    owned = hooks.stash.get("created_alias_probes", {})
    key = origin + "\n" + address_key(source)
    if alias is None:
        owned[key] = True
        hooks.stash["created_alias_probes"] = owned
        return
    if key not in owned and not a_leftover_to_remove(transaction):
        return
    request = urllib.request.Request(
        origin + "/api/v1/admin/aliases/{:d}".format(alias["id"]),
        headers=headers,
        method="DELETE",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        if response.status != 204:
            raise RuntimeError("could not remove the previous alias probe")


# The alias getAlias, updateAlias and deleteAlias are sent to when the id they
# carry has none: createAlias's link names one that a probe or the documented
# delete may since have removed, and a reset removes it too.
OWN_ALIAS = "contract-own-alias@"


def an_alias_of_its_own(transaction):
    """An alias made for this exchange. After a reset it is on a domain of its
    own; otherwise it is the one at OWN_ALIAS on restmail.test, made if absent,
    so nothing is carried between exchanges."""
    if asked_for_a_clean_start():
        a_domain_of_its_own(transaction)
        return fixture_call(
            transaction,
            "POST",
            "/api/v1/admin/aliases",
            {"source_address": OWN_ALIAS + OWN_DOMAIN, "destination_address": ADMIN},
        )["id"]
    origin = origin_of(transaction)
    headers = {"Authorization": "Bearer " + admin_token(origin)}
    source = OWN_ALIAS + "restmail.test"
    alias = alias_on(origin, headers, hosted_domain_id(origin, headers, "restmail.test"), source)
    if alias is None:
        alias = fixture_call(
            transaction,
            "POST",
            "/api/v1/admin/aliases",
            {"source_address": source, "destination_address": ADMIN},
        )
    return alias["id"]


@hooks.before("getAlias")
@hooks.before("updateAlias")
@hooks.before("deleteAlias")
def on_an_alias_of_its_own(transaction):
    # Each exchange the link (or coverage's placeholder 1) reaches is sent to an
    # alias that exists: after a reset always one of its own, and otherwise one
    # of its own only when no alias has the id it carries. The refusals keep
    # their ids: missing's 2147483647, a non-positive or undecodable id, and
    # every exchange documented to answer 404.
    if asked_for_a_clean_start():
        start_clean(transaction)
    path, _, query = transaction["fullPath"].partition("?")
    parent, _, alias_id = path.rpartition("/")
    if not positive_id(alias_id) or alias_id == "2147483647":
        return
    if " > 404" in transaction.get("name", ""):
        return
    if not asked_for_a_clean_start() and fixture_call(
        transaction, "GET", "/api/v1/admin/aliases/" + alias_id, expect=(404,)
    ):
        return
    transaction["fullPath"] = (
        parent + "/" + str(an_alias_of_its_own(transaction)) + ("?" + query if query else "")
    )


def a_mailbox_at(transaction, address):
    """Host address's domain and give address a mailbox, through the admin
    API, once per run. A malformed address, or a domain the server will not
    host, is left alone for the request itself to meet."""
    if not isinstance(address, str) or address.count("@") != 1:
        return
    local, domain = address.split("@")
    origin = "{}//{}:{}".format(
        transaction["protocol"],
        transaction["host"],
        transaction["port"],
    )
    ready = hooks.stash.get("probe_mailboxes", {})
    if not local or "." not in domain or (origin + "\n" + address.lower()) in ready:
        return
    headers = {
        "Content-Type": "application/json",
        "Authorization": "Bearer " + admin_token(origin),
    }

    def call(method, path, body=None):
        data = json.dumps(body).encode() if body is not None else None
        request = urllib.request.Request(
            origin + path, data=data, headers=headers, method=method
        )
        with urllib.request.urlopen(request, timeout=10) as response:
            return json.load(response).get("data")

    hosted = find_in_list(origin, "/api/v1/admin/domains", headers, named(domain))
    domain_id = hosted["id"] if hosted else None
    if domain_id is None:
        try:
            domain_id = call(
                "POST",
                "/api/v1/admin/domains",
                {"name": domain, "server_type": "traditional"},
            )["id"]
        except urllib.error.HTTPError as error:
            if 400 <= error.code < 500:
                return
            raise
    if not find_in_list(
        origin,
        f"/api/v1/admin/mailboxes?domain_id={domain_id:d}",
        headers,
        addressed(address),
    ):
        call("POST", "/api/v1/accounts", {"address": address})
    ready[origin + "\n" + address.lower()] = True
    hooks.stash["probe_mailboxes"] = ready


@hooks.before("saveDraft")
def a_mailbox_for_the_draft(transaction):
    # After a reset (the own-data phase) every save, refusals included, starts
    # clean, and the sender's mailbox is made again after it: the reset cleared
    # probe_mailboxes along with the mailboxes it named.
    if asked_for_a_clean_start():
        start_clean(transaction)
    # Coverage probes save drafts from a generated address
    # (someone@example.com) that no mailbox holds, which the handler rightly
    # refuses (403). The sender gets a hosted domain and a mailbox the admin
    # created; the request's bytes and credentials stay as generated.
    body = hooks.get_json(transaction)
    if isinstance(body, dict):
        a_mailbox_at(transaction, body.get("from"))


def account_with_address(origin, headers, domain_id, address):
    """The domain's account with this address, or None.

    The list answers one page, 50 rows unless asked and 200 at most, in address
    order. The E2E suite's accounts and the run's invited-* ones put
    new-account@restmail.test past the first page by the ownership run, so a
    look at that page alone called it absent and its create answered 409.
    """
    offset = 0
    while True:
        request = urllib.request.Request(
            origin
            + f"/api/v1/admin/mailboxes?domain_id={domain_id:d}&limit=200&offset={offset:d}",
            headers=headers,
        )
        with urllib.request.urlopen(request, timeout=10) as response:
            page = json.load(response)
        accounts = page.get("data") or []
        for account in accounts:
            if (account.get("address") or "").lower() == address.lower():
                return account
        offset += len(accounts)
        if not accounts or not (page.get("pagination") or {}).get("has_more"):
            return None


@hooks.before("createAccount")
def host_the_account_probe_domain(transaction):
    # Coverage probes address a domain the fixture does not host
    # (someone@example.com), which the handler rightly refuses with 422.
    # Host the domain, claim an absent address before its first create, and
    # remove only that probe's account when generated bodies repeat it.
    # Existing accounts and all generated request bytes stay untouched, but
    # for a later run's leftovers (a_leftover_to_remove).
    from urllib.error import HTTPError

    body = hooks.get_json(transaction)
    address = body.get("address") if isinstance(body, dict) else None
    if not isinstance(address, str) or address.count("@") != 1:
        return
    local, domain = address.split("@")
    if not local or "." not in domain:
        return  # Leave a malformed address to the actual probe.
    origin = "{}//{}:{}".format(
        transaction["protocol"],
        transaction["host"],
        transaction["port"],
    )
    if address.lower() == "someone@example.com":
        # A later create can replace the account whose credential/id the link
        # hook prepared. Rebuild its proof before the next generated link.
        hooks.stash.get("link_fixture_prepared", {}).pop(origin, None)
        hooks.stash.get("link_fixture_ids", {}).pop(
            origin + "\n" + address.lower(), None
        )
    headers = {
        "Content-Type": "application/json",
        "Authorization": "Bearer " + admin_token(origin),
    }
    hosted = find_in_list(
        origin, "/api/v1/admin/domains", headers, lambda d: d.get("name") == domain
    )
    domain_id = hosted["id"] if hosted else None
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
    account = account_with_address(origin, headers, domain_id, address)
    owned = hooks.stash.get("created_account_probes", {})
    key = origin + "\n" + address.lower()
    if account is None:
        owned[key] = True
        hooks.stash["created_account_probes"] = owned
        return
    if key not in owned and not a_leftover_to_remove(transaction):
        return
    # Deleting an account always takes a session signed in with 2FA.
    request = urllib.request.Request(
        origin + "/api/v1/admin/mailboxes/{:d}".format(account["id"]),
        headers={"Authorization": "Bearer " + mfa_admin_token(origin)},
        method="DELETE",
    )
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            if response.status != 204:
                raise RuntimeError("could not remove the previous account probe")
    except urllib.error.HTTPError as error:
        raise RuntimeError(
            "could not remove the previous account probe: {} {}".format(
                error.code, error.read()[:300].decode(errors="replace")
            )
        ) from error


@hooks.after("createAccount")
def keep_the_created_account(transaction):
    # Only the example runs after hooks, and its id is what getMailbox,
    # updateMailbox and deleteMailbox are linked to. Probes repeating its
    # address remove that account above, so remember what to restore.
    data = hooks.get_json(transaction, "real").get("data") or {}
    if not data.get("id"):
        return
    hooks.stash["created_account"] = {
        "id": data["id"],
        "body": hooks.get_json(transaction),
    }


@hooks.before("updateMailbox")
def a_new_password_only_on_the_created_account(transaction):
    """A request that sets a password goes to the account the run made for this,
    and to no other. updated_password_at_minimum names no account, so a phase sends
    it to its in-range id, 1, a fixture account the run did not make, and
    fuzz.pin-valid in vertrag.yml puts a password in every valid body a generating
    phase draws. On id 1 it would replace that account's password and end its
    sessions. A probe of the path keeps its id: its body is the operation's first
    200 example, updated, which sets no password (TestMailboxUpdate_ExamplePairs)."""
    body = hooks.get_json(transaction)
    if not isinstance(body, dict) or not isinstance(body.get("password"), str):
        return
    if not body["password"] or len(body["password"]) < 12:
        return  # Refused for its length, before it can change anything.
    if asked_for_a_clean_start():
        # The own-data run: the reset removed the created account, and
        # a_mailbox_to_administer, registered first, has already sent every
        # positive id but missing's to an account made for this exchange. The
        # password goes to that account, so the exchange is sent as it is.
        return
    created = hooks.stash.get("created_account")
    if not created:
        transaction["skip"] = True
        return
    path, _, query = transaction["fullPath"].partition("?")
    parent, _, account_id = path.rpartition("/")
    if account_id.isdigit() and int(account_id) > 0:  # An invalid id is the probe.
        transaction["fullPath"] = parent + "/" + str(created["id"]) + ("?" + query if query else "")


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
    origin = "{}//{}:{}".format(
        transaction["protocol"],
        transaction["host"],
        transaction["port"],
    )
    # Making the account again takes a second factor.
    headers = {
        "Content-Type": "application/json",
        "Authorization": "Bearer " + mfa_admin_token(origin),
    }
    address = created["body"]["address"]
    domains = list_rows(origin, "/api/v1/admin/domains", headers)
    domain_id = next(d["id"] for d in domains if d.get("name") == address.split("@")[1])
    account = account_with_address(origin, headers, domain_id, address)
    if account is None:
        request = urllib.request.Request(
            origin + "/api/v1/accounts",
            data=json.dumps(created["body"]).encode(),
            headers=headers,
            method="POST",
        )
        with urllib.request.urlopen(request, timeout=10) as response:
            account = json.load(response)["data"]
    transaction["fullPath"] = (
        parent + "/" + str(account["id"]) + ("?" + query if query else "")
    )


def an_account_without_credentials(origin):
    """A fresh restmail.test account made without a password, so invitable."""
    return an_account_of_its_own(origin, "invited")


# The password of the account made for inviteAccount's existing_credentials
# example: the 409 needs one that holds a password, and the reset removed the
# fixture admin's id 1. Never sent anywhere else.
CREDENTIALED_PASSWORD = "velvet-tundra-compass-otter-47"


def an_account_of_its_own(origin, prefix, password=None):
    """A fresh restmail.test account, with a password only when one is given.
    Its address is named by the time it is made, not by a count kept in the
    stash, so nothing is carried between exchanges. restmail.test is seeded, so
    it is there after a reset too. Making one takes a two-factor session."""
    return a_new_account(origin, fresh_address(prefix), "Invited", password)


def fresh_address(prefix):
    """A restmail.test address no account has, named by the time."""
    return f"{prefix}-{time.time_ns():d}@restmail.test"


def a_new_account(origin, address, display_name, password=None):
    """Make the account, with a password only when one is given, and return its
    id. Making one takes a two-factor session."""
    body = {"address": address, "display_name": display_name}
    if password is not None:
        body["password"] = password
    request = urllib.request.Request(
        origin + "/api/v1/accounts",
        data=json.dumps(body).encode(),
        headers={
            "Content-Type": "application/json",
            "Authorization": "Bearer " + mfa_admin_token(origin),
        },
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        return json.load(response)["data"]["id"]


def a_made_account(origin, prefix, password=None):
    """an_account_of_its_own, answering the account as made: its id and address."""
    address = fresh_address(prefix)
    return {"id": a_new_account(origin, address, "Invited", password), "address": address}


# The passwords of the accounts made for recoverAccount's recovered example,
# before and after the recovery. Not secrets: each proves only a disposable
# account made for one request (a_recoverable_account).
RECOVERABLE_PASSWORD = "quartz-meadow-lantern-badger-68"
RECOVERED_PASSWORD = "cobalt-fjord-marmot-orbit-92"


def a_recoverable_account(origin):
    """A fresh account enrolled in TOTP through the public API, for one
    recovery: its address and the second factor (secret and recovery codes)."""
    address = fresh_address("recoverable")
    a_new_account(origin, address, "Recoverable", RECOVERABLE_PASSWORD)
    return address, mfa_admin.enroll_account(origin, address, RECOVERABLE_PASSWORD)


@hooks.before("recoverAccount")
def recover_an_account_of_its_own(transaction):
    # The recovered example (state recoverable_account) names the fixture
    # admin and placeholder codes; recovering it would set the fixture admin's
    # password. Each request expecting the recovery, every principal's in the
    # authorization phase, gets an account made for it alone, enrolled just
    # before, so the run's own accounts are never touched. The codes are
    # one-time: the authenticator's current code and an unspent recovery code.
    # Refusals keep their documented bodies.
    if str(transaction.get("expected", {}).get("statusCode", "")) != "200":
        return
    body = hooks.get_json(transaction)
    if not isinstance(body, dict):
        return
    address, factor = a_recoverable_account(origin_of(transaction))
    body.update({
        "email": address,
        "totp_code": totp(factor["secret"]),
        "recovery_code": factor["recovery_codes"][0],
        "new_password": RECOVERED_PASSWORD,
    })
    hooks.set_json(transaction, body)
    # Making the account spent the auth bucket pace_auth_requests waited for.
    rate_limits.pace(1.1)


# The password resetAdmin's reset example sets on the account made for it. Not
# a secret: it proves only a disposable account (an_owner_reset_target).
OWNER_RESET_PASSWORD = "saffron-glacier-kestrel-violet-73"


def an_owner_reset_target(origin):
    """A fresh account with no credentials, for one owner reset: the examples
    name the run's own admin, whose reset would end its sessions."""
    address = fresh_address("owner-reset")
    a_new_account(origin, address, "Owner reset")
    return address


@hooks.before("ownerStatus")
def sign_the_status_with_the_runs_owner_key(transaction):
    # The status example (state owner_key) signs a placeholder challenge with a
    # placeholder key. Each request expecting the 200, every principal's in the
    # authorization phase, gets a fresh challenge signed by the owner key the
    # run registered (owner_key.py). The principal's credential is untouched;
    # refusals keep their documented bodies.
    if str(transaction.get("expected", {}).get("statusCode", "")) != "200":
        return
    body = hooks.get_json(transaction)
    if not isinstance(body, dict):
        return
    body.update(owner_key.signed_answer(origin_of(transaction), "status"))
    hooks.set_json(transaction, body)
    # The challenge spent the owner bucket pace_owner_requests waited for.
    rate_limits.pace(1.1)


@hooks.before("ownerToken")
def mint_for_an_account_of_its_own(transaction):
    # The minted example names the run's admin. Each request expecting the 200
    # names an account made for it instead, signed by the run's owner key, so
    # no token for one of the run's own accounts is ever minted.
    if str(transaction.get("expected", {}).get("statusCode", "")) != "200":
        return
    body = hooks.get_json(transaction)
    if not isinstance(body, dict):
        return
    origin = origin_of(transaction)
    address = an_owner_reset_target(origin)
    body.update({"purpose": "reset-admin", "address": address,
                 **owner_key.signed_answer(origin, "reset-admin", address)})
    hooks.set_json(transaction, body)
    rate_limits.pace(1.1)


@hooks.before("resetAdmin")
def reset_an_account_of_its_own(transaction):
    # The reset example's token is a placeholder, and a real reset of the run's
    # admin would end its sessions and strip its second factor. Each request
    # expecting the 200 redeems a token the run's owner key minted for an
    # account made for it alone. The token is one-time: the reset spends it.
    if str(transaction.get("expected", {}).get("statusCode", "")) != "200":
        return
    body = hooks.get_json(transaction)
    if not isinstance(body, dict):
        return
    origin = origin_of(transaction)
    address = an_owner_reset_target(origin)
    body.update({"token": owner_key.reset_token(origin, address),
                 "password": OWNER_RESET_PASSWORD, "client": "native"})
    hooks.set_json(transaction, body)
    # Making the account may have signed the two-factor admin in, which spends
    # the auth bucket pace_auth_requests waited for.
    rate_limits.pace(1.1)


# The owner refusals that also require owner_key carry the placeholder
# challenge, which the server checks before the key and the account: sent as
# documented with a key registered, each would answer 401 invalid_challenge.
# Each gets a real fresh challenge, so it reaches the check it documents.
def _owner_refusal(transaction, status):
    """The request's body, when it expects the refusal status and is an object."""
    if str(transaction.get("expected", {}).get("statusCode", "")) != status:
        return None
    body = hooks.get_json(transaction)
    return body if isinstance(body, dict) else None


@hooks.before("ownerStatus")
def refuse_a_status_key_that_is_not_listed(transaction):
    # not_status_key (403): a valid challenge signed by a key made for this
    # request and listed nowhere, neither a status key nor the run's owner key.
    body = _owner_refusal(transaction, "403")
    if body is None:
        return
    body.update(owner_key.unlisted_answer(origin_of(transaction), "status"))
    hooks.set_json(transaction, body)
    # The challenge spent the owner bucket pace_owner_requests waited for.
    rate_limits.pace(1.1)


@hooks.before("ownerToken")
def refuse_a_token_to_a_key_that_is_not_an_owner(transaction):
    # not_owner (403): the purpose and address the example names, signed over
    # a valid challenge by a key made for this request and registered nowhere.
    # The key is refused before the account is looked at, so nothing is minted.
    body = _owner_refusal(transaction, "403")
    if body is None:
        return
    body.update(owner_key.unlisted_answer(
        origin_of(transaction), body.get("purpose", ""), body.get("address", "")))
    hooks.set_json(transaction, body)
    rate_limits.pace(1.1)


@hooks.before("ownerToken")
def refuse_a_token_for_an_account_that_does_not_exist(transaction):
    # no_account (404): the run's owner key signs a valid challenge for an
    # address made for this request and given to no account, so the refusal
    # is the account's. A fresh address, not the example's, so no account that
    # happens to exist under it could ever be minted a token.
    body = _owner_refusal(transaction, "404")
    if body is None:
        return
    address = fresh_address("no-account")
    body.update({"purpose": "reset-admin", "address": address,
                 **owner_key.signed_answer(origin_of(transaction), "reset-admin", address)})
    hooks.set_json(transaction, body)
    rate_limits.pace(1.1)


# The recipient each delivery and mailbox check success names, by operation:
# a hosted mailbox the reset keeps (the gateway's found example names the
# in-process fixture's user@example.com, which look_up_a_hosted_mailbox sends
# to the fixture admin). After a reset the documented success goes to a
# mailbox of its own instead, made then. The RESTMAIL check's 200 is sent
# whatever address Vertrag gives it, user@example.test today, which no mailbox
# has: None takes any one address.
HOSTED_RECIPIENT = {
    "checkMailboxAddress": "user@example.com",
    "deliverMessage": ADMIN,
    "restmailCheckMailbox": None,
    "restmailDeliver": "eve@restmail.test",
}

# The status of each operation's documented success.
DELIVERY_SUCCESS = {
    "checkMailboxAddress": "200",
    "deliverMessage": "201",
    "restmailCheckMailbox": "200",
    "restmailDeliver": "201",
}


def operation_of(transaction):
    """Which delivery or mailbox check operation a transaction is, by its path."""
    path = transaction["fullPath"].partition("?")[0]
    return {
        "/api/mailboxes": "checkMailboxAddress",
        "/api/v1/messages/deliver": "deliverMessage",
        "/restmail/mailboxes": "restmailCheckMailbox",
        "/restmail/messages": "restmailDeliver",
    }.get(path)


def to_a_recipient_of_its_own(transaction, operation):
    """Send a documented success to a mailbox made for it after the reset, and
    say which (its id and address), or None when the exchange names another
    recipient (a refusal's, or a missing mailbox's)."""
    hosted = HOSTED_RECIPIENT[operation]
    path, _, query = transaction["fullPath"].partition("?")
    if operation in ("checkMailboxAddress", "restmailCheckMailbox"):
        pairs = urllib.parse.parse_qsl(query, keep_blank_values=True)
        if len(pairs) != 1 or pairs[0][0] != "address" or pairs[0][1] != (hosted or pairs[0][1]):
            return None
        made = a_recipient_of_its_own(transaction)
        transaction["fullPath"] = path + "?" + urllib.parse.urlencode({"address": made["address"]})
        return made
    body = hooks.get_json(transaction)
    if not isinstance(body, dict):
        return None
    if operation == "deliverMessage":
        if body.get("address") != hosted or "raw_message" in body:
            return None
        made = a_recipient_of_its_own(transaction)
        body["address"] = made["address"]
    else:
        if body.get("to") != [hosted]:
            return None
        made = a_recipient_of_its_own(transaction)
        body["to"] = [made["address"]]
        an_inbound_pipeline_without_greylisting(transaction)
    hooks.set_json(transaction, body)
    return made


def a_recipient_of_its_own(transaction):
    """A fresh restmail.test mailbox, made on the public listener. Its address
    is named by the time it is made, like an_account_of_its_own's."""
    origin = setup_origin(transaction)
    address = f"contract-own-recipient-{time.time_ns():d}@restmail.test"
    request = urllib.request.Request(
        origin + "/api/v1/accounts",
        data=json.dumps({"address": address, "display_name": "Recipient"}).encode(),
        headers={
            "Content-Type": "application/json",
            "Authorization": "Bearer " + mfa_admin_token(origin),
        },
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        data = json.load(response)["data"]
    if type(data.get("id")) is not int or str(data.get("address", "")).lower() != address:
        raise RuntimeError(f"the recipient was not made as asked: {data!r}")
    return {"id": data["id"], "address": address}


def an_inbound_pipeline_without_greylisting(transaction):
    """An active inbound pipeline for restmail.test that only validates the
    headers. After a reset the domain has none, and the default one greylists:
    a sender, recipient and address it has not seen are deferred 503 for five
    minutes, so a RESTMAIL delivery from a new mailbox could never succeed."""
    origin = setup_origin(transaction)
    headers = {"Content-Type": "application/json", "Authorization": "Bearer " + admin_token(origin)}
    request = urllib.request.Request(origin + "/api/v1/admin/domains?limit=200", headers=headers)
    with urllib.request.urlopen(request, timeout=10) as response:
        domains = json.load(response).get("data") or []
    domain = next((d for d in domains if d.get("name") == "restmail.test"), None)
    if domain is None:
        raise RuntimeError("the reset kept no restmail.test domain")
    request = urllib.request.Request(
        origin + "/api/v1/admin/pipelines",
        data=json.dumps(
            {
                "domain_id": domain["id"],
                "direction": "inbound",
                "filters": [{"name": "header_validate", "config": {}}],
                "active": True,
            }
        ).encode(),
        headers=headers,
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=10):
        pass


@hooks.before("checkMailboxAddress")
@hooks.before("deliverMessage")
@hooks.before("restmailDiscover")
@hooks.before("restmailCapabilities")
@hooks.before("restmailCheckMailbox")
@hooks.before("restmailDeliver")
def delivery_after_a_reset(transaction):
    # The delivery and federation operations, moved to a clean start: each
    # exchange, refusals included, resets and seeds first, on the public
    # listener (setup_origin). A documented success of a delivery or a mailbox
    # check is sent to a mailbox of its own, made after the reset, and its
    # after hook checks the answer names that mailbox. Refusals keep what they
    # name: an empty or missing address, nobody@restmail.test, missing's
    # 2147483647, unavailable@example.com. Discovery asks about the seeded
    # restmail.test, and the capabilities need nothing made. A RESTMAIL
    # delivery also gets restmail.test an inbound pipeline that does not
    # greylist, since the default one defers a new sender.
    # Registered before the other hooks of these operations, so they see the
    # clean start and the new recipient. A run with no reset changes nothing.
    if not asked_for_a_clean_start():
        return
    start_clean(transaction)
    operation = operation_of(transaction)
    if operation is None or expected_status(transaction) != DELIVERY_SUCCESS[operation]:
        return
    made = to_a_recipient_of_its_own(transaction, operation)
    if made is not None:
        hooks.stash["recipient_made"] = dict(made, name=transaction.get("name", ""))


@hooks.before("restmailDeliver")
def primary_restmail_delivery_fixture(transaction):
    if asked_for_a_clean_start() or expected_status(transaction) != DELIVERY_SUCCESS["restmailDeliver"]:
        return
    made = to_a_recipient_of_its_own(transaction, "restmailDeliver")
    if made is not None:
        hooks.stash["recipient_made"] = dict(made, name=transaction.get("name", ""))


@hooks.after("checkMailboxAddress")
@hooks.after("deliverMessage")
@hooks.after("restmailCheckMailbox")
@hooks.after("restmailDeliver")
def the_answer_names_the_recipient_made(transaction):
    # After a reset a documented success must answer for the mailbox made for
    # it, so the status and the schema alone do not pass on another mailbox or
    # on none: a check that it exists, with its address (and, from the
    # gateway's check, its id), a gateway delivery stored in it, a RESTMAIL
    # delivery that names it delivered and nothing failed. A documented success
    # with no mailbox made for it fails too. Refusals and a run with no reset
    # are left alone; a status other than the one documented fails on its own.
    made = hooks.stash.pop("recipient_made", None)
    operation = operation_of(transaction)
    real = transaction.get("real") or {}
    if (
        (not asked_for_a_clean_start() and made is None)
        or operation is None
        or expected_status(transaction) != DELIVERY_SUCCESS[operation]
        or str(real.get("statusCode")) != DELIVERY_SUCCESS[operation]
    ):
        return
    if made is None or made["name"] != transaction.get("name", ""):
        transaction["fail"] = "no mailbox was made for this exchange"
        return
    data = (hooks.get_json(transaction, "real") or {}).get("data")
    data = data if isinstance(data, dict) else {}
    address = made["address"]
    problems = []
    if operation in ("checkMailboxAddress", "restmailCheckMailbox"):
        if data.get("exists") is not True:
            problems.append(f"exists is {data.get('exists')!r}")
        if str(data.get("address", "")).lower() != address.lower():
            problems.append(f"the address is {data.get('address')!r}")
        if operation == "checkMailboxAddress" and data.get("mailbox_id") != made["id"]:
            problems.append(f"the mailbox id is {data.get('mailbox_id')!r}, not {made['id']:d}")
    elif operation == "deliverMessage":
        if data.get("mailbox_id") != made["id"]:
            problems.append(f"the message is in mailbox {data.get('mailbox_id')!r}, not {made['id']:d}")
    else:
        delivered = [str(a).lower() for a in data.get("delivered") or ()]
        if delivered != [address.lower()]:
            problems.append(f"delivered is {data.get('delivered')!r}")
        if data.get("failed"):
            problems.append(f"failed is {data.get('failed')!r}")
    if problems:
        transaction["fail"] = f"the answer is not for the mailbox made for it ({address}): " + "; ".join(problems)


# The address each check's found example names: the in-process fixtures' own
# mailboxes, which the topology does not have.
FOUND_ADDRESS = {
    "checkMailboxAddress": "user@example.com",
    "restmailCheckMailbox": "user@example.test",
}


@hooks.before("checkMailboxAddress")
@hooks.before("restmailCheckMailbox")
def look_up_a_hosted_mailbox(transaction):
    # found names an in-process fixture's mailbox; on the topology the hosted
    # mailbox is the fixture admin's, so found asks about that one, and
    # the_lookup_finds_the_hosted_mailbox checks the answer. Other values are
    # the refusals' and the probes', sent as they are. After a reset
    # delivery_after_a_reset has already sent found to a mailbox of its own.
    hooks.stash.pop("hosted_lookup", None)
    operation = operation_of(transaction)
    path, _, query = transaction["fullPath"].partition("?")
    pairs = urllib.parse.parse_qsl(query, keep_blank_values=True)
    if operation not in FOUND_ADDRESS or pairs != [("address", FOUND_ADDRESS[operation])]:
        return
    transaction["fullPath"] = path + "?" + urllib.parse.urlencode({"address": ADMIN})
    hooks.stash["hosted_lookup"] = transaction.get("name", "")


@hooks.after("checkMailboxAddress")
@hooks.after("restmailCheckMailbox")
def the_lookup_finds_the_hosted_mailbox(transaction):
    # A check's 200 has the same shape for a mailbox that is not there
    # (exists: false), so its status and schema alone pass on a miss (#1801).
    # The lookup look_up_a_hosted_mailbox sent to the fixture admin must
    # answer that it exists, with the admin's address. Other lookups are left
    # alone; a status other than 200 fails on its own.
    name = hooks.stash.pop("hosted_lookup", None)
    real = transaction.get("real") or {}
    if name is None or name != transaction.get("name", "") or str(real.get("statusCode")) != "200":
        return
    data = (hooks.get_json(transaction, "real") or {}).get("data")
    data = data if isinstance(data, dict) else {}
    problems = []
    if data.get("exists") is not True:
        problems.append(f"exists is {data.get('exists')!r}")
    if str(data.get("address", "")).lower() != ADMIN.lower():
        problems.append(f"the address is {data.get('address')!r}")
    if problems:
        transaction["fail"] = f"the check did not find the hosted mailbox {ADMIN}: " + "; ".join(problems)


@hooks.before("deliverMessage")
def deliver_a_whole_message(transaction):
    # The examples are the in-process fixture's: a few fields, from
    # sender@example.net. On the topology the inbound pipeline checks the
    # message as the gateway hands it over: header_validate wants its Date and
    # Message-ID, which only the raw message carries, and DMARC is checked
    # against a From domain that publishes p=reject. So a delivery the
    # examples make is sent as the gateway sends one, the whole message with
    # its fields, from a domain that publishes no policy. Refusals (an array
    # body, a refused folder, a missing mailbox) are answered before either.
    body = hooks.get_json(transaction)
    if not isinstance(body, dict) or body.get("sender") != "sender@example.net":
        return
    sender = body["sender"] = "sender@contract-sender.test"
    if "raw_message" not in body:
        stamp = time.time_ns()
        message = (
            f"From: {sender}\r\nTo: {body.get('address', '')}\r\n"
            f"Subject: {body.get('subject', '')}\r\n"
            f"Date: {formatdate(localtime=False)}\r\n"
            f"Message-ID: <contract-deliver-{stamp}@contract-sender.test>\r\n"
            "MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n"
            f"{body.get('body_text', '')}\r\n"
        )
        body["raw_message"] = base64.b64encode(message.encode()).decode()
    hooks.set_json(transaction, body)


@hooks.before("deleteQuarantine")
def delete_the_fixture_quarantined_message(transaction):
    if os.environ.get("CONTRACT_COVERAGE") == "1":
        return
    # found names message 41 of the in-process fixture's account 1. Mail is
    # quarantined by filters a healthy run does not trip, so the contract
    # fixtures hold a message for the fixture admin (prepareHeldMail); the
    # newest unreleased one is deleted, from the admin's own account. Other ids
    # are the refusals' and the probes', sent as they are.
    # Only the example's own path: a probe that changes either id is sent as it is.
    path, _, query = transaction["fullPath"].partition("?")
    address = session_address(transaction)
    if path != "/api/v1/accounts/1/quarantine/41" or not (
        address == ADMIN
        or (os.environ.get("CONTRACT_AUTHORIZATION") == "1" and address == mfa_admin.ADDRESS)
    ):
        return
    origin = origin_of(transaction)
    collection = "/api/v1/accounts/{}/quarantine".format(mailbox_id(origin, address))
    held = [
        m
        for m in list_rows(
            origin,
            collection,
            {"Authorization": transaction["request"]["headers"]["Authorization"]},
        )
        if not m.get("released")
    ]
    if not held:
        raise RuntimeError("the contract fixtures hold no quarantined message for the admin")
    newest = max(held, key=lambda m: m["id"])
    transaction["fullPath"] = collection + "/" + str(newest["id"]) + ("?" + query if query else "")


@hooks.before("clearSuppression")
def clear_the_fixture_suppression(transaction):
    if os.environ.get("CONTRACT_COVERAGE") == "1" or asked_for_a_clean_start():
        return
    # found names suppression 1 of domain 1. A recipient is suppressed only
    # when mail to it hard-bounces, which the run does not send, so the
    # contract fixtures suppress one on the fixture domain (prepareHeldMail);
    # the newest is cleared. Other ids are sent as they are.
    # Only the example's own path: a probe that changes either id is sent as it is.
    path, _, query = transaction["fullPath"].partition("?")
    if path != "/api/v1/admin/domains/1/suppressions/1" or not with_the_fixture_admins_session(transaction):
        return
    origin = origin_of(transaction)
    headers = {"Authorization": transaction["request"]["headers"]["Authorization"]}
    domain_id = hosted_domain_id(origin, headers, FIXTURE_PLAN["domain"])
    collection = "/api/v1/admin/domains/{}/suppressions".format(domain_id)
    request = urllib.request.Request(origin + collection, headers=headers)
    with urllib.request.urlopen(request, timeout=10) as response:
        suppressed = [s for s in json.load(response).get("data") or []
                      if s.get("address", "").startswith("contract-suppressed-")]
    if not suppressed:
        raise RuntimeError("the contract fixtures suppress no recipient on the fixture domain")
    newest = max(suppressed, key=lambda s: s["id"])
    transaction["fullPath"] = collection + "/" + str(newest["id"]) + ("?" + query if query else "")


@hooks.before("inviteAccount")
def invite_an_account_without_credentials(transaction):
    # created, existing_credentials and missing all name the placeholder id 12,
    # and differ only in the state of that account. Each is given an account in
    # its state: a fresh one made without a password, the fixture admin (1),
    # who has one, and an id no account holds. Coverage probes keep their ids.
    # After a reset (the own-data phase) every exchange, refusals included,
    # starts clean, and existing_credentials gets an account of its own that
    # holds a password: the reset removed the fixture admin's id 1.
    clean = asked_for_a_clean_start()
    if clean:
        start_clean(transaction)
    path, _, query = transaction["fullPath"].partition("?")
    mailboxes, placeholder, _ = path.rstrip("/").rsplit("/", 2)
    name = transaction.get("name", "")
    # The minimum-id probe of the 201 names the fixture admin (1), who holds a
    # password: its 409 is the rule, not a disagreement, and no account at id 1
    # can be invitable. TestInvitationCanonicalExamples proves both sides. A
    # probe runs under its transaction's own name (the "coverage:" prefix is
    # added to the report later), so the id is what tells it from the example.
    if placeholder == "1" and " > 201 > " in name and " as " not in name:
        transaction["skip"] = True
        return
    if placeholder != "12":
        return
    if " > 409 > " in name:
        account_id = 1
        if clean:
            account_id = an_account_of_its_own(
                origin_of(transaction), "credentialed", CREDENTIALED_PASSWORD
            )
    elif " > 404 > " in name:
        account_id = 2147483647
    elif " > 201 > " in name:
        account_id = an_account_without_credentials(origin_of(transaction))
    else:
        return
    transaction["fullPath"] = f"{mailboxes}/{account_id:d}/invitation" + (
        "?" + query if query else ""
    )


@hooks.before("acceptInvitation")
def redeem_a_fresh_invitation(transaction):
    # After a reset (the own-data phase) every exchange, refusals included,
    # starts clean. The accepted example still gets an account and invitation
    # of its own below; the refusals reach the handler unchanged.
    if asked_for_a_clean_start():
        start_clean(transaction)
    body = hooks.get_json(transaction)
    # The accepted example and every coverage probe of its 200 need an unspent
    # invitation: coverage runs no after hooks, and a schema-valid body with an
    # unknown token is a 401. The named refusals (invalid_token, invalid_field,
    # body_array) and probes whose token is not a string reach the handler
    # unchanged. A probe of the 200 that leaves the optional token out gets one
    # too, and one whose password the schema cannot hold to 12 bytes (the
    # handler's rule) gets the documented password.
    if (
        " > 200 > " not in transaction.get("name", "")
        or not isinstance(body, dict)
        or not isinstance(body.get("token", ""), str)
    ):
        return
    password = body.get("password")
    if not isinstance(password, str) or not 12 <= len(password.encode()) <= 72:
        body["password"] = "the recipient chooses this password"
    origin = "{}//{}:{}".format(
        transaction["protocol"],
        transaction["host"],
        transaction["port"],
    )
    account_id = an_account_without_credentials(origin)
    request = urllib.request.Request(
        origin + f"/api/v1/admin/mailboxes/{account_id:d}/invitation",
        headers={"Authorization": "Bearer " + mfa_admin_token(origin)},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        body["token"] = json.load(response)["data"]["token"]
    hooks.set_json(transaction, body)


# Refusals a coverage probe's own values provoke in a setup call (an id out of
# range, an address that is not one): the probe reaches the handler unchanged.
PROBE_REFUSALS = (400, 404, 409, 422)


def positive_id(text):
    """Whether a path segment is an id a documented example would carry."""
    return text.isdecimal() and int(text) > 0


def fixture_call(transaction, method, path, body=None, expect=(200, 201, 204), two_factor=False):
    """One admin API call for fixture setup, answering its JSON data, or None
    for an expected refusal such as a 404 on a delete of nothing. two_factor
    sends it as the two-factor administrator, which removing a grant needs."""
    origin = "{}//{}:{}".format(
        transaction["protocol"],
        transaction["host"],
        transaction["port"],
    )
    token = mfa_admin_token(origin) if two_factor else admin_token(origin)
    request = urllib.request.Request(
        origin + path,
        data=json.dumps(body).encode() if body is not None else None,
        headers={
            "Content-Type": "application/json",
            "Authorization": "Bearer " + token,
        },
        method=method,
    )
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            payload = response.read()
    except urllib.error.HTTPError as error:
        if error.code in expect:
            return None
        raise
    return json.loads(payload).get("data") if payload else None


@hooks.before("blockSender")
def block_a_new_and_an_existing_sender(transaction):
    # created (201) blocks an address the account has no contact for, and
    # found (200) one it already has. Neither holds after the other has run,
    # or after a previous run, so each is given its state. Other bodies and
    # coverage probes reach the handler unchanged. After a reset (the own-data
    # phase) every exchange, refusals included, starts clean first and is sent
    # to the seeded account, where created needs nothing removed.
    a_contact_exchange_from_a_clean_start(transaction)
    body = hooks.get_json(transaction)
    email = body.get("email") if isinstance(body, dict) else None
    name = transaction.get("name", "")
    contacts = transaction["fullPath"].split("?")[0].rsplit("/", 1)[0]
    if not contacts.endswith("/contacts"):
        return
    path = contacts[contacts.index("/api/") :]
    if email == "new@example.com" and " > 201 > " in name:
        origin = origin_of(transaction)
        try:
            contact = find_in_list(
                origin,
                path + "?" + urllib.parse.urlencode({"search": email}),
                {"Authorization": "Bearer " + admin_token(origin)},
                lambda item: item.get("email") == email,
            )
        except urllib.error.HTTPError as error:
            if error.code not in PROBE_REFUSALS:
                raise
            contact = None
        if contact is not None:
            fixture_call(
                transaction,
                "DELETE",
                path + "/" + str(contact["id"]),
                expect=PROBE_REFUSALS,
            )
    elif email == "existing@example.com" and " > 200 > " in name:
        fixture_call(transaction, "POST", path, {"email": email}, expect=PROBE_REFUSALS)


@hooks.before("createBan")
def ban_a_new_and_an_existing_ip(transaction):
    # created (201) bans an address no ban holds, and clear_expiry (200)
    # replaces the ban an address already has. Each is given that state; the
    # refusals and coverage probes reach the handler unchanged.
    body = hooks.get_json(transaction)
    ip = body.get("ip") if isinstance(body, dict) else None
    name = transaction.get("name", "")
    if not isinstance(ip, str) or not ip:
        return
    if " > 201 > " in name:
        fixture_call(
            transaction,
            "DELETE",
            "/api/v1/admin/bans/ip/" + urllib.parse.quote(ip, safe=""),
            expect=PROBE_REFUSALS,
        )
    elif " > 200 > " in name:
        fixture_call(
            transaction,
            "POST",
            "/api/v1/admin/bans",
            {
                "ip": ip,
                "reason": "Contract fixture",
                "protocol": "all",
                "duration": "1h",
                "created_by": ADMIN,
            },
            expect=PROBE_REFUSALS,
        )


@hooks.before("updateDraft")
def update_a_draft_of_its_own(transaction):
    # The authorization matrix does not send saveDraft's 201 (OWN_ACCOUNT), so
    # no link fills updateDraft's id and its cells carry the placeholder draft
    # 12, which no draft holds. The admin's cell documents the success, so it
    # gets a draft of the admin's own to update (#376). The other principals'
    # cells keep the id: 404 is their answer whatever it holds. Coverage's
    # placeholders are contact_draft_hooks.py's, and the ownership run's id is
    # the one its link filled.
    if (
        os.environ.get("CONTRACT_OWNERSHIP") == "1"
        or os.environ.get("CONTRACT_COVERAGE") == "1"
        or " > 200 > " not in transaction.get("name", "")
        or session_address(transaction) != ADMIN
    ):
        return
    path, _, query = transaction["fullPath"].partition("?")
    drafts, placeholder = path.rstrip("/").rsplit("/", 1)
    if placeholder != "12":
        return
    draft = fixture_call(
        transaction,
        "POST",
        "/api/v1/messages/draft",
        {
            "from": ADMIN,
            "to": [ADMIN],
            "subject": "Contract draft",
            "body_text": "Updated by the contract.",
        },
    )
    transaction["fullPath"] = f"{drafts}/{draft['id']:d}" + ("?" + query if query else "")


@hooks.before("requestRecoveryEmail")
def a_recovery_request_for_an_address_of_its_own(transaction):
    # The server allows five recovery requests an address every 15 minutes
    # (recovery_email.go), charged whoever sends them. Every run and every
    # principal repeats the documented address, so the sixth answered 429,
    # which says nothing about who may call it (#376). Each documented success
    # asks about an address of its own that no account holds; generated bodies
    # and refusals stay as they are.
    if " > 200 > " not in transaction.get("name", ""):
        return
    body = hooks.get_json(transaction)
    if not isinstance(body, dict) or body.get("email") != "unknown@example.test":
        return
    body["email"] = "unknown-%d@example.test" % time.time_ns()
    hooks.set_json(transaction, body)


@hooks.before("sendDraft")
def send_a_draft_of_its_own(transaction):
    # After a reset (the own-data phase) every exchange starts clean; what
    # follows already makes the draft a send needs, after the reset.
    if asked_for_a_clean_start():
        start_clean(transaction)
    # sent (201) and missing (404) both name the placeholder draft 12: sent is
    # given a fresh draft of the fixture admin's to send, and missing an id no
    # draft holds. Coverage probes keep their ids.
    # The message ownership run sends another account's draft, made by that
    # account's own chain: it keeps the id its link filled (#1163).
    if os.environ.get("CONTRACT_OWNERSHIP") == "1":
        return
    path, _, query = transaction["fullPath"].partition("?")
    drafts, placeholder, action = path.rstrip("/").rsplit("/", 2)
    name = transaction.get("name", "")
    # A success's draft id is the one its link filled (named-values illustrate),
    # which the sent draft would consume; sent gets a draft of its own whatever
    # id it carries. Coverage probes reach hooks under the documented name, so
    # an id out of range or not a number marks one, and it keeps its id.
    if not positive_id(placeholder):
        return
    if " > 201 > " in name:
        draft = fixture_call(
            transaction,
            "POST",
            "/api/v1/messages/draft",
            {
                "from": ADMIN,
                "to": [ADMIN],
                "subject": "Contract draft",
                "body_text": "Sent by the contract.",
            },
        )
        draft_id = draft["id"]
    elif " > 404 > " in name and placeholder == "12":
        draft_id = 2147483647
    else:
        return
    transaction["fullPath"] = f"{drafts}/{draft_id:d}/{action}" + (
        "?" + query if query else ""
    )


@hooks.before("sendMessage")
@hooks.before("sendDraft")
@hooks.before("forwardMessage")
def a_used_send_idempotency_key(transaction):
    if " > 409 > " not in transaction.get("name", ""):
        return
    headers = transaction["request"]["headers"]
    if headers.get("Idempotency-Key") != "contract-idempotency-conflict":
        return
    key = "contract-send-" + str(time.time_ns())
    origin = origin_of(transaction)
    request = urllib.request.Request(
        origin + "/api/v1/messages/send",
        data=json.dumps({"from": ADMIN, "to": [ADMIN], "body_text": "The first request"}).encode(),
        headers={
            "Content-Type": "application/json",
            "Authorization": "Bearer " + admin_token(origin),
            "Idempotency-Key": key,
        },
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        if response.status != 201:
            raise RuntimeError("idempotency setup did not queue the first send")
    headers["Idempotency-Key"] = key


@hooks.before("retryQueueEntry")
def retry_a_held_entry_after_bulk_examples(transaction):
    # After a reset the fixture entry is gone: queued_and_held_mail_of_its_own
    # gives the exchange an entry of its own.
    if os.environ.get("CONTRACT_COVERAGE") == "1" or asked_for_a_clean_start():
        return
    name = transaction.get("name", "")
    path, _, query = transaction["fullPath"].partition("?")
    queue, entry, action = path.rstrip("/").rsplit("/", 2)
    if not positive_id(entry):
        return
    if " > 400 > " in name:
        # The documented refusal names entry 5, which only stood until the
        # first bulk delete with an empty filter: a later run got 404 (#376).
        # It gets an entry of its own, bounced first, as bounce's refusal does.
        target = a_disposable_queue_entry()
        fixture_call(transaction, "POST", f"/api/v1/admin/queue/{target:d}/bounce")
        transaction["fullPath"] = f"{queue}/{target:d}/{action}" + (
            "?" + query if query else ""
        )
        return
    if " > 200 > " not in name:
        return
    current = fixture_call(transaction, "GET", f"{queue}/{entry}", expect=(404, 422))
    recipient = FIXTURE_PLAN["queue"]["retry"]["recipient"]
    if not current or current.get("recipient") != recipient:
        return
    if current.get("status") in ("pending", "deferred"):
        return
    # Only this row: the whole fixture set would also add a suppression and a
    # quarantined message, which other operations' examples and probes count.
    subprocess.run(
        ["chore", "test:contract-retry-fixture"],
        check=True,
        capture_output=True,
        timeout=20,
    )
    listed = fixture_call(
        transaction,
        "GET",
        queue + "?" + urllib.parse.urlencode({"recipient": recipient, "status": "deferred"}),
    )
    items = (listed or {}).get("items") or []
    if not items:
        raise RuntimeError("the replenished contract retry fixture is missing")
    transaction["fullPath"] = f"{queue}/{items[0]['id']:d}/{action}" + (
        "?" + query if query else ""
    )


@hooks.before("bounceQueueEntry")
def bounce_the_held_bounce_entry(transaction):
    # After a reset the fixture entry is gone: queued_and_held_mail_of_its_own
    # gives the exchange an entry of its own.
    if os.environ.get("CONTRACT_COVERAGE") == "1" or asked_for_a_clean_start():
        return
    # The documented bounce and the retry would both take the one entry the
    # queue links lead to, and a bounced entry can be neither retried nor
    # bounced. The bounce gets the fixture's own held entry, or a new one when
    # a bulk bounce's empty filter took it. The refusal gets an entry bounced
    # first, never the held one. Coverage probes keep their ids.
    name = transaction.get("name", "")
    path, _, query = transaction["fullPath"].partition("?")
    queue, entry, action = path.rstrip("/").rsplit("/", 2)
    # Coverage probes reach hooks under the documented name; an id out of
    # range or not a number marks one.
    if not positive_id(entry):
        return
    if " > 400 > " in name:
        target = a_disposable_queue_entry()
        fixture_call(transaction, "POST", f"/api/v1/admin/queue/{target:d}/bounce")
    elif " > 200 > " in name:
        recipient = FIXTURE_PLAN["queue"]["bounce"]["recipient"]
        listed = fixture_call(
            transaction,
            "GET",
            "/api/v1/admin/queue?"
            + urllib.parse.urlencode({"recipient": recipient, "status": "deferred"}),
        )
        items = (listed or {}).get("items") or []
        target = items[0]["id"] if items else a_disposable_queue_entry()
    else:
        return
    transaction["fullPath"] = f"{queue}/{target:d}/{action}" + (
        "?" + query if query else ""
    )


@hooks.before("unbanIP")
def unban_an_ip_that_is_banned(transaction):
    # The documented removal needs a ban on its IP, which deleteBan's own
    # example can already have removed. Coverage probes reach it unchanged.
    name = transaction.get("name", "")
    if " > 204" not in name:
        return
    ip = urllib.parse.unquote(
        transaction["fullPath"].split("?")[0].rstrip("/").rsplit("/", 1)[1]
    )
    fixture_call(
        transaction,
        "POST",
        "/api/v1/admin/bans",
        {
            "ip": ip,
            "reason": "Contract fixture",
            "protocol": "all",
            "duration": "1h",
            "created_by": ADMIN,
        },
        expect=PROBE_REFUSALS,
    )


# The one name the generator draws for a domain. Earlier probes host it and give
# it accounts and aliases on demand, so a create of it must find it gone.
GENERATED_DOMAIN = "example.com"
# What remembers an address at that domain, keyed "<origin>\\n<address>" or
# by the address's account. Each rebuilds what it needs when it finds nothing.
ADDRESSES_REMEMBERED = (
    "probe_mailboxes",
    "sender_mailboxes",
    "test_send_mailboxes",
    "created_account_probes",
    "link_fixture_ids",
)


def empty_the_generated_domain(transaction, domain_id, domain_name=GENERATED_DOMAIN):
    """Remove the accounts and aliases earlier probes made at the generated domain.

    A hook cannot change a generated body (lost_probes.py), so createDomain's probes
    keep their drawn ASCII or IDN name, and the setup makes that name free. The domain's
    accounts take a two-factor session to remove, as the domain's grants do. Its own
    postmaster account stays: the server refuses to delete a domain's last postmaster
    on its own (409 postmaster_required), and deleting the domain retires it with the
    domain (#1705)."""
    origin = origin_of(transaction)
    headers = {
        "Content-Type": "application/json",
        "Authorization": "Bearer " + mfa_admin_token(origin),
    }

    def call(method, path):
        request = urllib.request.Request(origin + path, headers=headers, method=method)
        with urllib.request.urlopen(request, timeout=10) as response:
            payload = response.read()
        return (json.loads(payload).get("data") if payload else None) or []

    for alias in list(list_rows(origin, f"/api/v1/admin/aliases?domain_id={domain_id:d}", headers)):
        call("DELETE", f"/api/v1/admin/aliases/{alias['id']:d}")
    canonical = domain_key(domain_name)
    for account in list(list_rows(origin, f"/api/v1/admin/mailboxes?domain_id={domain_id:d}", headers)):
        local, _, at = (account.get("address") or "").rpartition("@")
        if local.lower() == "postmaster" and domain_key(at) == canonical:
            continue
        call("DELETE", f"/api/v1/admin/mailboxes/{account['id']:d}")
    for name in ADDRESSES_REMEMBERED:
        remembered = hooks.stash.get(name, {})
        for key in [k for k in remembered if domain_key(k.rpartition("@")[2]) == canonical]:
            del remembered[key]
    hooks.stash.get("link_fixture_prepared", {}).pop(origin, None)
    if canonical == GENERATED_DOMAIN:
        hooks.stash["generated_domain_freed"] = True


def delete_a_domain(transaction, domain_id):
    """Delete a leftover domain, retiring its provisioned postmaster with it.

    That postmaster holds the domain's receive grant, and removing a grant takes
    a two-factor session (#1705), so the delete is sent as the two-factor
    administrator: with the setup session it is refused 403 and the hook ends."""
    fixture_call(
        transaction,
        "DELETE",
        f"/api/v1/admin/domains/{domain_id:d}",
        expect=PROBE_REFUSALS,
        two_factor=True,
    )


@hooks.before("*")
def host_the_generated_domain_again(transaction):
    # Coverage runs no after hook, so what follows createDomain's probes puts back
    # what they used: the domain as other operations' hooks expect it, whatever
    # policy the last probe gave it. Other accounts come back on demand, as they
    # first did.
    if not hooks.stash.get("generated_domain_freed") or transaction.get("operationId") == "createDomain":
        return
    hooks.stash["generated_domain_freed"] = False
    for domain in fixture_call(transaction, "GET", "/api/v1/admin/domains") or []:
        if domain.get("name") == GENERATED_DOMAIN:
            delete_a_domain(transaction, domain["id"])
    # The fixtures host it live, which the admin API cannot do without a certificate.
    subprocess.run(
        ["chore", "test:contract-link-fixture"],
        check=True,
        capture_output=True,
        timeout=20,
    )


@hooks.before("createDomain")
def create_a_domain_that_is_not_there(transaction):
    # The documented create (201) makes example.org, and the coverage process
    # repeats it after the examples process made it: a leftover domain of the
    # same name is removed first. Probes whose name is not a string, and
    # refusals, reach the handler unchanged.
    # Moved to a clean start: every exchange, refusals included, resets first,
    # so the documented create meets no leftover after one.
    if asked_for_a_clean_start():
        start_clean(transaction)
    body = hooks.get_json(transaction)
    name = body.get("name") if isinstance(body, dict) else None
    if (
        " > 201 > " not in transaction.get("name", "")
        or not isinstance(name, str)
        or not name
    ):
        return
    origin = origin_of(transaction)
    domain = find_in_list(
        origin, "/api/v1/admin/domains",
        {"Authorization": "Bearer " + admin_token(origin)},
        lambda hosted: domain_key(hosted.get("name") or "") == domain_key(name),
    )
    if domain is None:
        return
    empty_the_generated_domain(transaction, domain["id"], name)
    delete_a_domain(transaction, domain["id"])


@hooks.before("importMessage")
def import_into_an_account_with_room(transaction):
    # Only the path: a hook that changed a generated body would end the probe. The
    # documented account is 12, which the E2E stages leave at its quota on the topology.
    # A held message is new mail there (probes.py), so the example would answer 409 and
    # skip the valid probes. Elsewhere the documented account is kept, which has room
    # for the one message the other examples store or skip, except for two. always_store
    # adds a message however often it is sent, so a later process would find that
    # account full. match_submission imports into Sent, where the run has saved no
    # submission, so it stores a second message there and found the account full
    # (#1707).
    if (
        " > 200 > " not in transaction.get("name", "")
        or transaction.get("fullPath") != "/api/v1/accounts/12/messages/import"
    ):
        return
    if not any(os.environ.get(phase) == "1" for phase in (
        "CONTRACT_COVERAGE", "CONTRACT_AUTHORIZATION", "CONTRACT_OWNERSHIP"
    )):
        body = hooks.get_json(transaction)
        if not isinstance(body, dict) or (
            body.get("always_store") is not True
            and body.get("match_submission") is not True
        ):
            return
    account = mailbox_id(origin_of(transaction), ADMIN)
    transaction["fullPath"] = f"/api/v1/accounts/{account:d}/messages/import"


@hooks.before("putSieveScript")
def install_a_sieve_script_where_there_is_none(transaction):
    # The documented install (201) creates the account's script, and the
    # coverage process repeats it after the examples process installed it: the
    # leftover script is removed first. Probes with an id out of range keep it.
    path = transaction["fullPath"].split("?")[0].rstrip("/")
    account = path.rsplit("/", 2)[1]
    if " > 201 > " not in transaction.get("name", "") or not positive_id(account):
        return
    # A coverage probe's script is held at the documented "keep;" (probes.py).
    fixture_call(
        transaction, "DELETE", path[path.index("/api/") :], expect=PROBE_REFUSALS
    )


# The documented session that never existed (closeImportSession/session_not_found).
UNKNOWN_IMPORT_SESSION = "ffeeddccbbaa99887766554433221100"


def on_the_admins_import_account(transaction):
    """The path's account becomes the fixture admin's own, for a request with
    the fixture admin's session: the example's account 12 is a number, not a
    fixture. Answers the new path's parts, or None when it is left alone."""
    if not with_the_fixture_admins_session(transaction):
        return None
    path, _, query = transaction.get("fullPath", "").partition("?")
    parts = path.split("/")
    if len(parts) < 7 or not positive_id(parts[4]):
        return None
    parts[4] = str(mailbox_id(origin_of(transaction), ADMIN))
    transaction["fullPath"] = "/".join(parts) + ("?" + query if query else "")
    return parts


@hooks.before("createImportSession")
def open_an_import_session_on_the_admins_account(transaction):
    # The documented open (201) opens a session on the run's own account; the
    # after hook closes it, so no session is left for the idle sweeper.
    if " > 201 > " in transaction.get("name", ""):
        on_the_admins_import_account(transaction)


@hooks.after("createImportSession")
def close_the_opened_import_session(transaction):
    data = hooks.get_json(transaction, "real").get("data") or {}
    session, account = data.get("id"), data.get("account_id")
    if not isinstance(session, str) or type(account) is not int or account < 1:
        return
    fixture_call(
        transaction,
        "DELETE",
        f"/api/v1/accounts/{account:d}/messages/import-sessions/{session}",
        expect=(404,),
    )


@hooks.before("closeImportSession")
def close_an_import_session_the_run_opened(transaction):
    # The documented close (200) needs a session that is open: one is opened
    # for the run's own account and its id put in the path. The documented
    # unknown session (404) is asked of the run's own account, so what it
    # finds missing is the session and not the account.
    name = transaction.get("name", "")
    path, separator, query = transaction.get("fullPath", "").partition("?")
    if " > 200 > " in name:
        if not re.fullmatch(r"[0-9a-f]{32}", path.rsplit("/", 1)[-1]):
            return
        parts = on_the_admins_import_account(transaction)
        if parts is None:
            return
        opened = fixture_call(
            transaction,
            "POST",
            f"/api/v1/accounts/{parts[4]}/messages/import-sessions",
            {"client_name": "vertrag", "client_version": "contract"},
        )
        session = (opened or {}).get("id")
        if not isinstance(session, str):
            raise RuntimeError("the import-session fixture returned no id")
        parts[-1] = session
        transaction["fullPath"] = "/".join(parts) + separator + query
    elif path.rstrip("/").rsplit("/", 1)[-1] == UNKNOWN_IMPORT_SESSION:
        on_the_admins_import_account(transaction)


# The grant examples that need the caller to hold a grant in a given state
# (#1162): eligible_grant and active_grant. A grant change acts only on the
# caller's own grant, so the grant is made for the two-factor administrator,
# the examples' sender and the authorization phase's admin principal, just
# before the request: an admin role at a domain no one hosts, named by the time,
# which gives a superadmin at * nothing it lacked. Keyed by the action and the
# expected status; the value is the state to prepare and whether the body
# gets an unused second factor of that administrator's (the examples carry a
# placeholder code; invalid_code keeps its wrong one).
GRANT_STATES = {
    ("activate", "201"): ("eligible", True),
    ("activate", "409"): ("switched_on", True),
    ("activate", "403"): ("eligible", False),
    ("deactivate", "200"): ("switched_on", None),
    ("deactivate", "409"): ("eligible", None),
    ("make-eligible", "200"): ("active", True),
    ("make-eligible", "403"): ("active", False),
    ("make-eligible", "409"): ("eligible", True),
}
# The grant id every one of those examples names.
EXAMPLE_GRANT_ID = "7"


def session_account_id(transaction):
    """The account id of the session the request carries, or None."""
    header = transaction["request"]["headers"].get("Authorization", "")
    if not header.startswith("Bearer "):
        return None
    try:
        payload = header[len("Bearer "):].split(".")[1]
        claims = json.loads(base64.urlsafe_b64decode(payload + "=" * (-len(payload) % 4)))
    except (IndexError, ValueError):
        return None
    return claims.get("account_id") if isinstance(claims, dict) else None


def as_the_two_factor_admin_call(origin, method, path, body=None):
    """A setup request of the hooks' own, as the two-factor administrator."""
    request = urllib.request.Request(
        origin + path,
        data=None if body is None else json.dumps(body).encode(),
        headers={"Content-Type": "application/json", "Authorization": "Bearer " + mfa_admin_token(origin)},
        method=method,
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        raw = response.read()
    return json.loads(raw).get("data") if raw else None


def a_throwaway_grant(origin, holder, state):
    """A grant held by holder in the state asked for: eligible, active, or
    eligible and switched on (switched_on). Returns its id."""
    grant = as_the_two_factor_admin_call(origin, "POST", "/api/v1/admin/grants", {
        "holder": {"kind": "account", "id": holder},
        "role": "admin",
        "scope": f"domain:grant-{time.time_ns():d}.test",
        "mode": "active" if state == "active" else "eligible",
    })
    if state == "switched_on":
        as_the_two_factor_admin_call(
            origin, "POST", f"/api/v1/admin/grants/{grant['id']:d}/activate",
            {"reason": "contract run: a grant to switch off", "minutes": 5, **mfa_admin.factor()},
        )
        # Switching on is throttled like sign-in.
        rate_limits.pace(1.1)
    return grant["id"]


@hooks.before("activateGrant")
@hooks.before("deactivateGrant")
@hooks.before("makeGrantEligible")
def a_grant_of_the_callers_own(transaction):
    # Only the two-factor administrator's requests: it is the one principal
    # that holds the second factor these changes take. Another principal's
    # request is sent as it is, on the example's grant 7, which is not its own.
    path, _, query = transaction["fullPath"].partition("?")
    parent, _, action = path.rpartition("/")
    base, _, grant_id = parent.rpartition("/")
    status = str(transaction.get("expected", {}).get("statusCode", ""))
    wanted = GRANT_STATES.get((action, status))
    if wanted is None or grant_id != EXAMPLE_GRANT_ID:
        return
    if session_address(transaction) != mfa_admin.ADDRESS:
        return
    state, fresh_factor = wanted
    origin = origin_of(transaction)
    made = a_throwaway_grant(origin, session_account_id(transaction), state)
    hooks.stash.setdefault("throwaway_grants", {})[transaction["name"]] = made
    transaction["fullPath"] = f"{base}/{made:d}/{action}" + ("?" + query if query else "")
    if fresh_factor:
        body = hooks.get_json(transaction)
        if isinstance(body, dict):
            body.pop("totp_code", None)
            body.pop("recovery_code", None)
            body.update(mfa_admin.factor())
            hooks.set_json(transaction, body)


@hooks.after("activateGrant")
@hooks.after("deactivateGrant")
@hooks.after("makeGrantEligible")
def remove_the_throwaway_grant(transaction):
    # The grant made for the request is removed after it, whatever the answer.
    made = hooks.stash.get("throwaway_grants", {}).pop(transaction.get("name", ""), None)
    if made is None:
        return
    try:
        as_the_two_factor_admin_call(origin_of(transaction), "DELETE", f"/api/v1/admin/grants/{made:d}")
    except urllib.error.HTTPError as error:
        if error.code != 404:
            raise


# The password of each account linked for unlinkAccount's unlinked and
# linkAccount's already_linked examples (existing_link). Not a secret: it
# proves only a disposable account made for one request.
LINKED_PASSWORD = "tundra-saffron-orbit-badger-84"
UNLINKED_SUMMARY = "The link is given up"
ALREADY_LINKED_SUMMARY = "The account is linked already"


def a_throwaway_link(origin):
    """A fresh account with a password, linked to the two-factor
    administrator by its creation grant: its address and id."""
    address = fresh_address("linked")
    account_id = a_new_account(origin, address, "Linked", LINKED_PASSWORD)
    return address, account_id


@hooks.before("unlinkAccount")
def unlink_a_link_of_its_own(transaction):
    # unlinked (existing_link) names account 2, a seeded account. The
    # two-factor administrator's request gives up a link made for it alone,
    # just before; other principals' requests are sent as they are.
    if str(transaction.get("expected", {}).get("statusCode", "")) != "204":
        return
    if session_address(transaction) != mfa_admin.ADDRESS:
        return
    path, _, query = transaction["fullPath"].partition("?")
    base, _, _ = path.rpartition("/")
    _, account = a_throwaway_link(origin_of(transaction))
    transaction["fullPath"] = f"{base}/{account:d}" + ("?" + query if query else "")


GRANTED_SUMMARY = "An eligible admin grant on one domain"


@hooks.before("createGrant")
def grant_to_an_account_of_its_own(transaction):
    # granted (grant_holder) names account 2, a seeded account. Each request
    # expecting it, every principal's in the authorization phase, names an
    # account made for it alone, without credentials, just before: the eligible
    # grant it gets touches none of the run's own accounts. Made as the
    # two-factor administrator; refusals keep the documented holder.
    if str(transaction.get("expected", {}).get("statusCode", "")) != "201":
        return
    body = hooks.get_json(transaction)
    if not isinstance(body, dict) or not isinstance(body.get("holder"), dict):
        return
    body["holder"]["id"] = an_account_of_its_own(origin_of(transaction), "grant-holder")
    hooks.set_json(transaction, body)
