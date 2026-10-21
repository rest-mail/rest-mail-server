"""sendMessage's hook makes restmail.test transactional for the paired 422 alone."""

import importlib.util
import io
import json
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
SEND = "Contract > /api/v1/messages/send > Compose and send a new message > %s > application/json"


class Response(io.BytesIO):
    status = 200

    def __enter__(self):
        return self

    def __exit__(self, *args):
        return False


class ContractSendPolicyHookTests(unittest.TestCase):
    def setUp(self):
        hooks = types.ModuleType("vertrag_hooks")
        hooks.before = hooks.after = lambda *args: lambda function: function
        hooks.stash = {"setup_admin_tokens": {"http://api:8080": "admin"}}
        spec = importlib.util.spec_from_file_location("contract_send_policy_hooks", ROOT / "tests/contract/hooks.py")
        self.module = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {"vertrag_hooks": hooks}):
            spec.loader.exec_module(self.module)
        self.requests = []

    def urlopen(self, request, timeout):
        self.requests.append((request.get_method(), request.full_url, request.data))
        if request.get_method() == "GET":
            domains = [{"id": 3, "name": "other.test"}, {"id": 5, "name": "restmail.test"}]
            return Response(json.dumps({"data": domains}).encode())
        return Response(b"{}")

    def send(self, status, after=False):
        transaction = {"name": SEND % status, "protocol": "http:", "host": "api", "port": "8080"}
        with patch.object(self.module.urllib.request, "urlopen", side_effect=self.urlopen):
            if after:
                self.module.no_sending_policy_after_the_refusal(transaction)
            else:
                self.module.a_sending_policy_for_the_send(transaction)

    def patches(self):
        return [json.loads(data) for method, _, data in self.requests if method == "PATCH"]

    def test_only_the_paired_refusal_is_sent_from_a_transactional_domain(self):
        self.send("201")
        self.assertEqual(self.requests, [])

        self.send("422")
        self.assertEqual(self.requests[-1][:2], ("PATCH", "http://api:8080/api/v1/admin/domains/5"))
        self.assertEqual(self.patches(), [{"sending_policy": "transactional"}])

        self.send("422")
        self.send("400")
        self.assertEqual(self.patches(), [{"sending_policy": "transactional"}, {"sending_policy": ""}])

    def test_the_policy_comes_off_after_the_refusal(self):
        self.send("422")
        self.send("422", after=True)
        self.send("201")
        self.assertEqual(self.patches(), [{"sending_policy": "transactional"}, {"sending_policy": ""}])
        self.assertEqual([m for m, _, _ in self.requests].count("GET"), 1)


if __name__ == "__main__":
    unittest.main()
