"""The report-only matrix uses the E2E identities and preserves gate settings."""

import base64
import json
import stat
import tempfile
import unittest
from pathlib import Path

import authorization
import yaml


class AuthorizationConfig(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.spec = self.root / "openapi.yaml"
        self.spec.write_text(
            yaml.safe_dump(
                {
                    "security": [{"BearerAuth": ["global:read"]}],
                    "paths": {
                        "/inherited": {"get": {"operationId": "inherited"}},
                        "/public": {"get": {"security": []}},
                        "/custom": {
                            "post": {
                                "security": [
                                    {"BearerAuth": ["mail:write", "mail:read"]},
                                    {"OtherAuth": ["mail:read"]},
                                ]
                            }
                        },
                        "/head": {
                            "head": {"security": [{"BearerAuth": ["head:read"]}]}
                        },
                        "/options": {
                            "options": {"security": [{"BearerAuth": ["options:read"]}]}
                        },
                        "/trace": {
                            "trace": {"security": [{"BearerAuth": ["trace:read"]}]}
                        },
                    },
                }
            )
        )
        self.config = self.root / "vertrag.yml"
        self.settings = {
            "spec": "./old-spec.yaml",
            "endpoint": "http://fixture.test:8080",
            "hookfiles": "./tests/contract/hooks.py",
            "language": "python",
            "auth": {"header": "Authorization: Bearer admin-default"},
            "checks": {"ignored-auth": True},
            "exclude-operation-id": ["resetTestData"],
            "exclude-matching": ["> 403( >|$)"],
            "surface": {"count": "passed", "require": 100},
            "reporter": ["cli", "junit"],
            "output": ["", "examples.xml"],
        }
        self.config.write_text(yaml.safe_dump(self.settings))
        self.fixture = {
            "admin": {"address": "admin@example.test", "password": "admin fixture"},
            "ordinary": {"address": "user@example.test", "password": "user fixture"},
            "app_password": {"address": "user@example.test", "secret": "app:fixture"},
            "owned": {"address": "owned@example.test", "id": 7},
            "none": {},
        }
        self.principals = self.root / "principals.json"
        self.principals.write_text(json.dumps(self.fixture))
        self.output = self.root / "generated.yml"
        self.report = self.root / "authorization.xml"

    def generate(self):
        self.assertEqual(
            authorization.main(
                str(self.spec),
                str(self.config),
                str(self.principals),
                str(self.output),
                str(self.report),
            ),
            0,
        )
        return yaml.safe_load(self.output.read_text())

    def test_roles_include_inherited_and_alternative_requirements_without_duplicates(
        self,
    ):
        self.assertEqual(
            authorization.roles(self.spec),
            [
                "global:read",
                "head:read",
                "mail:read",
                "mail:write",
                "options:read",
                "trace:read",
            ],
        )

    def test_each_identity_has_its_own_credential_and_explicit_role_expectations(self):
        config = self.generate()
        none, admin, ordinary, app = config["principals"]
        self.assertEqual(none, {"name": "none"})
        self.assertEqual(
            [p["name"] for p in config["principals"]],
            ["none", "admin", "ordinary", "app-password"],
        )
        for principal, fixture_name in ((admin, "admin"), (ordinary, "ordinary")):
            self.assertEqual(
                principal["auth"]["login"],
                {
                    "path": "/api/v1/auth/login",
                    "body": {
                        "email": self.fixture[fixture_name]["address"],
                        "password": self.fixture[fixture_name]["password"],
                        "client": "native",
                    },
                    "secrets": ["password"],
                    "public": ["email", "client"],
                },
            )
            self.assertEqual(principal["auth"]["header"], authorization.BEARER)
        self.assertEqual(admin["roles"], authorization.roles(self.spec))
        self.assertEqual(ordinary["roles"], [])
        self.assertEqual(app["roles"], [])
        encoded = app["auth"]["header"].removeprefix("Authorization: Basic ")
        self.assertEqual(
            base64.b64decode(encoded).decode(), "user@example.test:app:fixture"
        )

    def test_live_url_uses_saved_bytes_for_role_inventory(self):
        url = "https://fixture.test/api/docs/openapi.json?revision=one"
        authorization.main(
            str(self.spec),
            str(self.config),
            str(self.principals),
            str(self.output),
            str(self.report),
            run_spec=url,
        )
        config = yaml.safe_load(self.output.read_text())
        self.assertEqual(config["spec"], url)
        self.assertEqual(
            config["principals"][1]["roles"], authorization.roles(self.spec)
        )

    def test_report_does_not_change_examples_gate_or_exclusions(self):
        config = self.generate()
        self.assertNotIn("surface", config)
        for key in (
            "endpoint",
            "auth",
            "checks",
            "exclude-operation-id",
            "exclude-matching",
        ):
            self.assertEqual(config[key], self.settings[key])
        self.assertEqual(config["spec"], str(self.spec))
        self.assertEqual(
            Path(config["hookfiles"]), self.root / "tests/contract/hooks.py"
        )
        self.assertEqual(config["reporter"], ["cli", "junit"])
        self.assertEqual(config["output"], ["", str(self.report)])
        self.assertEqual(yaml.safe_load(self.config.read_text()), self.settings)

    def test_generated_credentials_are_private_even_if_output_existed(self):
        for existing in (False, True):
            with self.subTest(existing=existing):
                if existing:
                    self.output.chmod(0o644)
                self.generate()
                self.assertEqual(stat.S_IMODE(self.output.stat().st_mode), 0o600)

    def test_missing_fixture_fails_before_replacing_configuration(self):
        self.output.write_text("previous configuration")
        del self.fixture["app_password"]
        self.principals.write_text(json.dumps(self.fixture))
        with self.assertRaises(KeyError):
            self.generate()
        self.assertEqual(self.output.read_text(), "previous configuration")


if __name__ == "__main__":
    unittest.main()
