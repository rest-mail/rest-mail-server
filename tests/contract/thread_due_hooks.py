"""Provide a thread for due successes without changing the request under test."""

import base64
import json
import re
import urllib.error
import urllib.parse
import urllib.request

import vertrag_hooks as hooks

THREAD = "<due/2026@example.net>"


def register_thread_due_hook(admin_token):
    @hooks.before("setThreadDue")
    def prepare_thread(transaction):
        request = transaction.get("request", {})
        if request.get("method") != "POST" or not str(
            transaction.get("expected", {}).get("statusCode", "")
        ).startswith("2"):
            return
        match = re.fullmatch(
            r"/api/v1/accounts/([1-9][0-9]*)/threads/([^?]+)/due",
            transaction.get("fullPath", ""),
        )
        if not match or urllib.parse.unquote(match[2]) != THREAD:
            return
        if not any(
            key.lower() in ("authorization", "cookie", "x-api-key") and value
            for key, value in (request.get("headers") or {}).items()
        ):
            return
        origin = (
            f"{transaction['protocol']}//{transaction['host']}:{transaction['port']}"
        )
        raw = (
            f"Message-ID: {THREAD}\r\n"
            "From: sender@example.test\r\n"
            "To: recipient@restmail.test\r\n"
            "Date: Mon, 2 Nov 2026 12:00:00 +0000\r\n"
            "Subject: Due contract fixture\r\n\r\nDue fixture.\r\n"
        ).encode()
        setup = urllib.request.Request(
            origin + f"/api/v1/accounts/{match[1]}/messages/import",
            data=json.dumps(
                {
                    "raw_message": base64.b64encode(raw).decode(),
                    "folder": "Contract due",
                    "internal_date": "2026-11-02T12:00:00Z",
                }
            ).encode(),
            headers={
                "Content-Type": "application/json",
                "Authorization": "Bearer " + admin_token(origin),
            },
            method="POST",
        )
        try:
            with urllib.request.urlopen(setup, timeout=10) as response:
                if response.status not in (200, 201):
                    raise RuntimeError("due fixture import failed")
                message_id = (json.load(response).get("data") or {}).get("id")
                if type(message_id) is not int or message_id < 1:
                    raise RuntimeError("due fixture import returned no message")
        except urllib.error.HTTPError as error:
            if error.code != 404:
                raise
            error.close()

    return prepare_thread
