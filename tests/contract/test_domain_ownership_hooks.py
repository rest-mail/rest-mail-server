"""Domain ownership setup uses the victim's actual, domain-scoped credential."""

import ast
import copy
import importlib.util
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import ownership
import yaml
from test_ownership import basic_header, bearer, load_ownership_hooks


class DomainOwnershipSetup(unittest.TestCase):
    def setUp(self):
        shared = load_ownership_hooks()
        spec = importlib.util.spec_from_file_location(
            "contract_domain_ownership_hooks",
            Path(shared.__file__).with_name("domain_ownership_hooks.py"),
        )
        self.module = importlib.util.module_from_spec(spec)
        with patch.dict(
            sys.modules, {"vertrag_hooks": shared.hooks, "ownership_hooks": shared}
        ):
            spec.loader.exec_module(self.module)
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        fixture = Path(directory.name) / "domain-tenants.json"
        self.fixture = fixture
        self.tenants = {
            "a": {"address": "contract-domain-admin@restmail.test", "domain_id": 11},
            "b": {
                "address": "contract-domain-admin@contract-other.test",
                "domain_id": 29,
            },
        }
        fixture.write_text(json.dumps(self.tenants))
        environment = patch.dict(
            os.environ,
            {"CONTRACT_OWNERSHIP": "1", "CONTRACT_DOMAIN_TENANTS": str(fixture)},
        )
        environment.start()
        self.addCleanup(environment.stop)

    def test_domain_configuration_loads_the_setup_after_the_shared_hooks(self):
        root = self.fixture.parent
        config = root / "config.yml"
        shared = str(root / "shared.py")
        config.write_text(yaml.safe_dump({"hookfiles": shared}))
        output = root / "ownership.yml"
        ownership.domains(
            str(root / "spec.json"),
            str(config),
            str(self.fixture),
            str(output),
            str(root / "ownership.xml"),
        )
        generated = yaml.safe_load(output.read_text())
        self.assertEqual(generated["hookfiles"], [shared, self.module.__file__])
        self.assertEqual(generated["ownership"], {"refuse": [403, 404]})
        self.assertEqual(generated["principals"][0]["roles"], ownership.DOMAIN_ROLES)

    def transaction(self, body, address, operation="Create pipeline", status=201):
        return {
            "name": f"/api/v1/admin/pipelines > {operation} > {status} > application/json",
            "fullPath": "/api/v1/admin/pipelines?unchanged=true",
            "request": {
                "headers": {"Authorization": bearer(address)},
                "body": json.dumps(body),
            },
        }

    def test_resources_are_created_on_each_victims_own_domain(self):
        for tenant in self.tenants.values():
            for name in ("Create pipeline", "Create custom filter"):
                with self.subTest(tenant=tenant, operation=name):
                    transaction = self.transaction(
                        {"domain_id": 1, "name": "example", "active": False},
                        tenant["address"],
                        name,
                    )
                    original = copy.deepcopy(transaction)
                    self.module.create_on_the_tenants_domain(transaction)
                    body = json.loads(transaction["request"]["body"])
                    self.assertEqual(body["domain_id"], tenant["domain_id"])
                    self.assertNotEqual(body["name"], "example")
                    self.assertFalse(body["active"])
                    transaction["request"]["body"] = original["request"]["body"]
                    self.assertEqual(transaction, original)

    def test_setup_hooks_are_registered_for_only_the_three_creates(self):
        tree = ast.parse(Path(self.module.__file__).read_text())
        registered = {
            node.name: [decorator.args[0].value for decorator in node.decorator_list]
            for node in tree.body
            if isinstance(node, ast.FunctionDef)
            and node.name
            in ("create_on_the_tenants_domain", "create_alias_on_the_tenants_domain")
        }
        self.assertEqual(
            registered,
            {
                "create_on_the_tenants_domain": [
                    "createPipeline",
                    "createCustomFilter",
                ],
                "create_alias_on_the_tenants_domain": ["createAlias"],
            },
        )

    def test_pipeline_setup_has_no_name_field(self):
        transaction = self.transaction(
            {"domain_id": 1, "direction": "inbound", "filters": [], "active": False},
            self.tenants["b"]["address"],
        )
        self.module.create_on_the_tenants_domain(transaction)
        self.assertEqual(
            json.loads(transaction["request"]["body"]),
            {"domain_id": 29, "direction": "inbound", "filters": [], "active": False},
        )

    def test_aliases_are_fresh_and_belong_to_the_credential_domain(self):
        for tenant in self.tenants.values():
            transaction = self.transaction(
                {
                    "source_address": "sales@restmail.test",
                    "destination_address": "remote@example.test",
                },
                tenant["address"],
            )
            original = copy.deepcopy(transaction)
            sources = []
            for _ in range(2):
                self.module.create_alias_on_the_tenants_domain(transaction)
                body = json.loads(transaction["request"]["body"])
                self.assertEqual(
                    body["source_address"].split("@")[1],
                    tenant["address"].split("@")[1],
                )
                self.assertEqual(body["destination_address"], "remote@example.test")
                sources.append(body["source_address"])
            self.assertNotEqual(*sources)
            transaction["request"]["body"] = original["request"]["body"]
            self.assertEqual(transaction, original)

    def test_unrelated_credentials_and_phases_are_byte_for_byte_unchanged(self):
        for header in (
            bearer("admin@restmail.test"),
            bearer("ordinary@restmail.test"),
            basic_header(self.tenants["a"]["address"]),
            "",
            "Bearer invalid",
        ):
            transaction = self.transaction(
                {"domain_id": 1, "name": "example"}, self.tenants["a"]["address"]
            )
            transaction["request"]["headers"]["Authorization"] = header
            original = copy.deepcopy(transaction)
            self.module.create_on_the_tenants_domain(transaction)
            self.module.create_alias_on_the_tenants_domain(transaction)
            self.assertEqual(transaction, original)
        for marker in (None, "", "0", "true"):
            with patch.dict(os.environ):
                if marker is None:
                    os.environ.pop("CONTRACT_OWNERSHIP", None)
                else:
                    os.environ["CONTRACT_OWNERSHIP"] = marker
                transaction = self.transaction(
                    {"domain_id": 1, "name": "example"}, self.tenants["a"]["address"]
                )
                original = copy.deepcopy(transaction)
                self.module.create_on_the_tenants_domain(transaction)
                self.module.create_alias_on_the_tenants_domain(transaction)
                self.assertEqual(transaction, original)

    def test_refusal_bodies_and_non_create_requests_are_unchanged(self):
        for body in (
            None,
            [],
            {"domain_id": None},
            {"domain_id": True},
            {"domain_id": "1"},
            {},
            {"domain_id": 1},
        ):
            transaction = self.transaction(
                body, self.tenants["a"]["address"], status=422
            )
            original = copy.deepcopy(transaction)
            self.module.create_on_the_tenants_domain(transaction)
            self.module.create_alias_on_the_tenants_domain(transaction)
            self.assertEqual(transaction, original)
        for body in (
            {},
            {"domain_id": None, "name": "example"},
            {"domain_id": True, "name": "example"},
            {"domain_id": "1", "name": "example"},
            {"domain_id": -1, "name": "example"},
            {"source_address": ""},
            {"source_address": "invalid"},
        ):
            transaction = self.transaction(body, self.tenants["a"]["address"])
            original = copy.deepcopy(transaction)
            self.module.create_on_the_tenants_domain(transaction)
            self.module.create_alias_on_the_tenants_domain(transaction)
            self.assertEqual(transaction, original)


if __name__ == "__main__":
    unittest.main()
