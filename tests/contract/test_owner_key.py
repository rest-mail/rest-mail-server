"""The run's throwaway owner key signs as the owner endpoints verify (#1162)."""

import base64
import json
import stat
import struct
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch

import owner_key

# RFC 8032, section 7.1, TEST 1 and TEST 2.
VECTORS = [
    ("9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60",
     "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a", "",
     "e5564300c360ac729086e2cc806e828a84877f1eb8e5d974d873e06522490155"
     "5fb8821590a33bacc61e39701cf9b46bd25bf5f0595bbe24655141438e7a100b"),
    ("4ccd089b28ff96da9db6c346ec114e0f5b8a319f35aba624da8cf6ed4fb8a6fb",
     "3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c", "72",
     "92a009a9f0d4cab8720e820b5f642540a2b27b5416503f8fb3762223ebdb69da"
     "085ac1e43e15996e458f3613d0f11d8c387b2eaeb4302aeeb00d291612bb0c00"),
]


def ssh_strings(raw):
    out = []
    while raw:
        (n,) = struct.unpack(">I", raw[:4])
        out.append(raw[4:4 + n])
        raw = raw[4 + n:]
    return out


class Ed25519(unittest.TestCase):
    def test_rfc_8032_vectors(self):
        for seed, public, message, signature in VECTORS:
            with self.subTest(public=public):
                seed = bytes.fromhex(seed)
                self.assertEqual(owner_key.public_key(seed).hex(), public)
                self.assertEqual(owner_key.sign(seed, bytes.fromhex(message)).hex(), signature)

    def test_the_public_key_is_an_authorized_keys_line(self):
        seed, public, _, _ = VECTORS[0]
        kind, blob, comment = owner_key.authorized_key(bytes.fromhex(seed)).split(" ")
        self.assertEqual((kind, comment), ("ssh-ed25519", "contract-owner"))
        self.assertEqual(ssh_strings(base64.b64decode(blob)), [b"ssh-ed25519", bytes.fromhex(public)])

    def test_the_signature_is_ssh_wire_format_over_the_owner_message(self):
        seed = bytes.fromhex(VECTORS[1][0])
        message = owner_key.owner_message("the-challenge", "reset-admin", "a@restmail.test")
        self.assertEqual(message, b"restmail-owner-v1\nreset-admin\na@restmail.test\nthe-challenge")
        self.assertEqual(owner_key.owner_message("c", "status", ""), b"restmail-owner-v1\nstatus\n\nc")
        wire = ssh_strings(base64.b64decode(owner_key.ssh_signature(seed, message)))
        self.assertEqual(wire, [b"ssh-ed25519", owner_key.sign(seed, message)])


class FakeOwnerAPI(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])) or b"{}")
        self.server.calls.append((self.path, self.headers.get("Authorization"), body))
        if self.path == "/api/v1/auth/owner/challenge":
            self.server.challenges += 1
            data = {"challenge": f"challenge-{self.server.challenges}"}
        elif self.path == "/api/v1/auth/owner/token":
            data = {"token": "token-for-" + body["address"], "purpose": body["purpose"]}
        else:
            self.send_response(404)
            self.end_headers()
            return
        payload = json.dumps({"data": data}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


class TheKeyAndItsRequests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.key = Path(directory.name) / "contract" / "owner-key.json"
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), FakeOwnerAPI)
        self.server.calls, self.server.challenges = [], 0
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.origin = "http://127.0.0.1:%d" % self.server.server_address[1]
        patch.object(owner_key.time, "sleep").start()
        self.addCleanup(patch.stopall)

    def seed(self):
        return bytes.fromhex(json.loads(self.key.read_text())["seed"])

    def test_new_keeps_the_seed_private_and_prints_only_the_public_key(self):
        line = owner_key.new(self.key)
        self.assertEqual(stat.S_IMODE(self.key.stat().st_mode), 0o600)
        self.assertEqual(line, owner_key.authorized_key(self.seed()))
        self.assertNotIn(self.seed().hex(), line)

    def test_each_run_registers_a_key_of_its_own(self):
        first = owner_key.new(self.key)
        self.assertNotEqual(owner_key.new(self.key), first)
        self.assertEqual(stat.S_IMODE(self.key.stat().st_mode), 0o600)

    def test_signing_without_a_registered_key_says_how_to_register_one(self):
        with self.assertRaisesRegex(RuntimeError, "test:contract-owner-key -- add"):
            owner_key.signed_answer(self.origin, "status", path=self.key)
        self.assertEqual(self.server.calls, [])

    def test_a_signed_answer_is_a_fresh_challenge_signed_for_its_purpose(self):
        owner_key.new(self.key)
        answer = owner_key.signed_answer(self.origin, "status", path=self.key)
        self.assertEqual(self.server.calls, [("/api/v1/auth/owner/challenge", None, {})])
        self.assertEqual(answer["challenge"], "challenge-1")
        self.assertEqual(answer["public_key"], owner_key.authorized_key(self.seed()))
        self.assertEqual(answer["signature"], owner_key.ssh_signature(
            self.seed(), b"restmail-owner-v1\nstatus\n\nchallenge-1"))

    def test_a_reset_token_is_minted_for_the_address_it_names(self):
        owner_key.new(self.key)
        token = owner_key.reset_token(self.origin, "target@restmail.test", path=self.key)
        self.assertEqual(token, "token-for-target@restmail.test")
        (challenge, _, _), (trade, authorization, body) = self.server.calls
        self.assertEqual((challenge, trade, authorization), (
            "/api/v1/auth/owner/challenge", "/api/v1/auth/owner/token", None))
        self.assertEqual(body, {
            "purpose": "reset-admin", "address": "target@restmail.test", "challenge": "challenge-1",
            "public_key": owner_key.authorized_key(self.seed()),
            "signature": owner_key.ssh_signature(
                self.seed(), b"restmail-owner-v1\nreset-admin\ntarget@restmail.test\nchallenge-1"),
        })


class TheRunRegistersItForTheAuthorizationPhaseOnly(unittest.TestCase):
    def setUp(self):
        self.chores = (Path(__file__).resolve().parents[2] / "chores.yml").read_text()

    def test_registered_just_before_the_authorization_phase_and_revoked_after(self):
        # The gate's no_owner_keys refusals and the ownership phase see no key.
        script = self.chores[self.chores.index("\n  contract:\n"):self.chores.index("\n  contract:pairs:\n")]
        order = [script.index(step) for step in (
            "tests/contract/run.sh gate",
            "chore test:contract-owner-key -- add",
            "tests/contract/run.sh authorization",
            "chore test:contract-owner-key -- revoke",
            "tests/contract/run.sh ownership",
        )]
        self.assertEqual(order, sorted(order))

    def test_only_the_public_key_reaches_the_documented_operator_action(self):
        task = self.chores[self.chores.index("\n  test:contract-owner-key:\n"):]
        task = task[:task.index("\n  test:contract-held-mail-fixture:\n")]
        self.assertIn('"$CONTRACT_FIXTURE_CONFIG" != "restmail.test"', task)
        self.assertIn("python3 -B tests/contract/owner_key.py new |", task)
        self.assertIn("go run ./cmd/api owner-key add", task)
        self.assertIn("go run ./cmd/api owner-key revoke --fingerprint", task)
        self.assertIn("rm -f -- .cache/contract/owner-key.json", task)


if __name__ == "__main__":
    unittest.main()
