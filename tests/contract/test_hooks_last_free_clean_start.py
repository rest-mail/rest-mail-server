"""The last operations step 2 found free start clean in the own-data run
(#1385): each exchange resets first, the account read and the calendar list go
to the seeded account, and the calendar list must list an invitation made for
it."""

import json
import os
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

from test_hooks_pipeline import load_hooks

OPERATIONS = {
    "getAccount": ("/api/v1/accounts/1", "GET"),
    "listCalendarEvents": ("/api/v1/accounts/1/calendar-events", "GET"),
    "respondToCalendar": ("/api/v1/messages/1/calendar-reply", "POST"),
    "accountEvents": ("/api/v1/accounts/1/events", "GET"),
    "probeServices": ("/api/v1/admin/test/probe", "POST"),
    "verifyDelivery": ("/api/v1/admin/test/verify?to=recipient%40example.com&timeout=0s", "GET"),
}


def transaction(path, name, body=None):
    return {
        "protocol": "http:", "host": "api", "port": "8080",
        "fullPath": path, "name": name,
        "request": {"body": json.dumps(body), "headers": {"Authorization": "Bearer session"}},
    }


def answered(sent, status, data):
    sent["real"] = {"statusCode": status, "body": json.dumps({"data": data})}
    return sent


class CleanStart(unittest.TestCase):
    def setUp(self):
        self.hooks = load_hooks()
        self.hook = self.hooks.the_last_free_operations_after_a_reset
        self.globals = self.hook.__globals__
        self.patch = patch.dict(self.globals)
        names = self.patch.start()
        self.addCleanup(self.patch.stop)
        self.start_clean = Mock()
        names["start_clean"] = self.start_clean
        self.made = []

        self.invitations = [{"uid": self.globals["OWN_INVITATION_UID"], "message_id": 31}]
        # What each read of the sent message's recipients answers, in turn; the
        # last answer repeats.
        self.deliveries = [["delivered"]]

        def fixture_call(transaction, method, path, body=None, expect=(200, 201, 204)):
            self.made.append((method, path, body))
            if method == "GET" and path.endswith("/calendar-events"):
                return self.invitations
            if method == "GET" and path.endswith("/recipients"):
                states = self.deliveries.pop(0) if len(self.deliveries) > 1 else self.deliveries[0]
                return {"message_id": 7, "recipients": [
                    {"address": self.globals["ADMIN"], "state": state} for state in states]}
            return {"id": 7}

        names["fixture_call"] = fixture_call
        # How many fixture calls had been made each time restmail.test was
        # given an inbound pipeline that does not greylist.
        self.pipelines = []
        names["an_inbound_pipeline_without_greylisting"] = Mock(
            side_effect=lambda transaction: self.pipelines.append(len(self.made)))
        sleep = patch.object(self.globals["time"], "sleep")
        self.sleep = sleep.start()
        self.addCleanup(sleep.stop)
        names["mailbox_id"] = Mock(return_value=4)
        self.stash = self.globals["hooks"].stash
        self.stash.clear()
        self.clean(True)

    def clean(self, on):
        env = patch.dict("os.environ")
        env.start()
        self.addCleanup(env.stop)
        os.environ.pop("CONTRACT_RESET_SEED", None)
        if on:
            os.environ["CONTRACT_RESET_SEED"] = "1"

    def get_json(self, sent, which):
        return json.loads(sent[which]["body"])

    def after(self, hook, sent):
        with patch.object(self.globals["hooks"], "get_json", self.get_json, create=True):
            hook(sent)

    def test_each_refusal_resets_first_and_keeps_its_request(self):
        for operation, (path, method) in OPERATIONS.items():
            for status in ("400", "404", "422"):
                with self.subTest(operation=operation, status=status):
                    self.start_clean.reset_mock()
                    sent = transaction(path, f"{path} > x > {status} > application/json", {"a": 1})
                    before = json.dumps(sent, sort_keys=True)
                    self.hook(sent)
                    self.start_clean.assert_called_once_with(sent)
                    self.assertEqual(json.dumps(sent, sort_keys=True), before)
        self.assertEqual(self.made, [])

    def test_it_is_each_operations_first_hook(self):
        for operation in OPERATIONS:
            with self.subTest(operation=operation):
                self.assertIs(self.hooks.registered_before[operation][0].__name__, self.hook.__name__)

    def test_nothing_happens_without_a_reset(self):
        self.clean(False)
        for path, _ in OPERATIONS.values():
            sent = transaction(path, f"{path} > x > 200 > application/json")
            self.hook(sent)
            self.assertEqual(sent["fullPath"], path)
        self.start_clean.assert_not_called()
        self.assertEqual(self.made, [])

    def test_the_account_read_goes_to_the_seeded_account(self):
        sent = transaction("/api/v1/accounts/1", "/api/v1/accounts/{id} > x > 200 > application/json")
        self.hook(sent)
        self.assertEqual(sent["fullPath"], "/api/v1/accounts/4")
        self.assertEqual(self.made, [])

    def test_the_probe_success_needs_nothing_made(self):
        path = "/api/v1/admin/test/probe"
        sent = transaction(path, f"{path} > x > 200 > application/json", {"services": ["db"]})
        self.hook(sent)
        self.assertEqual(sent["fullPath"], path)
        self.assertEqual(self.made, [])

    def test_the_delivery_check_needs_nothing_made(self):
        # Its documented 200 asks about an address no mailbox has (#388).
        path = "/api/v1/admin/test/verify?to=recipient%40example.com&timeout=0s"
        sent = transaction(path, "/api/v1/admin/test/verify > x > 200 > application/json")
        self.hook(sent)
        self.assertEqual(sent["fullPath"], path)
        self.assertEqual(self.made, [])

    def test_the_calendar_list_lists_the_invitation_made_for_it(self):
        name = "/api/v1/accounts/{id}/calendar-events > x > 200 > application/json"
        sent = transaction("/api/v1/accounts/1/calendar-events", name)
        self.hook(sent)
        self.assertEqual(sent["fullPath"], "/api/v1/accounts/4/calendar-events")
        [(method, path, body), (read, waited, _)] = self.made
        self.assertEqual((method, path), ("POST", "/api/v1/messages/send"))
        self.assertEqual((read, waited), ("GET", "/api/v1/messages/7/recipients"))
        self.assertEqual(body["to"], [body["from"]])
        self.assertEqual(body["calendar_event"]["method"], "REQUEST")
        # The invitation comes back through restmail.test's inbound pipeline
        # (#1564), so the domain gets one that does not greylist before the
        # send: the default one defers a new sender for five minutes.
        self.assertEqual(self.pipelines, [0])
        uid = body["calendar_event"]["uid"]
        self.after(self.hooks.the_listing_holds_what_was_made, answered(sent, 200, [{"uid": uid}]))
        self.assertNotIn("fail", sent)

    def test_a_calendar_list_without_the_invitation_fails(self):
        name = "/api/v1/accounts/{id}/calendar-events > x > 200 > application/json"
        sent = transaction("/api/v1/accounts/1/calendar-events", name)
        self.hook(sent)
        self.after(self.hooks.the_listing_holds_what_was_made, answered(sent, 200, []))
        self.assertIn("does not hold the invitation made for it", sent["fail"])

    def test_a_calendar_list_with_nothing_made_fails(self):
        name = "/api/v1/accounts/{id}/calendar-events > x > 200 > application/json"
        sent = answered(transaction("/api/v1/accounts/4/calendar-events", name), 200, [])
        self.after(self.hooks.the_listing_holds_what_was_made, sent)
        self.assertEqual(sent["fail"], "the listing had nothing made for it to list (#1785, #1789)")

    def test_the_reply_goes_to_an_invitation_made_after_the_reset(self):
        for status, body in (("200", {"from": "admin@restmail.test", "response": "ACCEPTED"}),
                             ("403", {"from": "someone@other.test", "response": "ACCEPTED"})):
            with self.subTest(status=status):
                self.made.clear()
                name = f"/api/v1/messages/{{id}}/calendar-reply > x > {status} > application/json"
                sent = transaction("/api/v1/messages/1/calendar-reply", name, body)
                self.hook(sent)
                self.start_clean.assert_called_with(sent)
                self.assertEqual(sent["fullPath"], "/api/v1/messages/31/calendar-reply")
                self.assertEqual(json.loads(sent["request"]["body"]), body)
                [(method, path, made), (wait, waited, _), (read, listing, _)] = self.made
                self.assertEqual((method, path), ("POST", "/api/v1/messages/send"))
                self.assertEqual((wait, waited), ("GET", "/api/v1/messages/7/recipients"))
                self.assertEqual(made["to"], [made["from"]])
                self.assertEqual(made["calendar_event"]["method"], "REQUEST")
                self.assertEqual((read, listing), ("GET", "/api/v1/accounts/4/calendar-events"))
                self.assertEqual(self.pipelines[-1], 0)

    def test_a_reply_with_no_invitation_delivered_is_not_sent(self):
        self.invitations = [{"uid": "someone-elses@restmail.test", "message_id": 9}]
        name = "/api/v1/messages/{id}/calendar-reply > x > 200 > application/json"
        sent = transaction("/api/v1/messages/1/calendar-reply", name, {"from": "admin@restmail.test"})
        with self.assertRaisesRegex(RuntimeError, "was not delivered"):
            self.hook(sent)

    def test_the_invitation_is_read_only_once_the_queue_worker_delivered_it(self):
        # A send to a mailbox on this server is queued (#1564): the 201 comes
        # before the queue worker delivers it, so the invitation is looked for
        # only once the sent message's recipient reads delivered.
        self.deliveries = [["queued"], ["retrying"], ["delivered"]]
        for name, path in (
            ("/api/v1/accounts/{id}/calendar-events > x > 200 > application/json",
             "/api/v1/accounts/1/calendar-events"),
            ("/api/v1/messages/{id}/calendar-reply > x > 200 > application/json",
             "/api/v1/messages/1/calendar-reply"),
        ):
            with self.subTest(name=name):
                self.made.clear()
                self.sleep.reset_mock()
                self.deliveries = [["queued"], ["retrying"], ["delivered"]]
                self.hook(transaction(path, name, {"from": "admin@restmail.test"}))
                reads = [path for method, path, _ in self.made if method == "GET"]
                self.assertEqual(reads[:3], ["/api/v1/messages/7/recipients"] * 3)
                self.assertEqual(self.sleep.call_count, 2)

    def test_an_invitation_that_bounced_is_an_error(self):
        self.deliveries = [["queued"], ["failed"]]
        name = "/api/v1/messages/{id}/calendar-reply > x > 200 > application/json"
        sent = transaction("/api/v1/messages/1/calendar-reply", name, {"from": "admin@restmail.test"})
        with self.assertRaisesRegex(RuntimeError, "was not delivered: .*failed"):
            self.hook(sent)
        self.assertFalse(any(path.endswith("/calendar-events") for _, path, _ in self.made))

    def test_an_invitation_never_delivered_is_an_error_after_the_wait(self):
        self.deliveries = [["queued"]]
        clock = iter(range(0, 10_000, 30))
        name = "/api/v1/accounts/{id}/calendar-events > x > 200 > application/json"
        sent = transaction("/api/v1/accounts/1/calendar-events", name)
        with patch.object(self.globals["time"], "monotonic", lambda: next(clock)):
            with self.assertRaisesRegex(RuntimeError, "not delivered within 60 seconds: .*queued"):
                self.hook(sent)

    def test_the_stream_is_not_given_events(self):
        # Its example is a literal stream the live server never sends, so the
        # run does not provide event_stream and the stream gets nothing made.
        name = "/api/v1/accounts/{id}/events > x > 200 > text/event-stream"
        sent = transaction("/api/v1/accounts/1/events", name)
        self.hook(sent)
        self.assertEqual(sent["fullPath"], "/api/v1/accounts/4/events")
        self.assertNotIn("Last-Event-ID", sent["request"]["headers"])
        self.assertEqual(self.made, [])

    def test_the_listing_after_hook_is_registered_for_the_calendar_list(self):
        # The stub's after() registers nothing, so read the source.
        source = (Path(__file__).resolve().parent / "hooks.py").read_text()
        stack = source.partition("\ndef the_listing_holds_what_was_made(")[0].rsplit("\n\n", 1)[-1]
        self.assertIn('@hooks.after("listCalendarEvents")', stack.splitlines())


if __name__ == "__main__":
    unittest.main()
