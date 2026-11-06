"""renameFolder's hook gives each probe a source folder, and leaves a probe on an
account the run cannot reach to the route, as createFolder's hook does (#1163)."""

import importlib.util
import io
import json
import sys
import types
import unittest
import urllib.error
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]


def load_hooks():
    stub = types.ModuleType("vertrag_hooks")
    stub.stash = {}
    registered = []
    stub.before = lambda name: lambda function: registered.append(name) or function
    stub.after = lambda name: lambda function: function
    stub.get_json = lambda transaction: json.loads(transaction["request"]["body"])
    spec = importlib.util.spec_from_file_location(
        "contract_rename_folder_hooks", ROOT / "tests/contract/hooks.py"
    )
    module = importlib.util.module_from_spec(spec)
    with patch.dict(sys.modules, {"vertrag_hooks": stub}):
        spec.loader.exec_module(module)
    return module, registered


class Reply(io.BytesIO):
    def __init__(self, status):
        super().__init__(b"{}")
        self.status = status

    def __enter__(self):
        return self

    def __exit__(self, *_):
        return False


class RenameFolderFixture(unittest.TestCase):
    def setUp(self):
        self.module, registered = load_hooks()
        self.assertIn("renameFolder", registered)
        self.requests = []
        self.answers = []
        self.enterContext(
            patch.object(self.module, "admin_token", lambda origin: "fixture-token")
        )
        self.enterContext(patch.object(self.module.urllib.request, "urlopen", self.answer))
        # Each probe's source folder is named for the moment it is made; one
        # moment here, so the name is known.
        self.enterContext(patch.object(self.module.time, "time_ns", lambda: 1))

    def answer(self, request, timeout):
        self.requests.append(request)
        status = self.answers.pop(0)
        if status >= 400:
            raise urllib.error.HTTPError(request.full_url, status, "refused", {}, None)
        return Reply(status)

    def rename(self, path):
        transaction = {
            "name": "/api/v1/accounts/{id}/folders/{folder} > Rename a folder > 200 > application/json",
            "fullPath": path,
            "protocol": "http:",
            "host": "server.test",
            "port": 8080,
            "request": {"method": "PATCH", "body": json.dumps({"name": "New folder"}), "headers": {}},
        }
        self.module.give_each_rename_probe_a_folder(transaction)
        return transaction

    def test_a_probe_on_a_reachable_account_renames_a_folder_of_its_own(self):
        self.answers = [201, 204]
        transaction = self.rename("/api/v1/accounts/1/folders/Work")
        self.assertEqual(transaction["fullPath"], "/api/v1/accounts/1/folders/contract-rename-1")
        self.assertEqual(
            [(r.get_method(), r.full_url) for r in self.requests],
            [
                ("POST", "http://server.test:8080/api/v1/accounts/1/folders"),
                ("DELETE", "http://server.test:8080/api/v1/accounts/1/folders/New%20folder"),
            ],
        )

    def test_a_probe_on_an_account_no_one_has_is_left_to_the_route(self):
        # The older nightly's fuzz probe: a valid ID, 1000, that no account has.
        for status in (404, 403, 400):
            with self.subTest(status=status):
                self.requests.clear()
                self.answers = [status]
                transaction = self.rename("/api/v1/accounts/1000/folders/Work")
                self.assertEqual(transaction["fullPath"], "/api/v1/accounts/1000/folders/Work")
                self.assertEqual(json.loads(transaction["request"]["body"]), {"name": "New folder"})
                self.assertEqual(len(self.requests), 1)

    def test_a_failing_server_still_fails_the_hook(self):
        self.answers = [500]
        with self.assertRaises(urllib.error.HTTPError):
            self.rename("/api/v1/accounts/1/folders/Work")


if __name__ == "__main__":
    unittest.main()
