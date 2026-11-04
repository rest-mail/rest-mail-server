"""The delivery log and the pipeline log list a send made for them after the
reset in the own-data run (#1785, #1385): a 200 that does not list it fails."""

import json
import os
import sys
import types
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

import yaml

from test_hooks_pipeline import load_hooks

ROOT = Path(__file__).resolve().parents[2]
DELIVERY = "/api/v1/admin/logs/delivery"
PIPELINE = "/api/v1/admin/pipelines/logs"


def transaction(path, status):
    return {
        "protocol": "http:", "host": "api", "port": "8080",
        "fullPath": path + "?limit=20", "name": f"{path} > x > {status} > application/json",
        "request": {"headers": {"Authorization": "Bearer session"}},
    }


def answered(sent, status, rows):
    sent["real"] = {"statusCode": status, "body": json.dumps({"data": rows})}
    return sent


def registered_after():
    """Each operation's after hooks, by name, as the entrypoint registers them."""
    stub = types.ModuleType("vertrag_hooks")
    stub.stash = {}
    after = {}

    def recorder(store):
        def register(name):
            def wrap(fn):
                store.setdefault(name, []).append(fn.__name__)
                return fn
            return wrap
        return register

    stub.before = recorder({})
    stub.after = recorder(after)
    config = yaml.safe_load((ROOT / "vertrag.yml").read_text())
    with patch.dict(sys.modules, {"vertrag_hooks": stub}):
        import runpy
        runpy.run_path(str(ROOT / config["hookfiles"]))
    return after


class LogListings(unittest.TestCase):
    def setUp(self):
        self.hooks = load_hooks()
        self.globals = self.hooks.the_delivery_log_after_a_reset.__globals__
        self.patch = patch.dict(self.globals)
        names = self.patch.start()
        self.addCleanup(self.patch.stop)
        self.start_clean = Mock()
        names["start_clean"] = self.start_clean
        self.made = []

        def fixture_call(transaction, method, path, body=None, expect=(200, 201, 204)):
            self.made.append((method, path, body))
            return {"id": 9, "message_id": "<made@restmail.test>"}

        names["fixture_call"] = fixture_call
        sleep = patch.object(self.globals["time"], "sleep")
        self.sleep = sleep.start()
        self.addCleanup(sleep.stop)
        self.globals["hooks"].stash.clear()
        env = patch.dict("os.environ")
        env.start()
        self.addCleanup(env.stop)
        os.environ["CONTRACT_RESET_SEED"] = "1"

    def before(self, operation, sent):
        for hook in self.hooks.registered_before[operation]:
            hook(sent)

    def after(self, sent):
        with patch.object(
            self.globals["hooks"], "get_json", lambda t, which: json.loads(t[which]["body"]), create=True
        ):
            self.hooks.the_listing_holds_what_was_made(sent)

    def the_send(self):
        [(method, path, body)] = self.made
        self.assertEqual((method, path), ("POST", "/api/v1/messages/send"))
        self.assertEqual(body["from"], self.globals["ADMIN"])
        [recipient] = body["to"]
        return recipient

    def test_the_after_hook_checks_both_listings(self):
        after = registered_after()
        for operation in ("getDeliveryLogs", "listPipelineLogs"):
            self.assertIn("the_listing_holds_what_was_made", after.get(operation, []), operation)

    def test_the_delivery_log_resets_first(self):
        hooks = self.hooks.registered_before["getDeliveryLogs"]
        self.assertEqual(hooks[0].__name__, "the_delivery_log_after_a_reset")
        names = [h.__name__ for h in self.hooks.registered_before["listPipelineLogs"]]
        self.assertLess(names.index("pipeline_reads_after_a_reset"), names.index("the_pipeline_log_holds_a_send"))

    def test_the_delivery_log_lists_the_queued_message(self):
        sent = transaction(DELIVERY, 200)
        self.before("getDeliveryLogs", sent)
        self.start_clean.assert_called_once_with(sent)
        recipient = self.the_send()
        # A remote recipient of its own, so the send is queued.
        self.assertTrue(recipient.startswith("contract-log-"))
        self.assertTrue(recipient.endswith("@" + self.globals["OWN_LOG_DOMAIN"]))
        self.after(answered(sent, 200, [{"id": 3, "recipient": "other@x.test"}, {"id": 4, "recipient": recipient}]))
        self.assertNotIn("fail", sent)

    def test_a_delivery_log_without_the_message_fails(self):
        sent = transaction(DELIVERY, 200)
        self.before("getDeliveryLogs", sent)
        self.after(answered(sent, 200, []))
        self.assertIn("does not hold the queued message made for it (recipient 'contract-log-", sent["fail"])

    def test_the_pipeline_log_lists_the_delivery_trace(self):
        # The documented 200 asks for direction=inbound, action=continue and
        # outcome=delivered, so the send goes to a local mailbox: the fixture
        # admin's own.
        sent = transaction(PIPELINE, 200)
        self.before("listPipelineLogs", sent)
        self.start_clean.assert_called_once_with(sent)
        self.assertEqual(self.the_send(), self.globals["ADMIN"])
        self.sleep.assert_called_once_with(1)
        rows = [
            {"direction": "outbound", "rfc_message_id": "<made@restmail.test>"},
            {"direction": "inbound", "rfc_message_id": "<made@restmail.test>", "outcome": "delivered"},
        ]
        self.after(answered(sent, 200, rows))
        self.assertNotIn("fail", sent)

    def test_a_pipeline_log_with_only_another_trace_fails(self):
        sent = transaction(PIPELINE, 200)
        self.before("listPipelineLogs", sent)
        rows = [
            {"direction": "outbound", "rfc_message_id": "<made@restmail.test>"},
            {"direction": "inbound", "rfc_message_id": "<other@restmail.test>"},
        ]
        self.after(answered(sent, 200, rows))
        self.assertIn(
            "does not hold the delivery trace made for it "
            "(direction 'inbound', rfc_message_id '<made@restmail.test>')",
            sent["fail"],
        )
        self.assertIn("it answered 2 other rows", sent["fail"])

    def test_a_send_without_a_message_id_is_an_error(self):
        self.globals["fixture_call"] = lambda *a, **k: {"id": 9}
        with self.assertRaises(RuntimeError):
            self.before("listPipelineLogs", transaction(PIPELINE, 200))

    def test_a_listing_with_nothing_made_fails(self):
        # The control: a 200 whose before hook made nothing.
        for path in (DELIVERY, PIPELINE):
            with self.subTest(path=path):
                sent = answered(transaction(path, 200), 200, [])
                self.after(sent)
                self.assertEqual(sent["fail"], "the listing had nothing made for it to list (#1785, #1789)")

    def test_refusals_reset_and_make_nothing(self):
        for operation, path in (("getDeliveryLogs", DELIVERY), ("listPipelineLogs", PIPELINE)):
            for status in ("401", "422"):
                with self.subTest(operation=operation, status=status):
                    self.start_clean.reset_mock()
                    sent = transaction(path, status)
                    self.before(operation, sent)
                    self.start_clean.assert_called_once_with(sent)
                    self.after(answered(sent, int(status), []))
                    self.assertNotIn("fail", sent)
        self.assertEqual(self.made, [])

    def test_nothing_happens_without_a_reset(self):
        os.environ.pop("CONTRACT_RESET_SEED")
        for operation, path in (("getDeliveryLogs", DELIVERY), ("listPipelineLogs", PIPELINE)):
            sent = transaction(path, 200)
            self.before(operation, sent)
            self.after(answered(sent, 200, []))
            self.assertNotIn("fail", sent)
        self.start_clean.assert_not_called()
        self.assertEqual(self.made, [])


if __name__ == "__main__":
    unittest.main()
