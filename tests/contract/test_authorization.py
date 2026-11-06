"""The report-only matrix uses the E2E identities and preserves gate settings."""

import base64
import json
import stat
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import authorization
import mfa_admin
import yaml
from test_hooks_two_factor_admin import load_hooks


class AuthorizationConfig(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        # Reports name canonical paths; macOS aliases /var to /private/var.
        self.root = Path(directory.name).resolve()
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
        # The two-factor administrator's unused second factor, spent by the
        # admin principal's one sign-in.
        factor = mock.patch.object(
            mfa_admin, "factor", return_value={"recovery_code": "recovery fixture"}
        )
        self.factor = factor.start()
        self.addCleanup(factor.stop)

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
        self.assertEqual(
            ordinary["auth"]["login"],
            {
                "path": "/api/v1/auth/login",
                "body": {
                    "email": self.fixture["ordinary"]["address"],
                    "password": self.fixture["ordinary"]["password"],
                    "client": "native",
                },
                "secrets": ["password"],
                "public": ["email", "client"],
            },
        )
        self.assertEqual(ordinary["auth"]["header"], authorization.BEARER)
        self.assertEqual(admin["auth"]["header"], authorization.BEARER)
        self.factor.assert_called_once_with()
        self.assertEqual(admin["roles"], authorization.roles(self.spec))
        self.assertEqual(ordinary["roles"], [])
        self.assertEqual(app["roles"], [])
        encoded = app["auth"]["header"].removeprefix("Authorization: Basic ")
        self.assertEqual(
            base64.b64decode(encoded).decode(), "user@example.test:app:fixture"
        )

    def test_admin_signs_in_as_the_two_factor_administrator_with_a_second_factor(self):
        # Creating credentials, linking accounts and changing grants take a
        # recent second factor. A hook that swapped the admin's session for one
        # would leave the cell unsent, so the admin is that administrator, and
        # its factor is a secret the report redacts like the password.
        admin = self.generate()["principals"][1]
        self.assertEqual(
            admin["auth"]["login"],
            {
                "path": "/api/v1/auth/login",
                "body": {
                    "email": mfa_admin.ADDRESS,
                    "password": mfa_admin.PASSWORD,
                    "client": "native",
                    "recovery_code": "recovery fixture",
                },
                "secrets": ["password", "recovery_code"],
                "public": ["email", "client"],
            },
        )

    def test_an_unenrolled_two_factor_administrator_fails_before_replacing_configuration(self):
        self.output.write_text("previous configuration")
        self.factor.side_effect = RuntimeError("not enrolled")
        with self.assertRaises(RuntimeError):
            self.generate()
        self.assertEqual(self.output.read_text(), "previous configuration")

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

    def test_a_refusal_may_be_the_404_that_hides_someone_elses_object(self):
        # Decided on #672: only 404, added to the status each cell requires. The
        # statuses a cell already requires stay out, so 401 and 403 do not start
        # standing in for each other.
        self.assertEqual(self.generate()["authorization"], {"refuse": [404]})

    def test_the_phase_provides_its_own_states_beside_the_runs_own(self):
        self.settings["provides"] = {"state": ["kept"], "fault": ["database"]}
        self.config.write_text(yaml.safe_dump(self.settings))
        config = self.generate()
        self.assertEqual(
            config["provides"],
            {
                "state": [
                    "active_grant",
                    "eligible_grant",
                    "existing_link",
                    "grant_holder",
                    "kept",
                    "owner_key",
                    "recoverable_account",
                ],
                "fault": ["database"],
            },
        )
        # The examples phase's own configuration and unproven list are kept.
        self.assertEqual(yaml.safe_load(self.config.read_text()), self.settings)
        self.assertEqual(
            config["unproven"], ".cache/contract/unproven-authorization.json"
        )

    def test_each_pair_needing_a_provided_state_has_a_hook_that_prepares_it(self):
        # A provided state proves every pair requiring it, so the phase sends
        # each of them (its examples run first), and each names one a hook in
        # hooks.py prepares for every principal: the successes on accounts made
        # for them, the owner refusals on a fresh challenge that reaches the
        # check they document (a challenge needs only the registered key). No
        # state of the caller's own account is provided: its success would end
        # the principal's sessions.
        examples = json.loads(
            (Path(__file__).resolve().parents[2] / "openapi-example.json").read_text()
        )
        needing = sorted(
            (operation, name, pair["response"]["status"])
            for operation, pairs in examples.items()
            for name, pair in pairs.items()
            if (pair.get("requires") or {}).get("state")
            in authorization.PROVIDES["state"]
        )
        prepared_by = {
            ("ownerChallenge", "issued", 200): None,
            ("ownerStatus", "not_status_key", 403): "refuse_a_status_key_that_is_not_listed",
            ("ownerStatus", "status", 200): "sign_the_status_with_the_runs_owner_key",
            ("ownerToken", "minted", 200): "mint_for_an_account_of_its_own",
            ("ownerToken", "no_account", 404): "refuse_a_token_for_an_account_that_does_not_exist",
            ("ownerToken", "not_owner", 403): "refuse_a_token_to_a_key_that_is_not_an_owner",
            ("recoverAccount", "recovered", 200): "recover_an_account_of_its_own",
            ("resetAdmin", "reset", 200): "reset_an_account_of_its_own",
            ("activateGrant", "activated", 201): "a_grant_of_the_callers_own",
            ("activateGrant", "already_active", 409): "a_grant_of_the_callers_own",
            ("activateGrant", "invalid_code", 403): "a_grant_of_the_callers_own",
            ("deactivateGrant", "not_active", 409): "a_grant_of_the_callers_own",
            ("deactivateGrant", "switched_off", 200): "a_grant_of_the_callers_own",
            ("makeGrantEligible", "already_eligible", 409): "a_grant_of_the_callers_own",
            ("makeGrantEligible", "invalid_code", 403): "a_grant_of_the_callers_own",
            ("makeGrantEligible", "now_eligible", 200): "a_grant_of_the_callers_own",
            ("linkAccount", "already_linked", 409): "link_an_account_linked_already",
            ("unlinkAccount", "unlinked", 204): "unlink_a_link_of_its_own",
            ("createGrant", "granted", 201): "grant_to_an_account_of_its_own",
        }
        self.assertEqual(needing, sorted(prepared_by))
        _, _, registered = load_hooks()
        for (operation, name, _), hook in prepared_by.items():
            with self.subTest(operation=operation, name=name):
                if hook is not None:
                    self.assertIn(hook, registered[operation])
        for own in (
            "known_password",
            "enrolled_authenticator",
            "pending_enrollment",
            # Other examples name these for the run's own data or an open stream.
            "two_factor_session",
            "event_stream",
        ):
            self.assertNotIn(own, authorization.PROVIDES["state"])

    def test_own_account_operations_are_left_out_with_a_reason(self):
        self.settings["skip"] = [{"name": "/kept > Kept > 200", "reason": "kept"}]
        self.config.write_text(yaml.safe_dump(self.settings))
        config = self.generate()
        self.assertEqual(
            config["skip"], self.settings["skip"] + authorization.OWN_ACCOUNT
        )
        names = [entry["name"] for entry in authorization.OWN_ACCOUNT]
        self.assertEqual(
            names,
            [
                "/api/v1/messages/draft > Save a new draft > 201 > application/json",
                "/api/v1/messages/scheduled > List the messages waiting to be sent"
                " > 200 > application/json",
            ],
        )
        for entry in authorization.OWN_ACCOUNT:
            self.assertTrue(entry["reason"])

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
