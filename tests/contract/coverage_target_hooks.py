"""Disposable targets for coverage's success requests (Part of #1443).

Only paths change: rewriting a generated body would discard its probe.
"""

import json
import os
import subprocess
import tempfile
import time
import urllib.request
from pathlib import Path

import vertrag_hooks as hooks


def coverage_placeholder(transaction, status, paths):
    path = transaction.get("fullPath", "").partition("?")[0]
    return (
        os.environ.get("CONTRACT_COVERAGE") == "1"
        and f" > {status}" in transaction.get("name", "")
        and path in paths
    )


def replace_path(transaction, path):
    _, separator, query = transaction["fullPath"].partition("?")
    transaction["fullPath"] = path + (separator + query if separator else "")


def register_coverage_target_hooks(shared):
    def selected(transaction, status, paths):
        return coverage_placeholder(transaction, status, paths) and shared[
            "session_address"
        ](transaction) in (shared["ADMIN"], shared["mfa_admin"].ADDRESS)

    @hooks.before("revokeAPIKey")
    @hooks.before("revokeCredential")
    def a_disposable_credential_for_coverage(transaction):
        paths = (
            "/api/v1/accounts/1/api-keys/1",
            "/api/v1/accounts/1/credentials/1",
        )
        if not selected(transaction, 204, paths):
            return
        origin = shared["origin_of"](transaction)
        collection = transaction["fullPath"].partition("?")[0].rsplit("/", 1)[0]
        body = {
            "name": f"contract-revoke-{time.time_ns()}",
            "password": shared["mfa_admin"].PASSWORD,
        }
        if collection.endswith("/api-keys"):
            address = shared["mailbox_address"](origin, "1")
            if not address:
                raise RuntimeError("the coverage credential's account does not exist")
            body["permissions"] = [
                {"capability": "mail:read", "scope": "mailbox:" + address}
            ]
        time.sleep(1.1)
        request = urllib.request.Request(
            origin + collection,
            data=json.dumps(body).encode(),
            headers={
                "Content-Type": "application/json",
                "Authorization": "Bearer " + shared["mfa_admin_token"](origin),
            },
            method="POST",
        )
        with urllib.request.urlopen(request, timeout=10) as response:
            created = json.load(response)["data"]
        replace_path(transaction, collection + "/" + str(created["id"]))

    @hooks.before("updateMailbox")
    def a_disposable_mailbox_for_coverage(transaction):
        # A probe's generated password replaces the password of the account it
        # names and revokes its other credentials. The literal id is the seeded
        # eve@restmail.test, whose password is what linkAccount's example proves:
        # a later run's link answered 403 invalid_credentials (#376). One
        # mailbox takes every probe of this run.
        path = "/api/v1/admin/mailboxes/1"
        if not selected(transaction, 200, (path,)):
            return
        origin = shared["origin_of"](transaction)
        made = hooks.stash.setdefault("coverage_mailboxes", {})
        if origin not in made:
            domain = shared["ADMIN"].partition("@")[2]
            request = urllib.request.Request(
                origin + "/api/v1/accounts",
                data=json.dumps(
                    {"address": f"contract-update-{time.time_ns()}@{domain}"}
                ).encode(),
                headers={
                    "Content-Type": "application/json",
                    "Authorization": "Bearer " + shared["mfa_admin_token"](origin),
                },
                method="POST",
            )
            with urllib.request.urlopen(request, timeout=10) as response:
                made[origin] = json.load(response)["data"]["id"]
        replace_path(transaction, f"/api/v1/admin/mailboxes/{made[origin]}")

    @hooks.before("getCertificate")
    def a_disposable_certificate_for_coverage(transaction):
        if not selected(transaction, 200, ("/api/v1/admin/certificates/1",)):
            return
        domain = shared["fixture_call"](
            transaction,
            "POST",
            "/api/v1/admin/domains",
            {
                "name": f"contract-certificate-{time.time_ns()}.test",
                "server_type": "traditional",
            },
        )
        with tempfile.TemporaryDirectory() as directory:
            key = Path(directory) / "key.pem"
            certificate = Path(directory) / "certificate.pem"
            shared["openssl"](
                "req",
                "-x509",
                "-newkey",
                "rsa:2048",
                "-nodes",
                "-days",
                "30",
                "-subj",
                "/CN=" + domain["name"],
                "-addext",
                "subjectAltName=DNS:" + domain["name"],
                "-keyout",
                str(key),
                "-out",
                str(certificate),
            )
            created = shared["fixture_call"](
                transaction,
                "POST",
                "/api/v1/admin/certificates",
                {
                    "domain_id": domain["id"],
                    "cert_pem": certificate.read_text(),
                    "key_pem": key.read_text(),
                    "auto_renew": False,
                },
            )
        replace_path(transaction, "/api/v1/admin/certificates/" + str(created["id"]))

    def replenish_held_mail():
        completed = subprocess.run(
            ["chore", "test:contract-held-mail-fixture"],
            check=True,
            capture_output=True,
            timeout=20,
            text=True,
        )
        for line in completed.stdout.splitlines():
            if line.startswith("HELD_MAIL_FIXTURE="):
                targets = json.loads(line.partition("=")[2])
                if all(
                    type(targets.get(key)) is int and targets[key] > 0
                    for key in (
                        "account_id",
                        "message_id",
                        "domain_id",
                        "suppression_id",
                    )
                ):
                    return targets
        raise RuntimeError("the coverage fixture returned no held-mail target ids")

    @hooks.before("deleteQuarantine")
    def a_disposable_quarantined_message_for_coverage(transaction):
        paths = ("/api/v1/accounts/1/quarantine/41", "/api/v1/accounts/1/quarantine/1")
        if not selected(transaction, 200, paths):
            return
        targets = replenish_held_mail()
        replace_path(
            transaction,
            "/api/v1/accounts/{account_id}/quarantine/{message_id}".format(**targets),
        )

    @hooks.before("clearSuppression")
    def a_disposable_suppression_for_coverage(transaction):
        paths = ("/api/v1/admin/domains/1/suppressions/1",)
        if not selected(transaction, 204, paths):
            return
        targets = replenish_held_mail()
        replace_path(
            transaction,
            "/api/v1/admin/domains/{domain_id}/suppressions/{suppression_id}".format(
                **targets
            ),
        )
