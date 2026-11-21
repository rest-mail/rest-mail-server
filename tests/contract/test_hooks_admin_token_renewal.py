"""The fixture admin's session is renewed, not cached for the whole run (#2195).

The own-data phase resets and seeds before every exchange and runs forty-odd
minutes into the job now that the full suite runs on the pull request, so a
setup session that never renews is dead by the time the admin exchanges run:
they answered 401 "Invalid or expired token" on two different exchanges before
this was found. The two-factor and probe sessions had always renewed after ten
minutes; this pins the same rule on the fixture admin's.
"""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import json
from pathlib import Path
import sys
import threading
import time
import types
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[2]
TOKEN_LIFETIME = 600


def load_hooks():
    """hooks.py imports vertrag_hooks, which only vertrag provides."""
    stub = types.ModuleType("vertrag_hooks")
    stub.stash = {}
    stub.before = lambda name: (lambda fn: fn)
    stub.after = lambda name: (lambda fn: fn)
    spec = importlib.util.spec_from_file_location("contract_admin_token_hooks", ROOT / "tests/contract/hooks.py")
    module = importlib.util.module_from_spec(spec)
    with patch.dict(sys.modules, {"vertrag_hooks": stub}):
        spec.loader.exec_module(module)
    return module, stub


class LoginAPI(BaseHTTPRequestHandler):
    """The one endpoint admin_token calls: POST /api/v1/auth/login."""

    def log_message(self, *args):
        pass

    def do_POST(self):
        self.server.calls.append(self.path)
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))))
        # Each sign-in answers a token of its own, so a cache hit and a renewal
        # cannot be confused by returning the same string twice.
        token = "session-%d" % len(self.server.calls)
        self.server.emails.append(body.get("email"))
        data = json.dumps({"data": {"access_token": token}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


class FakeClock:
    def __init__(self):
        self.now = 1_000_000.0

    def time(self):
        return self.now

    def sleep(self, seconds):
        time.sleep(seconds)


class AdminTokenRenewalTests(unittest.TestCase):
    def setUp(self):
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), LoginAPI)
        self.server.calls, self.server.emails = [], []
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.hooks, self.stub = load_hooks()
        self.origin = "http://127.0.0.1:" + str(self.server.server_address[1])
        self.clock = FakeClock()
        self.hooks.time = self.clock

    def test_a_second_call_inside_the_window_reuses_the_session(self):
        first = self.hooks.admin_token(self.origin)
        self.clock.now += TOKEN_LIFETIME - 1
        second = self.hooks.admin_token(self.origin)
        self.assertEqual(first, second)
        self.assertEqual(len(self.server.calls), 1, self.server.calls)

    def test_the_session_is_renewed_once_it_is_older_than_the_lifetime(self):
        first = self.hooks.admin_token(self.origin)
        self.clock.now += TOKEN_LIFETIME + 1
        second = self.hooks.admin_token(self.origin)
        self.assertNotEqual(first, second)
        self.assertEqual(len(self.server.calls), 2, self.server.calls)
        # The renewal signs in for the same fixture admin, not another account.
        self.assertEqual(self.server.emails, [self.hooks.ADMIN, self.hooks.ADMIN])

    def test_a_bare_string_already_in_the_stash_is_still_a_session(self):
        # The hook tests seed one, and so did every run before the age existed.
        self.stub.stash["setup_admin_tokens"] = {self.origin: "setup-session"}
        self.assertEqual(self.hooks.admin_token(self.origin), "setup-session")
        self.assertEqual(self.server.calls, [])

    def test_a_renewal_replaces_the_stashed_session_rather_than_adding_one(self):
        self.hooks.admin_token(self.origin)
        self.clock.now += TOKEN_LIFETIME + 1
        self.hooks.admin_token(self.origin)
        stashed = self.stub.stash["setup_admin_tokens"]
        self.assertEqual(list(stashed), [self.origin])
        self.assertEqual(stashed[self.origin]["token"], "session-2")
        self.assertEqual(stashed[self.origin]["at"], self.clock.now)


if __name__ == "__main__":
    unittest.main()
