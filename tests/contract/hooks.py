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
import json
import os
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
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

PRINCIPALS = os.environ.get("CONTRACT_PRINCIPALS", ".cache/contract-principals.json")


def principals():
    with open(PRINCIPALS) as f:
        return json.load(f)


def basic(address, password):
    token = base64.b64encode((f"{address}:{password}").encode()).decode()
    return "Basic " + token


def mfa_admin_token(origin):
    """A session signed in with a real second factor, renewed after ten minutes."""
    cached = hooks.stash.get("mfa_admin_sessions", {}).get(origin)
    if cached and time.time() - cached["at"] < 600:
        return cached["token"]
    sessions = hooks.stash.get("mfa_admin_sessions", {})
    sessions[origin] = {"token": mfa_admin.session(origin), "at": time.time()}
    hooks.stash["mfa_admin_sessions"] = sessions
    return sessions[origin]["token"]


def origin_of(transaction):
    return "{}//{}:{}".format(transaction["protocol"], transaction["host"], transaction["port"])


def fixture_admin_claims(transaction):
    """The claims of the fixture admin's own session, when the request carries
    it. The authorization phase's principals and an unauthenticated 401 do not."""
    header = transaction["request"]["headers"].get("Authorization", "")
    if not header.startswith("Bearer "):
        return None
    try:
        payload = header[len("Bearer "):].split(".")[1]
        claims = json.loads(base64.urlsafe_b64decode(payload + "=" * (-len(payload) % 4)))
    except (IndexError, ValueError):
        return None
    return claims if isinstance(claims, dict) and claims.get("address") == ADMIN else None


def with_the_fixture_admins_session(transaction):
    """Whether the request carries the fixture admin's own session."""
    return fixture_admin_claims(transaction) is not None


def as_the_two_factor_admin(transaction):
    """Send a request made with the fixture admin's session with the two-factor
    administrator's instead, and say whether it was."""
    if not with_the_fixture_admins_session(transaction):
        return False
    transaction["request"]["headers"]["Authorization"] = "Bearer " + mfa_admin_token(origin_of(transaction))
    return True


@hooks.before("listAPIKeys")
@hooks.before("revokeAPIKey")
@hooks.before("confirmTwoFactor")
@hooks.before("createGrant")
@hooks.before("deleteGrant")
@hooks.before("activateGrant")
@hooks.before("deactivateGrant")
@hooks.before("makeGrantEligible")
def send_as_the_two_factor_admin(transaction):
    # These take a session that signed in with a second factor, which the
    # fixture admin has not. The run's requests are sent as the two-factor
    # administrator, a superadmin as the fixture admin is, signed in for real.
    # Confirming its authenticator again is refused (409): it is already on,
    # and the fixture admin's sign-in requirements are left alone.
    as_the_two_factor_admin(transaction)


@hooks.before("createAPIKey")
def an_api_key_from_the_two_factor_admin(transaction):
    # Making a key also takes the maker's current password.
    claims = fixture_admin_claims(transaction)
    if not as_the_two_factor_admin(transaction):
        return
    body = hooks.get_json(transaction)
    # The body_array refusal sends a JSON array, which has no password to set.
    if not isinstance(body, dict) or "password" not in body:
        return
    body["password"] = mfa_admin.PASSWORD
    # A key holds only what its maker and the account it is for both hold
    # (apiKeyWithinGrants). The example's key is for the fixture admin (1),
    # whose grants reach restmail.test, not the example's robot@example.com:
    # it is scoped to the admin's own mailbox, which an owner may read.
    path = transaction["fullPath"].partition("?")[0]
    account = path.rstrip("/").split("/")[-2]
    if account == str(claims.get("account_id")) and isinstance(body.get("permissions"), list):
        for permission in body["permissions"]:
            if isinstance(permission, dict) and "scope" in permission:
                permission["scope"] = "mailbox:" + ADMIN
    hooks.set_json(transaction, body)


@hooks.before("deleteGrant")
def delete_a_grant_made_for_it(transaction):
    # body_boundary names grant 1, which is a fixture principal's own. Delete a
    # grant made for the purpose instead: an eligible admin role on the fixture
    # domain, held by an account made without credentials. Other ids are the
    # refusal examples' and the coverage probes', and are sent as they are.
    path, _, query = transaction["fullPath"].partition("?")
    parent, _, grant_id = path.rpartition("/")
    if grant_id != "1":
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
@hooks.before("protocolSignIn")
@hooks.before("enrollTwoFactor")
@hooks.before("confirmTwoFactor")
@hooks.before("recoverAccount")
@hooks.before("acceptInvitation")
def pace_auth_requests(transaction):
    # These operations share the default 1 request/second auth bucket. Coverage
    # repeats valid requests as well as refusals, so even correct credentials
    # drain its burst. Wait for a token without changing or retrying the probe.
    # Leave a small margin for clock/scheduling differences at the boundary.
    time.sleep(1.1)


@hooks.before("testPipeline")
@hooks.before("testFilter")
@hooks.before("testCustomFilter")
def pace_pipeline_previews(transaction):
    # Pipeline previews share a 2 requests/second bucket in the E2E topology.
    # Pace every probe without changing its body, credential or expected reply.
    time.sleep(0.55)


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
        body["refresh_token"] = hooks.stash.get("refresh_token", "")
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
    origin = "{}//{}:{}".format(
        transaction["protocol"],
        transaction["host"],
        transaction["port"],
    )
    cookie = fresh_browser_refresh_cookie(origin)
    headers["Cookie"] = cookie
    transaction["request"]["headers"] = headers


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


@hooks.before("createAppPassword")
def confirm_with_the_admin_password(transaction):
    body = hooks.get_json(transaction)
    # The body_array refusal sends a JSON array, which has no password to set.
    if not isinstance(body, dict):
        return
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
        request = urllib.request.Request(
            origin + "/api/v1/admin/mailboxes?limit=1000",
            headers={"Authorization": "Bearer " + admin_token(origin)},
        )
        with urllib.request.urlopen(request, timeout=10) as response:
            accounts = json.load(response).get("data") or []
        ids[key] = next(
            (
                account["id"]
                for account in accounts
                if account.get("address") == address
            ),
            None,
        )
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
    body = hooks.get_json(transaction)
    # The undecodable create (body_array, 400) is sent as documented: an
    # array, with no object to put the domain in.
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
    # Replace only the documented successful request's placeholder. Missing
    # IDs, invalid bodies and coverage probes must retain their own target.
    if (
        not isinstance(body, dict)
        or "email" not in body
        or type(body.get("pipeline_id")) is not int
        or body["pipeline_id"] != 1
    ):
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


@hooks.before("createCertificate")
def a_self_signed_certificate(transaction):
    body = hooks.get_json(transaction)
    # Replace only the documented material placeholders, and the schema-valid
    # placeholder material of a coverage probe named for the 201, which no
    # server accepts as a certificate. Preserve incomplete, invalid and refusal
    # bodies and the caller's renewal policy.
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
    name = hooks.stash.get("domain") or "example.org"
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
            body["cert_pem"] = cert_file.read()
            body["key_pem"] = key_file.read()
            if body.get("domain_id") == 1 and hooks.stash.get("domain_id") is not None:
                body["domain_id"] = hooks.stash["domain_id"]
            hooks.set_json(transaction, body)


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
    origin = "{}//{}:{}".format(
        transaction["protocol"],
        transaction["host"],
        transaction["port"],
    )
    ready = hooks.stash.get("certified_domains", {})
    if not domain_id.isdigit() or (origin + "\n" + domain_id) in ready:
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

    try:
        name = call("GET", "/api/v1/admin/domains/" + domain_id)["name"]
    except urllib.error.HTTPError as error:
        # No such domain, or an id out of range: the probe meets the refusal itself.
        if error.code in PROBE_REFUSALS:
            return
        raise
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
    ready[origin + "\n" + domain_id] = True
    hooks.stash["certified_domains"] = ready


@hooks.before("mtaStsPolicyFile")
def ask_for_the_created_domains_policy(transaction):
    # Served at mta-sts.<domain>: the domain is the Host's, not a parameter.
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
        request = urllib.request.Request(
            origin + "/api/v1/admin/domains", headers=headers
        )
        with urllib.request.urlopen(request, timeout=10) as response:
            domains = json.load(response).get("data") or []
        domain_id = next(
            (d["id"] for d in domains if d.get("name") == ADMIN.split("@")[1]), None
        )
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

    domain_id = next(
        (
            d["id"]
            for d in call("GET", "/api/v1/admin/domains") or []
            if (d.get("name") or "").lower() == domain.lower()
        ),
        None,
    )
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
                call(
                    "PUT",
                    f"/api/v1/admin/dkim/{domain_id:d}",
                    {"selector": "contract", "private_key": key_file.read()},
                )
    mailboxes = call("GET", f"/api/v1/admin/mailboxes?domain_id={domain_id:d}") or []
    if not any((m.get("address") or "").lower() == address.lower() for m in mailboxes):
        call("POST", "/api/v1/accounts", {"address": address})
    ready[origin + "\n" + address.lower()] = True
    hooks.stash["sender_mailboxes"] = ready


@hooks.before("sendMessage")
def a_sending_policy_for_the_send(transaction):
    # unsubscribe_not_allowed (422) is refused by a transactional sender
    # domain, and restmail.test has no policy: it is made transactional for
    # that request, and the probes built from it, alone. Every other send,
    # coverage probes included (they run no after hooks), finds no policy.
    paired = " > 422 > " in transaction.get("name", "")
    set_the_admin_domains_policy(transaction, "transactional" if paired else "")
    # Coverage probes send from a generated address (someone@example.com)
    # that no mailbox holds, which the handler rightly refuses (403). The
    # sender gets a hosted, signing domain and a mailbox; the request's bytes
    # and credentials stay as generated.
    body = hooks.get_json(transaction)
    if isinstance(body, dict):
        a_signing_mailbox_at(transaction, body.get("from"))


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
        headers = {
            "Content-Type": "application/json",
            "Authorization": "Bearer " + admin_token(origin),
        }
        request = urllib.request.Request(
            origin + "/api/v1/admin/domains", headers=headers
        )
        with urllib.request.urlopen(request, timeout=10) as response:
            domains = json.load(response).get("data") or []
        domain_id = next((d["id"] for d in domains if d.get("name") == domain), None)
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
        request = urllib.request.Request(
            origin + f"/api/v1/admin/mailboxes?domain_id={domain_id:d}",
            headers=headers,
        )
        with urllib.request.urlopen(request, timeout=10) as response:
            accounts = json.load(response).get("data") or []
        if not any(
            (a.get("address") or "").lower() == address.lower() for a in accounts
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


def schedule_a_message(transaction):
    """Store a scheduled message through the API and return its id, so the
    list and cancel examples have something of their own to act on."""
    origin = "{}//{}:{}".format(
        transaction["protocol"],
        transaction["host"],
        transaction["port"],
    )
    headers = {
        "Content-Type": "application/json",
        "Authorization": "Bearer " + admin_token(origin),
    }
    body = {
        "from": ADMIN,
        "to": [ADMIN],
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
        transaction["fullPath"] = f"/api/v1/messages/scheduled/{scheduled_id:d}"


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
    if not path.endswith("/api/v1/admin/queue"):
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
    # The 422 is the report that names no hosted domain: leave it unhosted.
    if " > 422 > " in transaction["name"]:
        return
    body = hooks.get_json(transaction)
    policies = body.get("policies") if isinstance(body, dict) else None
    if not isinstance(policies, list):
        return
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
    folder = f"contract-rename-{serial:d}"
    origin = "{}//{}:{}".format(
        transaction["protocol"],
        transaction["host"],
        transaction["port"],
    )
    request = urllib.request.Request(
        origin + parent,
        data=json.dumps({"name": folder}).encode(),
        headers={
            "Content-Type": "application/json",
            "Authorization": "Bearer " + admin_token(origin),
        },
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        if response.status != 201:
            raise RuntimeError("could not create a folder for the rename probe")
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


@hooks.before("/api/v1/auth/protocol-sign-in > * > 200 > application/json")
@hooks.before("/api/v1/auth/protocol-sign-in > * > 400 > application/json")
@hooks.before("/api/v1/auth/protocol-sign-in > * > 422 > application/json")
def sign_in_a_mail_client(transaction):
    app = principals()["app_password"]
    transaction["request"]["headers"]["Authorization"] = basic(
        app["address"], app["secret"]
    )


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
                origin + parent + "/" + str(contact["id"]),
                headers=headers,
                method="DELETE",
            )
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
    origin = "{}//{}:{}".format(
        transaction["protocol"],
        transaction["host"],
        transaction["port"],
    )
    headers = {
        "Content-Type": "application/json",
        "Authorization": "Bearer " + admin_token(origin),
    }
    pattern = created["body"]["pattern"]
    rule = next(
        (
            r
            for r in sender_rules(origin, parent, headers) or []
            if r.get("pattern") == pattern
        ),
        None,
    )
    if rule is None:
        request = urllib.request.Request(
            origin + parent,
            data=json.dumps(created["body"]).encode(),
            headers=headers,
            method="POST",
        )
        with urllib.request.urlopen(request, timeout=10) as response:
            rule = json.load(response)["data"]
    transaction["fullPath"] = (
        parent + "/" + str(rule["id"]) + ("?" + query if query else "")
    )


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
    request = urllib.request.Request(
        origin + f"/api/v1/admin/aliases?domain_id={domain_id:d}",
        headers=headers,
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        aliases = json.load(response).get("data") or []
    return next(
        (
            a
            for a in aliases
            if (a.get("source_address") or "").lower() == source.lower()
        ),
        None,
    )


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
    owned = hooks.stash.get("created_alias_probes", {})
    key = origin + "\n" + source.lower()
    if alias is None:
        owned[key] = True
        hooks.stash["created_alias_probes"] = owned
        return
    if key not in owned:
        return
    request = urllib.request.Request(
        origin + "/api/v1/admin/aliases/{:d}".format(alias["id"]),
        headers=headers,
        method="DELETE",
    )
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
    hooks.stash["created_alias"] = {
        "id": data["id"],
        "body": hooks.get_json(transaction),
    }


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
    origin = "{}//{}:{}".format(
        transaction["protocol"],
        transaction["host"],
        transaction["port"],
    )
    headers = {
        "Content-Type": "application/json",
        "Authorization": "Bearer " + admin_token(origin),
    }
    source = created["body"]["source_address"]
    domain_id = hosted_domain_id(origin, headers, source.split("@")[1].lower())
    alias = alias_on(origin, headers, domain_id, source)
    if alias is None:
        request = urllib.request.Request(
            origin + "/api/v1/admin/aliases",
            data=json.dumps(created["body"]).encode(),
            headers=headers,
            method="POST",
        )
        with urllib.request.urlopen(request, timeout=10) as response:
            alias = json.load(response)["data"]
    transaction["fullPath"] = (
        parent + "/" + str(alias["id"]) + ("?" + query if query else "")
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

    domain_id = next(
        (
            d["id"]
            for d in call("GET", "/api/v1/admin/domains") or []
            if (d.get("name") or "").lower() == domain.lower()
        ),
        None,
    )
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
    mailboxes = call("GET", f"/api/v1/admin/mailboxes?domain_id={domain_id:d}") or []
    if not any((m.get("address") or "").lower() == address.lower() for m in mailboxes):
        call("POST", "/api/v1/accounts", {"address": address})
    ready[origin + "\n" + address.lower()] = True
    hooks.stash["probe_mailboxes"] = ready


@hooks.before("saveDraft")
def a_mailbox_for_the_draft(transaction):
    # Coverage probes save drafts from a generated address
    # (someone@example.com) that no mailbox holds, which the handler rightly
    # refuses (403). The sender gets a hosted domain and a mailbox the admin
    # created; the request's bytes and credentials stay as generated.
    body = hooks.get_json(transaction)
    if isinstance(body, dict):
        a_mailbox_at(transaction, body.get("from"))


@hooks.before("createAccount")
def host_the_account_probe_domain(transaction):
    # Coverage probes address a domain the fixture does not host
    # (someone@example.com), which the handler rightly refuses with 422.
    # Host the domain, claim an absent address before its first create, and
    # remove only that probe's account when generated bodies repeat it.
    # Existing accounts and all generated request bytes stay untouched.
    from urllib.error import HTTPError

    body = hooks.get_json(transaction)
    # An owned account needs a session that signed in with a second factor
    # (#341, Decided 12), and the fixture admin has none: its refusal is the
    # rule, not a disagreement. TestCreateOwnedAccount proves both sides.
    if isinstance(body, dict) and body.get("owned") is True:
        transaction["skip"] = True
        return
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
        origin + f"/api/v1/admin/mailboxes?domain_id={domain_id:d}",
        headers=headers,
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        accounts = json.load(response).get("data") or []
    account = next(
        (a for a in accounts if (a.get("address") or "").lower() == address.lower()),
        None,
    )
    owned = hooks.stash.get("created_account_probes", {})
    key = origin + "\n" + address.lower()
    if account is None:
        owned[key] = True
        hooks.stash["created_account_probes"] = owned
        return
    if key not in owned:
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
    headers = {
        "Content-Type": "application/json",
        "Authorization": "Bearer " + admin_token(origin),
    }
    address = created["body"]["address"]
    request = urllib.request.Request(origin + "/api/v1/admin/domains", headers=headers)
    with urllib.request.urlopen(request, timeout=10) as response:
        domains = json.load(response).get("data") or []
    domain_id = next(d["id"] for d in domains if d.get("name") == address.split("@")[1])
    request = urllib.request.Request(
        origin + f"/api/v1/admin/mailboxes?domain_id={domain_id:d}",
        headers=headers,
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        accounts = json.load(response).get("data") or []
    account = next(
        (a for a in accounts if (a.get("address") or "").lower() == address.lower()),
        None,
    )
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
    count = hooks.stash.get("invitable_accounts", 0) + 1
    hooks.stash["invitable_accounts"] = count
    address = f"invited-{time.time_ns()}-{count}@restmail.test"
    request = urllib.request.Request(
        origin + "/api/v1/accounts",
        data=json.dumps({"address": address, "display_name": "Invited"}).encode(),
        headers={
            "Content-Type": "application/json",
            "Authorization": "Bearer " + admin_token(origin),
        },
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        return json.load(response)["data"]["id"]


@hooks.before("inviteAccount")
def invite_an_account_without_credentials(transaction):
    # created, existing_credentials and missing all name the placeholder id 12,
    # and differ only in the state of that account. Each is given an account in
    # its state: a fresh one made without a password, the fixture admin (1),
    # who has one, and an id no account holds. Coverage probes keep their ids.
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
    elif " > 404 > " in name:
        account_id = 2147483647
    elif " > 201 > " in name:
        origin = "{}//{}:{}".format(
            transaction["protocol"],
            transaction["host"],
            transaction["port"],
        )
        account_id = an_account_without_credentials(origin)
    else:
        return
    transaction["fullPath"] = f"{mailboxes}/{account_id:d}/invitation" + (
        "?" + query if query else ""
    )


@hooks.before("acceptInvitation")
def redeem_a_fresh_invitation(transaction):
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
        headers={"Authorization": "Bearer " + admin_token(origin)},
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


def fixture_call(transaction, method, path, body=None, expect=(200, 201, 204)):
    """One admin API call for fixture setup, answering its JSON data, or None
    for an expected refusal such as a 404 on a delete of nothing."""
    origin = "{}//{}:{}".format(
        transaction["protocol"],
        transaction["host"],
        transaction["port"],
    )
    request = urllib.request.Request(
        origin + path,
        data=json.dumps(body).encode() if body is not None else None,
        headers={
            "Content-Type": "application/json",
            "Authorization": "Bearer " + admin_token(origin),
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
    # coverage probes reach the handler unchanged.
    body = hooks.get_json(transaction)
    email = body.get("email") if isinstance(body, dict) else None
    name = transaction.get("name", "")
    contacts = transaction["fullPath"].split("?")[0].rsplit("/", 1)[0]
    if not contacts.endswith("/contacts"):
        return
    path = contacts[contacts.index("/api/") :]
    if email == "new@example.com" and " > 201 > " in name:
        query = urllib.parse.urlencode({"search": email, "limit": 200})
        listed = fixture_call(
            transaction, "GET", path + "?" + query, expect=PROBE_REFUSALS
        )
        for contact in listed or []:
            if contact.get("email") == email:
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


@hooks.before("sendDraft")
def send_a_draft_of_its_own(transaction):
    # sent (201) and missing (404) both name the placeholder draft 12: sent is
    # given a fresh draft of the fixture admin's to send, and missing an id no
    # draft holds. Coverage probes keep their ids.
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


@hooks.before("bounceQueueEntry")
def bounce_the_held_bounce_entry(transaction):
    # The documented bounce and the retry would both take the one entry the
    # queue links lead to, and a bounced entry cannot be retried. The bounce
    # gets the fixture's own held entry; coverage probes keep their ids.
    name = transaction.get("name", "")
    path, _, query = transaction["fullPath"].partition("?")
    queue, entry, action = path.rstrip("/").rsplit("/", 2)
    # Coverage probes reach hooks under the documented name; an id out of
    # range or not a number marks one.
    if " > 200 > " not in name or not positive_id(entry):
        return
    recipient = FIXTURE_PLAN["queue"]["bounce"]["recipient"]
    listed = fixture_call(
        transaction,
        "GET",
        "/api/v1/admin/queue?" + urllib.parse.urlencode({"recipient": recipient}),
    )
    items = (listed or {}).get("items") or []
    if not items:
        raise RuntimeError("the contract bounce fixture is missing")
    transaction["fullPath"] = f"{queue}/{items[0]['id']:d}/{action}" + (
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


@hooks.before("createDomain")
def create_a_domain_that_is_not_there(transaction):
    # The documented create (201) makes example.org, and the coverage process
    # repeats it after the examples process made it: a leftover domain of the
    # same name is removed first. Probes whose name is not a string, and
    # refusals, reach the handler unchanged.
    body = hooks.get_json(transaction)
    name = body.get("name") if isinstance(body, dict) else None
    if (
        " > 201 > " not in transaction.get("name", "")
        or not isinstance(name, str)
        or not name
    ):
        return
    for domain in fixture_call(transaction, "GET", "/api/v1/admin/domains") or []:
        if domain.get("name") == name:
            fixture_call(
                transaction,
                "DELETE",
                f"/api/v1/admin/domains/{domain['id']:d}",
                expect=PROBE_REFUSALS,
            )


@hooks.before("putSieveScript")
def install_a_sieve_script_where_there_is_none(transaction):
    # The documented install (201) creates the account's script, and the
    # coverage process repeats it after the examples process installed it: the
    # leftover script is removed first. Probes with an id out of range keep it.
    path = transaction["fullPath"].split("?")[0].rstrip("/")
    account = path.rsplit("/", 2)[1]
    if " > 201 > " not in transaction.get("name", "") or not positive_id(account):
        return
    # A coverage probe's script is whatever string the generator drew, which no
    # schema can make Sieve: it installs the documented "keep;" and keeps the
    # rest of its body, so what it tests is the other fields.
    body = hooks.get_json(transaction)
    if (
        isinstance(body, dict)
        and isinstance(body.get("script"), str)
        and body["script"] != "keep;"
    ):
        body["script"] = "keep;"
        hooks.set_json(transaction, body)
    fixture_call(
        transaction, "DELETE", path[path.index("/api/") :], expect=PROBE_REFUSALS
    )


@hooks.before("importMessage")
def import_a_message_a_parser_can_read(transaction):
    # A coverage probe's raw_message is base64 of whatever the generator drew,
    # which no schema can make RFC 5322. Such a probe imports a small message of
    # its own, with its own Message-ID so repeats are not duplicates, and keeps
    # the rest of its body. A documented message, which has headers, is left.
    if " > 200 > " not in transaction.get("name", ""):
        return
    body = hooks.get_json(transaction)
    raw = body.get("raw_message") if isinstance(body, dict) else None
    if not isinstance(raw, str):
        return
    try:
        octets = base64.b64decode(raw, validate=True)
    except ValueError:
        octets = b""
    if b"\r\n\r\n" in octets or b"\n\n" in octets:
        return
    message = (
        f"Message-ID: <contract-import-{time.time_ns()}@example.test>\r\n"
        "From: Original <old@example.test>\r\n"
        "Subject: imported\r\n\r\nhistory\r\n"
    )
    body["raw_message"] = base64.b64encode(message.encode()).decode()
    hooks.set_json(transaction, body)
