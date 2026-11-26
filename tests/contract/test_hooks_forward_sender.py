"""The forwardMessage hook gives a valid probe's sender a hosted, signing
mailbox with room for the sent copy in the fuzz process, without touching the
request (#2051)."""

from http.server import ThreadingHTTPServer
import importlib.util
import json
from pathlib import Path
import sys
import threading
import types
import unittest
from unittest.mock import patch
import urllib.error
import urllib.parse

sys.path.insert(0, str(Path(__file__).resolve().parent))

from test_hooks_send_message import FixtureAPI  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]

GENERATED = "ascii@xn--fsqu00a.xn--3lr804guic"
VALID = "fuzz: /api/v1/messages/{id}/forward > Forward a message > 201 > application/json · body · valid"


def load_hooks():
    """hooks.py imports vertrag_hooks, which only vertrag provides. The stub
    records the order in which before hooks are registered."""
    stub = types.ModuleType("vertrag_hooks")
    stub.stash = {}
    stub.registered = []

    def before(name):
        def register(fn):
            stub.registered.append((name, fn.__name__))
            return fn
        return register

    stub.before = before
    stub.after = lambda name: (lambda fn: fn)
    stub.get_json = lambda transaction, which="request": json.loads(transaction[which]["body"])
    spec = importlib.util.spec_from_file_location("contract_forward_sender_hooks", ROOT / "tests/contract/hooks.py")
    module = importlib.util.module_from_spec(spec)
    with patch.dict(sys.modules, {"vertrag_hooks": stub}):
        spec.loader.exec_module(module)
    return module, stub


class ForwardFixtureAPI(FixtureAPI):
    """The send fixture's admin endpoints, plus the account listing without a
    domain filter and the account update."""

    def do_GET(self):
        url = urllib.parse.urlparse(self.path)
        if url.path == "/api/v1/admin/mailboxes" and "domain_id" not in urllib.parse.parse_qs(url.query):
            self.server.state["calls"].append("GET " + self.path)
            return self.answer(200, {"data": self.server.state["mailboxes"]})
        return super().do_GET()

    def do_PATCH(self):
        state = self.server.state
        state["calls"].append("PATCH " + self.path)
        body = self.read_body()
        state["patches"].append((self.path, self.headers.get("Authorization"), body))
        if state.get("patch_refusal"):
            return self.answer(*state["patch_refusal"])
        mailbox_id = int(self.path.rsplit("/", 1)[1])
        mailbox = next(m for m in state["mailboxes"] if m["id"] == mailbox_id)
        mailbox.update(body)
        self.answer(200, {"data": mailbox})


class ForwardSenderProbes(unittest.TestCase):
    def setUp(self):
        self.hooks, self.stub = load_hooks()
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), ForwardFixtureAPI)
        self.server.state = {
            "calls": [], "patches": [], "domains": [{"id": 1, "name": "restmail.test"}], "signing": {1},
            "mailboxes": [{"id": 7, "domain_id": 1, "address": "admin@restmail.test",
                           "quota_bytes": 100, "quota_used_bytes": 100}],
        }
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        for name, token in (("mfa_admin_token", "two-factor"), ("admin_token", "setup")):
            patcher = patch.object(self.hooks, name, lambda origin, token=token: token)
            patcher.start()
            self.addCleanup(patcher.stop)
        fuzz = patch.dict(self.hooks.os.environ, {"CONTRACT_FUZZ": "1"})
        fuzz.start()
        self.addCleanup(fuzz.stop)

    def forward(self, body, name=VALID):
        transaction = {"name": name, "protocol": "http:", "host": "127.0.0.1", "port": str(self.server.server_port),
                       "fullPath": "/api/v1/messages/7/forward",
                       "request": {"body": json.dumps(body, ensure_ascii=False),
                                   "headers": {"Authorization": "Bearer probe",
                                               "Idempotency-Key": "contract-idempotency-conflict"}}}
        before = json.loads(json.dumps(transaction))
        self.hooks.a_forward_sender_with_room(transaction)
        self.assertEqual(transaction, before, "the request must stay as generated")

    def held_sender(self, quota_bytes, quota_used_bytes=0):
        """GENERATED as a generated createAccount body left it: hosted, signing,
        with a cap of its own."""
        state = self.server.state
        state["domains"].append({"id": 2, "name": "xn--fsqu00a.xn--3lr804guic"})
        state["signing"].add(2)
        state["mailboxes"].append({"id": 136, "domain_id": 2, "address": GENERATED,
                                   "quota_bytes": quota_bytes, "quota_used_bytes": quota_used_bytes})
        return state["mailboxes"][-1]

    def test_a_generated_sender_held_at_a_tiny_cap_gets_the_default_cap_again(self):
        # Scheduled fuzz run 45622224387: createAccount made the sender with
        # quota_bytes 174, and the valid forward was refused 422 mailbox_full.
        sender = self.held_sender(174)
        self.forward({"from": GENERATED, "to": ["用户@例子.广告", "someone@example.com"]})
        self.assertEqual(self.server.state["patches"],
                         [("/api/v1/admin/mailboxes/136", "Bearer two-factor", {"quota_bytes": 0})])
        self.assertEqual(sender["quota_bytes"], 0)

    def test_a_cap_used_up_to_its_last_megabyte_is_restored(self):
        self.held_sender(5 << 20, (5 << 20) - (1 << 20) + 1)
        self.forward({"from": GENERATED, "to": ["someone@example.com"]})
        self.assertEqual(len(self.server.state["patches"]), 1)

    def test_a_sender_with_room_or_no_cap_of_its_own_is_left_alone(self):
        sender = self.held_sender(5 << 20, (4 << 20))
        self.forward({"from": GENERATED, "to": ["someone@example.com"]})
        sender["quota_bytes"] = 0
        sender["quota_used_bytes"] = 1 << 40
        self.forward({"from": GENERATED, "to": ["someone@example.com"]})
        self.assertEqual(self.server.state["patches"], [])

    def test_an_unknown_sender_gets_a_signing_mailbox_and_its_recipients_stay_inside(self):
        self.forward({"from": "someone@example.com", "to": ["a@elsewhere.example", "not-an-address"]})
        state = self.server.state
        self.assertEqual([d["name"] for d in state["domains"]],
                         ["restmail.test", "example.com", "elsewhere.example"])
        self.assertEqual([m["address"] for m in state["mailboxes"]],
                         ["admin@restmail.test", "someone@example.com", "a@elsewhere.example"])
        self.assertIn("PUT /api/v1/admin/dkim/2", state["calls"])
        self.assertEqual(state["patches"], [])

    def test_a_sender_the_server_will_not_host_keeps_its_refusal(self):
        self.forward({"from": "someone@example.invalid", "to": ["a@elsewhere.example"]})
        state = self.server.state
        self.assertEqual([m["address"] for m in state["mailboxes"]], ["admin@restmail.test"])
        self.assertEqual(state["patches"], [])

    def test_the_fixture_admin_and_the_documented_mailbox_full_refusal_are_left_alone(self):
        self.held_sender(174)
        self.forward({"from": "admin@restmail.test", "to": ["someone@imap.restmail.test"]})
        self.forward({"from": "Admin@RestMail.test", "to": ["someone@imap.restmail.test"]})
        self.forward({"from": GENERATED, "to": ["target@remote.test"]},
                     name="/api/v1/messages/{id}/forward > Forward a message > 422 > application/json")
        self.assertEqual(self.server.state["calls"], [])

    def test_the_examples_and_coverage_processes_are_left_alone(self):
        # Their forwards pass today; only the fuzz process is prepared.
        self.held_sender(174)
        for environment in ({"CONTRACT_FUZZ": ""}, {"CONTRACT_FUZZ": "", "CONTRACT_COVERAGE": "1"}):
            with self.subTest(environment=environment), patch.dict(self.hooks.os.environ, environment):
                self.forward({"from": GENERATED, "to": ["someone@example.com"]})
                self.forward({"from": "someone@example.com", "to": ["a@elsewhere.example"]},
                             name="/api/v1/messages/{id}/forward > Forward a message > 201 > application/json")
        self.assertEqual(self.server.state["calls"], [])

    def test_malformed_bodies_are_left_to_the_probe(self):
        for body in ({"to": ["someone@example.com"]}, {"from": 42}, {"from": "a@b@c"}, {"from": "no-at-sign"},
                     ["not", "an", "object"], "text", None):
            with self.subTest(body=body):
                self.forward(body)
        self.assertEqual(self.server.state["calls"], [])

    def test_a_failed_restore_stops_the_probe_rather_than_hiding_it(self):
        self.held_sender(174)
        self.server.state["patch_refusal"] = (403, {"error": {"code": "recent_two_factor_required"}})
        with self.assertRaises(urllib.error.HTTPError) as caught:
            self.forward({"from": GENERATED, "to": ["someone@example.com"]})
        self.assertEqual(caught.exception.code, 403)
        caught.exception.close()


class ForwardHookOrder(unittest.TestCase):
    def test_the_sender_is_prepared_after_a_clean_start_resets_the_fixtures(self):
        # In the own-data phase on_a_message_of_its_own resets the database for
        # each forward. A sender prepared before that reset would be gone.
        _, stub = load_hooks()
        forward = [fn for name, fn in stub.registered if name == "forwardMessage"]
        self.assertIn("a_forward_sender_with_room", forward)
        self.assertLess(forward.index("on_a_message_of_its_own"), forward.index("a_forward_sender_with_room"))


if __name__ == "__main__":
    unittest.main()
