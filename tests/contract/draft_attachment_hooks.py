"""Give successful outgoing attachment exchanges their own writable draft."""

import json
import urllib.parse
import urllib.request

import vertrag_hooks as hooks


def register_draft_attachment_hooks(fixture_call, admin_token, admin):
    @hooks.before("uploadDraftAttachment")
    @hooks.before("listDraftAttachments")
    @hooks.before("deleteDraftAttachment")
    def an_attachment_draft_of_its_own(transaction):
        name = transaction.get("name", "")
        if not any(f" > {status}" in name for status in (200, 201, 204)):
            return
        url = urllib.parse.urlsplit(transaction["fullPath"])
        parts = url.path.split("/")
        if len(parts) not in (7, 8) or parts[:5] != ["", "api", "v1", "messages", "draft"] or parts[6] != "attachments":
            return
        if parts[5] not in ("1", "12"):
            return
        draft = fixture_call(transaction, "POST", "/api/v1/messages/draft", {
            "from": admin, "to": [admin], "subject": "Contract attachment upload",
        })
        parts[5] = str(draft["id"])
        hooks.stash["draft_attachment:" + name] = draft["id"]
        if transaction["request"]["method"].upper() != "POST":
            origin = f'{transaction["protocol"]}//{transaction["host"]}:{transaction["port"]}'
            request = urllib.request.Request(
                origin + "/".join(parts[:7]), data=b"attachment", method="POST",
                headers={
                    "Authorization": "Bearer " + admin_token(origin),
                    "Content-Type": "application/octet-stream",
                    "X-Attachment-Filename": "contract.pdf",
                    "X-Attachment-Content-Type": "application/pdf",
                },
            )
            with urllib.request.urlopen(request, timeout=10) as response:
                if response.status != 201:
                    raise RuntimeError("the draft attachment fixture was not stored")
                attachment = json.loads(response.read())["data"]
            if len(parts) == 8:
                parts[7] = str(attachment["id"])
        transaction["fullPath"] = urllib.parse.urlunsplit(url._replace(path="/".join(parts)))

    @hooks.after("uploadDraftAttachment")
    @hooks.after("listDraftAttachments")
    @hooks.after("deleteDraftAttachment")
    def remove_the_attachment_draft_fixture(transaction):
        draft_id = hooks.stash.pop("draft_attachment:" + transaction.get("name", ""), None)
        if draft_id is not None:
            fixture_call(transaction, "DELETE", f"/api/v1/messages/{draft_id}?permanent=true")
