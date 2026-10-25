"""Provide an existing domain without rewriting the lookup's principal or refusal."""

import json
import urllib.request
import uuid

import vertrag_hooks as hooks

POLICY_ABSENT_ID = 2147483646
POLICY_ABSENT_MESSAGE = "No MTA-STS policy found for this domain"


def register_policy_absent_hook(admin_token):
    @hooks.before("getMtaStsPolicy")
    def prepare_policy_absent_domain(transaction):
        request = transaction.get("request", {})
        expected = transaction.get("expected", {})
        if (
            request.get("method") != "GET"
            or transaction.get("fullPath")
            != f"/api/v1/admin/domains/{POLICY_ABSENT_ID}/mta-sts"
            or expected.get("statusCode") != "404"
        ):
            return
        try:
            body = json.loads(expected.get("body", ""))
        except (TypeError, ValueError):
            return
        if body != {"error": {"code": "not_found", "message": POLICY_ABSENT_MESSAGE}}:
            return
        # An unauthenticated probe must reach the authentication gate as sent.
        if not any(
            key.lower() in ("authorization", "cookie", "x-api-key") and value
            for key, value in (request.get("headers") or {}).items()
        ):
            return

        origin = (
            f"{transaction['protocol']}//{transaction['host']}:{transaction['port']}"
        )
        fixtures = hooks.stash.setdefault("policy_absent_domains", {})
        if origin not in fixtures:
            # A private, inactive domain is never the success chain's domain.
            # Creating it leaves it without a policy; no existing policy is removed.
            setup = urllib.request.Request(
                origin + "/api/v1/admin/domains",
                data=json.dumps(
                    {
                        "name": f"contract-policy-absent-{uuid.uuid4().hex}.test",
                        "server_type": "restmail",
                    }
                ).encode(),
                headers={
                    "Content-Type": "application/json",
                    "Authorization": "Bearer " + admin_token(origin),
                },
                method="POST",
            )
            with urllib.request.urlopen(setup, timeout=10) as response:
                if response.status != 201:
                    raise RuntimeError(
                        "policy-absent domain setup did not create a domain"
                    )
                domain_id = (json.load(response).get("data") or {}).get("id")
            if type(domain_id) is not int or domain_id <= 0:
                raise RuntimeError("policy-absent domain setup returned no positive ID")
            fixtures[origin] = domain_id
        transaction["fullPath"] = f"/api/v1/admin/domains/{fixtures[origin]}/mta-sts"

    return prepare_policy_absent_domain
