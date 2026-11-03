"""Give coverage's literal contact, draft and folder targets disposable rows."""

import os
import time
import urllib.parse

import vertrag_hooks as hooks


def register_contact_draft_hooks(fixture_call, admin):
    def coverage_path(transaction, status):
        if os.environ.get("CONTRACT_COVERAGE") != "1":
            return None
        if f" > {status}" not in transaction.get("name", ""):
            return None
        return urllib.parse.urlsplit(transaction["fullPath"])

    def replace_target(transaction, url, target):
        parent = url.path.rsplit("/", 1)[0]
        transaction["fullPath"] = urllib.parse.urlunsplit(
            url._replace(path=parent + "/" + urllib.parse.quote(str(target), safe=""))
        )

    @hooks.before("updateContact")
    @hooks.before("deleteContact")
    def a_contact_for_the_coverage_placeholder(transaction):
        status = 204 if " > 204" in transaction.get("name", "") else 200
        url = coverage_path(transaction, status)
        if url is None:
            return
        parts = url.path.split("/")
        if (
            len(parts) != 7
            or parts[:5] != ["", "api", "v1", "accounts", "1"]
            or parts[5] != "contacts"
            or parts[6] not in (("1", "41") if status == 204 else ("1",))
        ):
            return
        contact = fixture_call(
            transaction,
            "POST",
            "/api/v1/accounts/1/contacts",
            {"email": f"contract-probe-{time.time_ns()}@example.test"},
        )
        replace_target(transaction, url, contact["id"])

    @hooks.before("updateDraft")
    def a_draft_for_the_coverage_placeholder(transaction):
        url = coverage_path(transaction, 200)
        if url is None or url.path not in (
            "/api/v1/messages/draft/1",
            "/api/v1/messages/draft/12",
        ):
            return
        draft = fixture_call(
            transaction,
            "POST",
            "/api/v1/messages/draft",
            {
                "from": admin,
                "to": [admin],
                "subject": "Contract coverage draft",
                "body_text": "Not sent yet.",
            },
        )
        replace_target(transaction, url, draft["id"])

    @hooks.before("deleteFolder")
    def a_folder_for_the_coverage_placeholder(transaction):
        url = coverage_path(transaction, 204)
        if url is None or url.path != "/api/v1/accounts/1/folders/Work":
            return
        folder = fixture_call(
            transaction,
            "POST",
            "/api/v1/accounts/1/folders",
            {"name": f"contract-delete-{time.time_ns()}"},
        )
        replace_target(transaction, url, folder["name"])
