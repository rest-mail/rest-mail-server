"""Locate the read-only DMARC fixture without weakening any refusal probe."""

import json
import os
import urllib.request

import vertrag_hooks as hooks


def register_dmarc_report_hook(admin_token):
    @hooks.before("getDmarcReport")
    def existing_report(transaction):
        # After a reset (the own-data run) the fixture report is gone, and
        # hooks.py delivers one of its own first.
        if os.environ.get("CONTRACT_RESET_SEED") == "1":
            return
        request = transaction.get("request", {})
        expected = transaction.get("expected", {})
        if (
            request.get("method") != "GET"
            or transaction.get("fullPath") != "/api/v1/admin/dmarc-reports/1"
            or not str(expected.get("statusCode", "")).startswith("2")
            or not any(
                key.lower() in ("authorization", "cookie", "x-api-key") and value
                for key, value in (request.get("headers") or {}).items()
            )
        ):
            return
        origin = f"{transaction['protocol']}//{transaction['host']}:{transaction['port']}"
        setup = urllib.request.Request(
            origin + "/api/v1/admin/dmarc-reports?reporter=reporter.test",
            headers={"Authorization": "Bearer " + admin_token(origin)},
        )
        with urllib.request.urlopen(setup, timeout=10) as response:
            reports = json.load(response).get("data") or []
        matching = [report for report in reports if report.get("report_id") == "daily-1"]
        if len(matching) != 1 or type(matching[0].get("id")) is not int:
            raise RuntimeError("the DMARC contract fixture is missing")
        transaction["fullPath"] = f"/api/v1/admin/dmarc-reports/{matching[0]['id']}"

    return existing_report
