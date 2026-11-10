"""A bounded live principal matrix, validated by the pinned Vertrag (#1237).

Uses the existing disposable principals and --api-keys fixture. Both positive
and negative scope cells name real resources, never an invented missing ID.
This does not promote the provisional whole-API authorization matrix to a gate.
"""

import argparse
import base64
import json
import os
import ssl
import urllib.error
import urllib.request
from contextlib import contextmanager
from pathlib import Path

import mfa_admin
from api_keys_live import Client, login, mfa_session, require_fixture_account
from live_requirements import prove


@contextmanager
def app_gateway(app):
    require_fixture_account(app["address"])
    endpoint = os.environ.get("CONTRACT_INTERNAL_ENDPOINT") or "https://10.99.0.20:8443"
    if not endpoint.startswith("https://"):
        raise ValueError("the gateway session requires the HTTPS internal listener")
    directory = Path(os.environ.get("CONTRACT_MTLS_DIR", ".cache/contract-mtls")).resolve()
    transport = {
        "ca-cert": str(directory / "internal-ca.crt"),
        "cert": str(directory / "internal-imap.crt"),
        "cert-key": str(directory / "internal-imap.key"),
    }
    context = ssl.create_default_context(cafile=transport["ca-cert"])
    context.load_cert_chain(transport["cert"], transport["cert-key"])
    request = urllib.request.Request(
        endpoint.rstrip("/") + "/api/v1/gateway-sessions",
        data=json.dumps({"protocol": "imap", "address": app["address"], "password": app["secret"]}).encode(),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=10, context=context) as response:
        if response.status != 201:
            raise AssertionError("the IMAP gateway session did not answer 201")
        session = json.load(response)["data"]
    token = session.get("token")
    if not isinstance(token, str) or not token.startswith("rmgs_"):
        raise AssertionError("the IMAP gateway did not return a session token")
    try:
        if session.get("credential_kind") != "app_password" or session.get("protocol") != "imap" or session.get("user", {}).get("id") != app["id"]:
            raise AssertionError("the gateway session does not identify the fixture app password")
        yield token, endpoint, transport
    finally:
        request = urllib.request.Request(
            endpoint.rstrip("/") + "/api/v1/gateway-sessions/current",
            headers={"Authorization": "Bearer " + token},
            method="DELETE",
        )
        with urllib.request.urlopen(request, timeout=10, context=context) as response:
            if response.status != 204:
                raise AssertionError("closing the IMAP gateway session did not answer 204")


def require_basic_refused(endpoint, basic, accounts):
    for account in accounts:
        request = urllib.request.Request(
            endpoint.rstrip("/") + f"/api/v1/accounts/{account}/folders",
            headers={"Authorization": basic},
        )
        try:
            with urllib.request.urlopen(request, timeout=10) as response:
                status = response.status
        except urllib.error.HTTPError as error:
            status = error.code
            error.close()
        if status != 401:
            raise AssertionError(f"HTTP Basic must answer 401, got {status}")


def run(binary, endpoint, output, principals, fixture, examples):
    email, domain_id = fixture["email"], fixture["domain_id"]
    require_fixture_account(email)
    client = Client(endpoint)
    admin = mfa_admin.session(endpoint)
    domains = client.expect(200, "GET", "/api/v1/admin/domains", token=admin)["data"]
    target = next(domain for domain in domains if domain["id"] == domain_id)
    other = next((domain for domain in domains if domain["id"] != domain_id), None)
    if other is None:
        raise ValueError("the matrix requires two real domains with different IDs")
    scoped, _ = mfa_session(client, email, "contract-api-key123!@")
    held = {"capability": "domains:read", "scope": "domain:" + target["name"]}
    if held not in scoped.get("scoped_capabilities", []):
        raise ValueError(
            "the scoped session did not receive its fixture's real domain grant"
        )
    account = scoped["user"]["id"]
    keys = f"/api/v1/accounts/{account}/api-keys"
    ordinary = principals["ordinary"]
    ordinary_token = login(client, ordinary["address"], ordinary["password"])[
        "access_token"
    ]
    app = principals["app_password"]
    basic = (
        "Basic "
        + base64.b64encode(f"{app['address']}:{app['secret']}".encode()).decode()
    )
    with urllib.request.urlopen(
        endpoint.rstrip("/") + "/api/docs/openapi.json", timeout=30
    ) as response:
        document = json.load(response)
    other_account = principals["other"]["id"]
    client.expect(200, "GET", f"/api/v1/accounts/{other_account}/folders", token=admin)
    key = client.expect(
        201,
        "POST",
        keys,
        {
            "name": "Contract principal proof",
            "password": "contract-api-key123!@",
            "permissions": [held],
        },
        scoped["access_token"],
    )["data"]
    cells = [
        ("admin", "getDomain", "found", "Bearer " + admin, domain_id),
        (
            "ordinary",
            "getDomain",
            "outside_domain",
            "Bearer " + ordinary_token,
            domain_id,
        ),
        ("anonymous", "getDomain", "unauthenticated", "", domain_id),
        ("app-password-own", "listFolders", "found", "", app["id"]),
        (
            "app-password-outside",
            "listFolders",
            "key_wrong_scope",
            "",
            other_account,
        ),
        (
            "scoped-grant-inside",
            "getDomain",
            "found",
            "Bearer " + scoped["access_token"],
            domain_id,
        ),
        (
            "scoped-grant-outside",
            "getDomain",
            "outside_domain",
            "Bearer " + scoped["access_token"],
            other["id"],
        ),
        ("api-key-inside", "getDomain", "found", "Bearer " + key["token"], domain_id),
        (
            "api-key-outside",
            "getDomain",
            "outside_domain",
            "Bearer " + key["token"],
            other["id"],
        ),
    ]
    failed = 0
    try:
        with app_gateway(app) as (gateway_token, internal_endpoint, transport):
            require_basic_refused(endpoint, basic, (app["id"], other_account))
            for label, operation, name, header, resource in cells:
                pair = examples[operation][name]
                destination, options = endpoint, {}
                if label.startswith("app-password-"):
                    destination = internal_endpoint
                    header = "Bearer " + gateway_token
                    options["transport"] = transport
                failed |= prove(
                    binary,
                    destination,
                    document,
                    {operation: {name: pair}},
                    label,
                    output,
                    requirement=pair.get("requires", {}),
                    header="Authorization: " + header if header else "",
                    path_values={"id": resource},
                    **options,
                )
    finally:
        client.expect(
            204, "DELETE", f"{keys}/{key['id']}", token=scoped["access_token"]
        )
    return failed


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("endpoint")
    parser.add_argument("output")
    parser.add_argument("--vertrag", required=True)
    parser.add_argument("--principals", required=True)
    parser.add_argument("--fixture", required=True)
    parser.add_argument("--examples", default="openapi-example.json")
    args = parser.parse_args()
    return run(
        args.vertrag,
        args.endpoint,
        args.output,
        json.loads(Path(args.principals).read_text()),
        json.loads(Path(args.fixture).read_text()),
        json.loads(Path(args.examples).read_text()),
    )


if __name__ == "__main__":
    raise SystemExit(main())
