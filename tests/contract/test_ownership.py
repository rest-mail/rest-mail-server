"""The report-only ownership phase sends each account tenant the other's ids."""

import json
import os
import stat
import tempfile
import unittest
from pathlib import Path

import ownership
import yaml


class OwnershipConfig(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.spec = self.root / "openapi.yaml"
        self.spec.write_text(yaml.safe_dump({"paths": {}}))
        self.config = self.root / "vertrag.yml"
        self.config.write_text(
            yaml.safe_dump(
                {
                    "spec": "./old-spec.yaml",
                    "endpoint": "http://fixture.test:8080",
                    "hookfiles": "./tests/contract/hooks.py",
                    "language": "python",
                    "exclude-operation-id": ["resetTestData"],
                    "surface": {"count": "passed", "require": 100},
                    "reporter": ["cli", "junit"],
                    "output": ["", "examples.xml"],
                }
            )
        )
        self.principals = self.root / "principals.json"
        self.principals.write_text(
            json.dumps(
                {
                    "admin": {
                        "address": "admin@example.test",
                        "password": "admin fixture",
                        "id": 1,
                    },
                    "ordinary": {
                        "address": "user@example.test",
                        "password": "user fixture",
                        "id": 4,
                    },
                    "app_password": {
                        "address": "user@example.test",
                        "secret": "app:fixture",
                        "id": 4,
                    },
                    "other": {
                        "address": "other@example.test",
                        "password": "other fixture",
                        "id": 9,
                    },
                    "other_app_password": {
                        "address": "other@example.test",
                        "secret": "other:app",
                        "id": 9,
                    },
                    "owned": {"address": "owned@example.test", "id": 7},
                    "none": {},
                }
            )
        )
        self.output = self.root / "generated.yml"
        self.report = self.root / "ownership.xml"
        self.assertEqual(
            ownership.main(
                str(self.spec),
                str(self.config),
                str(self.principals),
                str(self.output),
                str(self.report),
            ),
            0,
        )
        self.generated = yaml.safe_load(self.output.read_text())

    def test_each_tenant_declares_its_own_account(self):
        self.assertEqual(
            self.generated["tenants"],
            {
                "a": {
                    "principals": ["ordinary", "app-password"],
                    "ids": {"path.id": "4"},
                },
                "b": {
                    "principals": ["other", "other-app-password"],
                    "ids": {"path.id": "9"},
                },
            },
        )
        self.assertEqual(self.generated["ownership"], {"refuse": [403, 404]})

    def test_only_account_operations_are_sent(self):
        # The declared id is an account's: sent to a message or domain route it
        # would be refused for naming nothing, and pass without proving anything.
        self.assertEqual(self.generated["only-matching"], [r"^/api/v1/accounts/\{id\}"])

    def test_principals_hold_what_they_hold_on_their_own_mailbox(self):
        principals = {p["name"]: p for p in self.generated["principals"]}
        self.assertEqual(
            set(principals), {"ordinary", "app-password", "other", "other-app-password"}
        )
        self.assertIn("mailbox:manage", principals["other"]["roles"])
        self.assertNotIn("mailbox:manage", principals["other-app-password"]["roles"])
        self.assertEqual(
            principals["other"]["auth"]["login"]["body"]["email"], "other@example.test"
        )
        self.assertTrue(
            principals["other-app-password"]["auth"]["header"].startswith(
                "Authorization: Basic "
            )
        )

    def test_the_run_keeps_its_settings_and_reports_apart(self):
        self.assertEqual(self.generated["exclude-operation-id"], ["resetTestData"])
        self.assertNotIn("surface", self.generated)
        self.assertEqual(self.generated["output"], ["", os.path.abspath(self.report)])

    def test_the_configuration_is_private(self):
        self.assertEqual(stat.S_IMODE(self.output.stat().st_mode), 0o600)


if __name__ == "__main__":
    unittest.main()
