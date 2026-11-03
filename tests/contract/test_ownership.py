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
from typing import ClassVar
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
    """The message run: the account tenants, each sent the other's messages,
    drafts, scheduled sends, invitations and attachments (#1163)."""

    ATTACHMENTS: ClassVar[dict[str, dict[str, str]]] = {
        "a": {"path.id": "31"},
        "b": {"path.id": "32"},
    }

    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.write_fixture()
        self.output = self.root / "generated.yml"
        self.report = self.root / "ownership-messages.xml"
        with patch.object(
            ownership, "held_attachments", return_value=self.ATTACHMENTS
        ) as held:
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
        self.held = held
        self.generated = yaml.safe_load(self.output.read_text())

    def test_each_tenant_declares_its_own_account(self):
        # Not its account, here: its attachment. A message id is not an account's: each cell's message is made by the
        # victim tenant's chain. Only getAttachment has none, so the one id a
        # tenant declares is an attachment it holds, made against the run's
        # endpoint before the run.
        self.assertEqual(
            self.generated["tenants"],
            {
                "a": {
                    "principals": ["ordinary", "app-password"],
                    "ids": {"path.id": "31"},
                },
                "b": {
                    "principals": ["other", "other-app-password"],
                    "ids": {"path.id": "32"},
                },
            },
        )
        endpoint, fixture = self.held.call_args.args
        self.assertEqual(endpoint, "http://fixture.test:8080")
        self.assertEqual(fixture["ordinary"]["id"], 4)

    def test_a_tenant_without_an_attachment_declares_no_ids(self):
        with patch.object(ownership, "held_attachments", return_value={}):
            ownership.messages(
                str(self.spec),
                str(self.config),
                str(self.principals),
                str(self.output),
                str(self.report),
            )
        self.assertEqual(
            yaml.safe_load(self.output.read_text())["tenants"],
            {
                "a": {"principals": ["ordinary", "app-password"]},
                "b": {"principals": ["other", "other-app-password"]},
            },
        )

    def test_the_run_provides_what_its_hooks_make(self):
        # ownership_hooks.py schedules the victim's send for tomorrow and puts
        # an invitation in its sent message.
        self.assertEqual(
            self.generated["provides"],
            {"state": ["future_time", "calendar_invitation"]},
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
        for linked in (
            "getMessage",
            "deleteMessage",
            "updateDraft",
            "sendDraft",
            "cancelScheduledMessage",
            "respondToCalendar",
        ):
            self.assertIn(linked, sources)
        # The one row the run declares an id for, as no link leads to it.
        self.assertNotIn("getAttachment", sources)


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

    EXAMPLE: ClassVar[dict[str, str | list[str]]] = {
        "from": "admin@restmail.test",
        "to": ["admin@restmail.test", "someone@imap.restmail.test"],
        "cc": ["admin@restmail.test"],
        "subject": "Hello",
    }

    def test_a_session_sends_from_and_to_its_own_address(self):
        self.assertEqual(
            self.sent_body(bearer("other@restmail.test"), self.EXAMPLE),
            {
                "from": "other@restmail.test",
                "to": ["other@restmail.test"],
                "subject": "Hello",
            },
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
            (
                bearer("other@restmail.test"),
                {"from": "someone@other.test", "to": ["x@y.test"]},
            ),
            (bearer("other@restmail.test"), {}),
        ]:
            self.assertEqual(self.sent_body(authorization, body), body, authorization)

    def test_only_an_ownership_run_is_changed(self):
        with patch.dict(os.environ, {"CONTRACT_OWNERSHIP": ""}):
            self.assertEqual(
                self.sent_body(bearer("other@restmail.test"), self.EXAMPLE),
                self.EXAMPLE,
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


class HeldAttachment(unittest.TestCase):
    """Before the message run, each tenant's owner imports a message with an
    attachment into its own account through the API and reads its id back."""

    def setUp(self):
        import http.server
        import threading

        self.requests = []
        test = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def answer(self, body):
                data = json.dumps(body).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def do_POST(self):
                length = int(self.headers.get("Content-Length", 0))
                body = json.loads(self.rfile.read(length))
                test.requests.append(
                    ("POST", self.path, self.headers.get("Authorization"), body)
                )
                if self.path == "/api/v1/auth/login":
                    self.answer({"data": {"access_token": "token-" + body["email"]}})
                else:
                    self.answer({"data": {"id": 77, "imported": True}})

            def do_GET(self):
                test.requests.append(
                    ("GET", self.path, self.headers.get("Authorization"), None)
                )
                self.answer({"data": [{"id": 5}, {"id": 6}]})

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.endpoint = f"http://127.0.0.1:{self.server.server_address[1]}"

    def test_the_owner_imports_into_its_own_account_and_reads_the_attachment(self):
        principal = {
            "address": "user@restmail.test",
            "password": "user fixture",
            "id": 4,
        }
        self.assertEqual(ownership.held_attachment(self.endpoint, principal), "5")
        login, imported, listed = self.requests
        self.assertEqual(
            login[1:],
            (
                "/api/v1/auth/login",
                None,
                {
                    "email": "user@restmail.test",
                    "password": "user fixture",
                    "client": "native",
                },
            ),
        )
        self.assertEqual(imported[1], "/api/v1/accounts/4/messages/import")
        self.assertEqual(imported[2], "Bearer token-user@restmail.test")
        self.assertEqual(imported[3]["folder"], "INBOX")
        raw = base64.b64decode(imported[3]["raw_message"])
        self.assertIn(b"Content-Disposition: attachment; filename=notes.txt", raw)
        self.assertRegex(
            raw.decode(), r"^Message-ID: <contract-ownership-\d+@example.test>"
        )
        self.assertEqual(
            listed[:3],
            (
                "GET",
                "/api/v1/messages/77/attachments",
                "Bearer token-user@restmail.test",
            ),
        )

    def test_each_tenant_declares_its_owners_attachment(self):
        fixture = {
            "ordinary": {"address": "user@restmail.test", "password": "a", "id": 4},
            "other": {"address": "other@restmail.test", "password": "b", "id": 9},
        }
        self.assertEqual(
            ownership.held_attachments(self.endpoint, fixture),
            {"a": {"path.id": "5"}, "b": {"path.id": "5"}},
        )
        imports = [r[1] for r in self.requests if r[1].endswith("/import")]
        self.assertEqual(
            imports,
            [
                "/api/v1/accounts/4/messages/import",
                "/api/v1/accounts/9/messages/import",
            ],
        )

    def test_an_attachment_that_cannot_be_made_declares_none(self):
        self.server.shutdown()
        self.server.server_close()
        fixture = {
            "ordinary": {"address": "user@restmail.test", "password": "a", "id": 4},
            "other": {"address": "other@restmail.test", "password": "b", "id": 9},
        }
        with patch("sys.stderr"):
            self.assertEqual(ownership.held_attachments(self.endpoint, fixture), {})
        with (
            patch.object(ownership, "call", return_value={"data": []}),
            patch("sys.stderr"),
        ):
            self.assertEqual(ownership.held_attachments(self.endpoint, fixture), {})


class LinkedResourceHooks(unittest.TestCase):
    """The victim's send makes what the scheduled-send and invitation rows are
    sent for; the reply is sent from the replier's own address."""

    def setUp(self):
        self.hooks = load_ownership_hooks()
        patcher = patch.dict(os.environ, {"CONTRACT_OWNERSHIP": "1"})
        patcher.start()
        self.addCleanup(patcher.stop)

    def sent(self, hook, authorization, body):
        transaction = {
            "request": {
                "headers": {"Authorization": authorization},
                "body": json.dumps(body),
            }
        }
        hook(transaction)
        return json.loads(transaction["request"]["body"])

    def test_a_scheduled_send_is_for_tomorrow(self):
        from datetime import UTC, datetime, timedelta

        body = self.sent(
            self.hooks.a_send_each_link_can_follow,
            bearer("other@restmail.test"),
            {
                "from": "other@restmail.test",
                "to": ["other@restmail.test"],
                "scheduled_at": "2026-10-27T00:00:00Z",
            },
        )
        when = datetime.strptime(body["scheduled_at"], "%Y-%m-%dT%H:%M:%SZ").replace(
            tzinfo=UTC
        )
        ahead = when - datetime.now(UTC)
        self.assertGreater(ahead, timedelta(hours=22))
        self.assertLess(ahead, timedelta(days=2))
        self.assertNotIn("calendar_event", body)

    def test_any_other_send_invites_the_sender(self):
        body = self.sent(
            self.hooks.a_send_each_link_can_follow,
            basic_header("user@restmail.test"),
            {"from": "user@restmail.test", "to": ["user@restmail.test"]},
        )
        event = body["calendar_event"]
        self.assertEqual(event["method"], "REQUEST")
        self.assertEqual(event["organizer"], {"address": "user@restmail.test"})
        self.assertEqual(event["attendees"][0]["address"], "user@restmail.test")
        self.assertTrue(event["uid"].endswith("@restmail.test"))
        self.assertLess(event["dtstart"], event["dtend"])
        # Every field the CalendarEvent schema requires.
        for field in (
            "method",
            "uid",
            "summary",
            "dtstart",
            "dtend",
            "all_day",
            "organizer",
            "sequence",
        ):
            self.assertIn(field, event)

    def test_the_administrator_and_other_runs_are_left_alone(self):
        example = {
            "from": "admin@restmail.test",
            "to": ["admin@restmail.test"],
            "scheduled_at": "2026-10-27T00:00:00Z",
        }
        self.assertEqual(
            self.sent(
                self.hooks.a_send_each_link_can_follow,
                bearer("admin@restmail.test"),
                example,
            ),
            example,
        )
        with patch.dict(os.environ, {"CONTRACT_OWNERSHIP": ""}):
            self.assertEqual(
                self.sent(
                    self.hooks.a_send_each_link_can_follow,
                    bearer("other@restmail.test"),
                    example,
                ),
                example,
            )
        given = {
            "from": "other@restmail.test",
            "to": ["other@restmail.test"],
            "calendar_event": {"method": "CANCEL"},
        }
        self.assertEqual(
            self.sent(
                self.hooks.a_send_each_link_can_follow,
                bearer("other@restmail.test"),
                given,
            ),
            given,
        )

    def test_a_reply_is_sent_from_the_repliers_own_address(self):
        example = {"from": "admin@restmail.test", "response": "ACCEPTED"}
        self.assertEqual(
            self.sent(
                self.hooks.reply_as_the_sender_itself,
                bearer("other@restmail.test"),
                example,
            ),
            {"from": "other@restmail.test", "response": "ACCEPTED"},
        )
        self.assertEqual(
            self.sent(
                self.hooks.reply_as_the_sender_itself,
                bearer("admin@restmail.test"),
                example,
            ),
            example,
        )
        foreign = {"from": "someone@other.test", "response": "ACCEPTED"}
        self.assertEqual(
            self.sent(
                self.hooks.reply_as_the_sender_itself,
                bearer("other@restmail.test"),
                foreign,
            ),
            foreign,
        )
        with patch.dict(os.environ, {"CONTRACT_OWNERSHIP": ""}):
            self.assertEqual(
                self.sent(
                    self.hooks.reply_as_the_sender_itself,
                    bearer("other@restmail.test"),
                    example,
                ),
                example,
            )
