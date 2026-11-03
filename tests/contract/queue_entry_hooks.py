"""Give each queue coverage success request a disposable target (Part of #1443)."""

import json
import re
import subprocess

import vertrag_hooks as hooks
from coverage_target_hooks import coverage_placeholder, replace_path


def register_queue_entry_hooks(shared):
    @hooks.before("deleteQueueEntry")
    @hooks.before("getQueueEntry")
    @hooks.before("retryQueueEntry")
    @hooks.before("bounceQueueEntry")
    def a_disposable_queue_entry_for_coverage(transaction):
        method = transaction.get("request", {}).get("method")
        path = transaction.get("fullPath", "").partition("?")[0]
        match = re.fullmatch(
            r"/api/v1/admin/queue/([1-9][0-9]{0,9})(/retry|/bounce)?", path
        )
        if match is None or int(match[1]) > 4294967295:
            return
        action = match[2] or ""
        targets = {
            ("DELETE", ""): 204,
            ("GET", ""): 200,
            ("POST", "/retry"): 200,
            ("POST", "/bounce"): 200,
        }
        status = targets.get((method, action))
        if (
            status is None
            or not coverage_placeholder(transaction, status, (path,))
            or shared["session_address"](transaction)
            not in (shared["ADMIN"], shared["mfa_admin"].ADDRESS)
        ):
            return
        completed = subprocess.run(
            ["chore", "test:contract-queue-entry-fixture"],
            check=True,
            capture_output=True,
            timeout=20,
            text=True,
        )
        for line in completed.stdout.splitlines():
            if not line.startswith("QUEUE_ENTRY_FIXTURE="):
                continue
            identifier = json.loads(line.partition("=")[2]).get("id")
            if type(identifier) is int and 0 < identifier <= 4294967295:
                replace_path(
                    transaction,
                    f"/api/v1/admin/queue/{identifier}{action}",
                )
                return
        raise RuntimeError("the coverage fixture returned no queue entry target id")
