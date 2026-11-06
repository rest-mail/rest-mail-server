"""Check refusal setup uses fresh postmasters without rewriting other probes."""

import copy
import io
import json
import runpy
import sys
import types
import unittest
from pathlib import Path
from unittest.mock import patch


class PostmasterHooksTests(unittest.TestCase):
    def setUp(self):
        module = types.ModuleType("vertrag_hooks")
        self.registered = []

        def before(operation):
            def register(function):
                self.registered.append(operation)
                return function

            return register

        module.before = before
        with patch.dict(sys.modules, {"vertrag_hooks": module}):
            loaded = runpy.run_path(
                str(Path(__file__).with_name("postmaster_hooks.py"))
            )
        self.prepare = loaded["register_postmaster_hooks"](
            lambda origin: "fixture-mfa-token"
        )
        self.transaction = {
            "protocol": "http:",
            "host": "public.test",
            "port": "8080",
            "fullPath": "/api/v1/admin/mailboxes/1",
            "request": {
                "method": "DELETE",
                "headers": {"Authorization": "Bearer probe"},
            },
            "expected": {
                "statusCode": "409",
                "body": json.dumps({"error": {"code": "postmaster_required"}}),
            },
        }

    def response(self, payload, status=200):
        response = io.BytesIO(json.dumps({"data": payload}).encode())
        response.status = status
        return response

    def test_registers_only_postmaster_mutations(self):
        self.assertEqual(
            set(self.registered),
            {
                "updateMailbox",
                "deleteMailbox",
                "updateAlias",
                "deleteAlias",
                "deleteGrant",
            },
        )

    def test_account_alias_and_grant_targets(self):
        for resource, identifier in (
            ("mailboxes", 12),
            ("aliases", 23),
            ("grants", 34),
        ):
            with self.subTest(resource=resource):
                transaction = copy.deepcopy(self.transaction)
                transaction["fullPath"] = f"/api/v1/admin/{resource}/1"
                domain = {"id": 11, "name": "fresh.test"}
                account = {"id": 12, "address": "postmaster@fresh.test"}
                replies = [self.response(domain, 201), self.response([account])]
                if resource == "aliases":
                    replies.extend(
                        [self.response({"id": 23}, 201), self.response(None, 204)]
                    )
                elif resource == "grants":
                    replies.append(self.response([{"id": 34, "role": "receiver"}]))
                with patch("urllib.request.urlopen", side_effect=replies) as opening:
                    self.prepare(transaction)
                self.assertEqual(
                    transaction["fullPath"], f"/api/v1/admin/{resource}/{identifier}"
                )
                self.assertEqual(transaction["request"], self.transaction["request"])
                self.assertEqual(transaction["expected"], self.transaction["expected"])
                for call in opening.call_args_list:
                    self.assertEqual(
                        call.args[0].get_header("Authorization"),
                        "Bearer fixture-mfa-token",
                    )
                if resource == "aliases":
                    self.assertEqual(
                        opening.call_args_list[-1].args[0].get_method(), "DELETE"
                    )
                    self.assertTrue(
                        opening.call_args_list[-1]
                        .args[0]
                        .full_url.endswith("/mailboxes/12")
                    )

    def test_other_refusals_and_generated_probes_stay_unchanged(self):
        for body in ({"error": {"code": "last_superadmin"}}, {}, "invalid JSON"):
            transaction = copy.deepcopy(self.transaction)
            transaction["expected"]["body"] = body
            before = copy.deepcopy(transaction)
            with patch("urllib.request.urlopen") as opening:
                self.prepare(transaction)
            opening.assert_not_called()
            self.assertEqual(transaction, before)


if __name__ == "__main__":
    unittest.main()
