"""The report-only ownership phase sends each account tenant the other's ids."""

import base64
import importlib.util
import json
import os
import re
import stat
import sys
import tempfile
import types
import unittest
from pathlib import Path
from unittest.mock import patch

import ownership
import yaml


ROOT = Path(__file__).resolve().parents[2]


class OwnershipConfig(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.write_fixture()
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

    def write_fixture(self):
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
        self.assertEqual(
            self.generated["only-matching"], [r"^/api/v1/accounts(/\{id\}| > )"]
        )
        selected = re.compile(self.generated["only-matching"][0])
        for name, sent in [
            ("/api/v1/accounts/{id}/folders > List folders > 200", True),
            ("/api/v1/accounts > List accounts > 200", True),
            ("/api/v1/accounts/link > Link an account > 201", False),
            ("/api/v1/admin/domains/{id} > Get a domain > 200", False),
        ]:
            self.assertEqual(bool(selected.search(name)), sent, name)

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


class DomainOwnershipConfig(unittest.TestCase):
    def test_each_domain_administrator_is_sent_the_other_domain(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            spec = root / "openapi.yaml"
            spec.write_text(yaml.safe_dump({"paths": {}}))
            config = root / "vertrag.yml"
            config.write_text(
                yaml.safe_dump({"hookfiles": "./hooks.py", "surface": {"require": 100}})
            )
            tenants = root / "domain-tenants.json"
            tenants.write_text(
                json.dumps(
                    {
                        "a": {
                            "address": "contract-domain-admin@restmail.test",
                            "domain_id": 1,
                        },
                        "b": {
                            "address": "contract-domain-admin@contract-other.test",
                            "domain_id": 6,
                        },
                    }
                )
            )
            out = root / "generated.yml"
            report = root / "ownership-domains.xml"
            self.assertEqual(
                ownership.domains(
                    str(spec), str(config), str(tenants), str(out), str(report)
                ),
                0,
            )
            generated = yaml.safe_load(out.read_text())
            self.assertEqual(stat.S_IMODE(out.stat().st_mode), 0o600)
        self.assertEqual(generated["only-matching"], [ownership.DOMAIN_OPERATIONS])
        selected = re.compile(generated["only-matching"][0])
        for name, sent in [
            ("/api/v1/admin/domains > List all domains > 200", True),
            ("/api/v1/admin/domains/{id}/relay > Get relay > 200", True),
            ("/api/v1/admin/dkim/{id} > Set or replace DKIM key > 200", True),
            ("/api/v1/admin/aliases/{id} > Get an alias > 200", True),
            ("/api/v1/admin/custom-filters > List custom filters > 200", True),
            ("/api/v1/admin/mailboxes/{id} > Get a mailbox > 200", False),
            ("/api/v1/admin/queue/{id} > Get a queue entry > 200", False),
            ("/api/v1/admin/domains-unrelated > x > 200", False),
        ]:
            self.assertEqual(bool(selected.search(name)), sent, name)
        self.assertEqual(
            generated["tenants"],
            {
                "a": {"principals": ["domain-admin-a"], "ids": {"path.id": "1"}},
                "b": {"principals": ["domain-admin-b"], "ids": {"path.id": "6"}},
            },
        )
        principals = {p["name"]: p for p in generated["principals"]}
        self.assertEqual(
            principals["domain-admin-b"]["auth"]["login"]["body"]["email"],
            "contract-domain-admin@contract-other.test",
        )
        self.assertIn("pipelines:write", principals["domain-admin-a"]["roles"])
        self.assertNotIn("queue:manage", principals["domain-admin-a"]["roles"])
        self.assertNotIn("surface", generated)
        self.assertEqual(generated["output"], ["", os.path.abspath(report)])


class MailboxOwnershipConfig(unittest.TestCase):
    def test_each_administrator_is_sent_the_other_tenants_mailbox(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            spec = root / "openapi.yaml"
            spec.write_text(yaml.safe_dump({"paths": {}}))
            config = root / "vertrag.yml"
            config.write_text(yaml.safe_dump({"hookfiles": "./hooks.py"}))
            tenants = root / "domain-tenants.json"
            tenants.write_text(
                json.dumps(
                    {
                        "a": {
                            "address": "a@restmail.test",
                            "domain_id": 1,
                            "mailbox_id": 31,
                        },
                        "b": {
                            "address": "b@contract-other.test",
                            "domain_id": 6,
                            "mailbox_id": 32,
                        },
                    }
                )
            )
            out = root / "generated.yml"
            report = root / "ownership-mailboxes.xml"
            self.assertEqual(
                ownership.mailboxes(
                    str(spec), str(config), str(tenants), str(out), str(report)
                ),
                0,
            )
            generated = yaml.safe_load(out.read_text())
        self.assertEqual(
            {t: v["ids"] for t, v in generated["tenants"].items()},
            {"a": {"path.id": "31"}, "b": {"path.id": "32"}},
        )
        selected = re.compile(generated["only-matching"][0])
        for name, sent in [
            ("/api/v1/admin/mailboxes/{id} > Get a mailbox > 200", True),
            ("/api/v1/admin/mailboxes > List mailboxes > 200", True),
            ("/api/v1/admin/domains/{id} > Get a domain > 200", False),
        ]:
            self.assertEqual(bool(selected.search(name)), sent, name)



# The twelve path-id operations #1163's audit found missing from the matrix,
# with the operations the description's links make their resources by.
MESSAGE_ROWS = (
    "cancelScheduledMessage",
    "deleteMessage",
    "forwardMessage",
    "getAttachment",
    "getMessage",
    "getRawMessage",
    "listAttachments",
    "respondToCalendar",
    "sendDraft",
    "updateDraft",
    "updateMessage",
)


def described_operations():
    """{operationId: (transaction name prefix, links into it)} of the real
    description; a transaction is named "<path> > <summary> > <status>"."""
    description = yaml.safe_load((ROOT / "openapi.yaml").read_text())
    names, sources = {}, {}
    for path, item in description["paths"].items():
        for operation in item.values():
            if not isinstance(operation, dict) or "operationId" not in operation:
                continue
            names[operation["operationId"]] = (
                f"{path} > {operation.get('summary', '')} > 200"
            )
            for response in operation.get("responses", {}).values():
                for link in (response.get("links") or {}).values():
                    target = link.get("operationId")
                    sources.setdefault(target, set()).add(operation["operationId"])
    return names, sources


class MessageOwnershipConfig(OwnershipConfig):
    """The message run: the account tenants, declaring no ids, each sent the
    other's messages, drafts, scheduled sends and attachments (#1163)."""

    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.write_fixture()
        self.output = self.root / "generated.yml"
        self.report = self.root / "ownership-messages.xml"
        self.assertEqual(
            ownership.messages(
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
        # A message id is not an account's: declared, it would be sent to the
        # message routes, refused for naming nothing, and prove nothing. Each
        # cell's message is made by the victim tenant's chain instead.
        self.assertEqual(
            self.generated["tenants"],
            {
                "a": {"principals": ["ordinary", "app-password"]},
                "b": {"principals": ["other", "other-app-password"]},
            },
        )

    def test_another_accounts_message_is_answered_as_absent(self):
        # docs/api-conventions.md rows 11-12 (#672, 2026-11-01): no 403.
        self.assertEqual(self.generated["ownership"], {"refuse": [404]})

    def test_only_account_operations_are_sent(self):
        # The account run's test, for this run: only the message operations.
        selected = re.compile(self.generated["only-matching"][0])
        for name, sent in [
            ("/api/v1/messages/{id} > Get a single message by ID > 200", True),
            ("/api/v1/messages/draft/{id}/send > Send a saved draft > 201", True),
            ("/api/v1/attachments/{id} > Download an attachment > 200", True),
            ("/api/v1/messages/send > Send a message > 201", True),
            ("/api/v1/messages/draft > Save a draft > 201", True),
            ("/api/v1/messages/scheduled > List scheduled messages > 200", False),
            ("/api/v1/messages/scheduled/{id}/release > Release > 200", False),
            ("/api/v1/messages/{id}/seal > Verify the seal > 200", False),
            ("/api/v1/messages/deliver > Deliver > 200", False),
            ("/api/v1/accounts/{id}/folders > List folders > 200", False),
        ]:
            self.assertEqual(bool(selected.search(name)), sent, name)

    def test_the_run_loads_the_sender_hooks_after_its_own(self):
        hookfiles = self.generated["hookfiles"]
        self.assertEqual(len(hookfiles), 2)
        self.assertTrue(hookfiles[0].endswith("tests/contract/hooks.py"))
        self.assertEqual(hookfiles[1], str(ROOT / "tests/contract/ownership_hooks.py"))
        self.assertTrue(Path(hookfiles[1]).is_file())

    def test_the_run_keeps_its_settings_and_reports_apart(self):
        super().test_the_run_keeps_its_settings_and_reports_apart()
        self.assertNotEqual(
            self.generated["output"][1], os.path.abspath(self.root / "ownership.xml")
        )

    def test_every_audited_operation_is_a_row_of_one_run(self):
        names, _ = described_operations()
        messages = re.compile(ownership.MESSAGE_OPERATIONS)
        accounts = re.compile(ownership.ACCOUNT_OPERATIONS)
        for operation in MESSAGE_ROWS:
            self.assertRegex(names[operation], messages, operation)
            self.assertNotRegex(names[operation], accounts, operation)
        # The quarantine release names the account in its path: the account
        # run's row, with that run's declared account id.
        self.assertRegex(names["releaseQuarantine"], accounts)
        self.assertNotRegex(names["releaseQuarantine"], messages)

    def test_every_link_into_a_row_starts_from_an_operation_the_run_sends(self):
        # A chain with an operation the run does not send leaves the cell
        # unsent ("an operation its ids come from is not one this run sends").
        names, sources = described_operations()
        selected = re.compile(ownership.MESSAGE_OPERATIONS)
        for operation in MESSAGE_ROWS:
            for source in sorted(sources.get(operation, ())):
                self.assertRegex(names[source], selected, f"{source} -> {operation}")
        for linked in ("getMessage", "deleteMessage", "updateDraft", "sendDraft"):
            self.assertIn(linked, sources)


def load_ownership_hooks():
    stub = types.ModuleType("vertrag_hooks")
    stub.stash = {}
    stub.before = stub.after = lambda name: lambda fn: fn
    stub.get_json = lambda transaction, which="request": json.loads(
        transaction[which]["body"]
    )
    stub.set_json = lambda transaction, body: transaction["request"].__setitem__(
        "body", json.dumps(body)
    )
    spec = importlib.util.spec_from_file_location(
        "contract_ownership_hooks", ROOT / "tests/contract/ownership_hooks.py"
    )
    module = importlib.util.module_from_spec(spec)
    with patch.dict(sys.modules, {"vertrag_hooks": stub}):
        spec.loader.exec_module(module)
    return module


def bearer(address):
    claims = base64.urlsafe_b64encode(json.dumps({"address": address}).encode())
    return "Bearer header." + claims.decode().rstrip("=") + ".signature"


def basic_header(address, secret="app:secret"):
    return "Basic " + base64.b64encode(f"{address}:{secret}".encode()).decode()


class SenderHooks(unittest.TestCase):
    def setUp(self):
        self.hooks = load_ownership_hooks()
        patcher = patch.dict(os.environ, {"CONTRACT_OWNERSHIP": "1"})
        patcher.start()
        self.addCleanup(patcher.stop)

    def transaction(self, authorization, body):
        return {
            "name": "/api/v1/messages/send > Send a message > 201 > application/json",
            "request": {
                "headers": {"Authorization": authorization},
                "body": json.dumps(body),
            },
        }

    def sent_body(self, authorization, body):
        transaction = self.transaction(authorization, body)
        self.hooks.send_as_the_sender_itself(transaction)
        return json.loads(transaction["request"]["body"])

    EXAMPLE = {
        "from": "admin@restmail.test",
        "to": ["admin@restmail.test", "someone@imap.restmail.test"],
        "cc": ["admin@restmail.test"],
        "subject": "Hello",
    }

    def test_a_session_sends_from_and_to_its_own_address(self):
        self.assertEqual(
            self.sent_body(bearer("other@restmail.test"), self.EXAMPLE),
            {"from": "other@restmail.test", "to": ["other@restmail.test"], "subject": "Hello"},
        )

    def test_an_app_password_sends_from_its_own_address(self):
        body = self.sent_body(basic_header("user@restmail.test"), self.EXAMPLE)
        self.assertEqual(body["from"], "user@restmail.test")
        self.assertEqual(body["to"], ["user@restmail.test"])

    def test_the_administrator_and_other_senders_are_left_alone(self):
        for authorization, body in [
            (bearer("admin@restmail.test"), self.EXAMPLE),
            ("", self.EXAMPLE),
            ("Bearer not-a-token", self.EXAMPLE),
            (bearer("other@restmail.test"), {"from": "someone@other.test", "to": ["x@y.test"]}),
            (bearer("other@restmail.test"), {}),
        ]:
            self.assertEqual(self.sent_body(authorization, body), body, authorization)

    def test_only_an_ownership_run_is_changed(self):
        with patch.dict(os.environ, {"CONTRACT_OWNERSHIP": ""}):
            self.assertEqual(
                self.sent_body(bearer("other@restmail.test"), self.EXAMPLE), self.EXAMPLE
            )


class LinkedDraftSend(unittest.TestCase):
    def test_an_ownership_run_sends_the_draft_its_link_filled(self):
        stub = types.ModuleType("vertrag_hooks")
        stub.stash = {}
        stub.before = stub.after = lambda name: lambda fn: fn
        spec = importlib.util.spec_from_file_location(
            "contract_ownership_draft_hooks", ROOT / "tests/contract/hooks.py"
        )
        module = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {"vertrag_hooks": stub}):
            spec.loader.exec_module(module)
        sent = {
            "name": "/api/v1/messages/draft/{id}/send > Send a saved draft > 201 > application/json",
            "fullPath": "/api/v1/messages/draft/36/send",
            "protocol": "http:",
            "host": "fixture.invalid",
            "port": "1",
            "request": {"headers": {}, "body": ""},
        }
        with patch.dict(os.environ, {"CONTRACT_OWNERSHIP": "1"}):
            module.send_a_draft_of_its_own(sent)
        self.assertEqual(sent["fullPath"], "/api/v1/messages/draft/36/send")


if __name__ == "__main__":
    unittest.main()
