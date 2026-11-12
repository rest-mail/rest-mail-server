"""Held signing keys and snooze dates must never replace named error examples."""

import json
import os
import re
import subprocess
import sys
import tempfile
import unittest
from datetime import datetime, timezone
from pathlib import Path
from unittest.mock import patch

import probes
import yaml

ROOT = Path(__file__).resolve().parents[2]
COVERAGE_PINS = [
    "private_key", "snoozed_until", "scheduled_at", "expires_at", "permissions",
    "time_zone", "script", "raw_message", "recipients", "date", "filter_name", "cert_pem",
    "key_pem",
]


class ProbeConfig(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.output = Path(directory.name) / "probes.yml"
        self.report = Path(directory.name) / "coverage.xml"
        self.source = ROOT / "vertrag.yml"
        self.before = self.source.read_text()
        self.base = yaml.safe_load(self.before)
        self.assertEqual(probes.main(self.source, self.output, self.report), 0)
        self.config = yaml.safe_load(self.output.read_text())

    def test_no_pin_leaves_the_pins_out(self):
        self.assertEqual(
            self.config["fuzz"]["pin"].keys(), set(COVERAGE_PINS)
        )
        self.assertEqual(
            probes.main(self.source, self.output, self.report, pin=False), 0
        )
        config = yaml.safe_load(self.output.read_text())
        self.assertFalse(config.get("fuzz", {}).get("pin"))

    def test_a_named_pin_is_dropped_and_the_other_stays(self):
        self.assertEqual(
            probes.main(
                self.source, self.output, self.report, unpinned=["snoozed_until"]
            ),
            0,
        )
        config = yaml.safe_load(self.output.read_text())
        self.assertEqual(config["fuzz"]["pin"].keys(), set(COVERAGE_PINS) - {"snoozed_until"})

    def test_dropping_every_pin_leaves_no_pin_section_to_refuse(self):
        self.assertEqual(
            probes.main(
                self.source,
                self.output,
                self.report,
                unpinned=COVERAGE_PINS,
            ),
            0,
        )
        config = yaml.safe_load(self.output.read_text())
        self.assertFalse(config.get("fuzz", {}).get("pin"))

    def fuzz_config(self, **options):
        report = self.report.with_name("fuzz.xml")
        self.discovery = {
            "paths": {
                "/.well-known/restmail": {
                    "get": {
                        "operationId": "restmailDiscover",
                        "parameters": [{"in": "query", "name": "domain", "schema": {"type": "string"}, "examples": {"found": {"value": "restmail.test"}, "missing": {"value": "unhosted.test"}}}],
                    }
                },
                "/another": {
                    "get": {
                        "operationId": "anotherOperation",
                        "parameters": [{"in": "query", "name": "domain", "schema": {"type": "string"}}],
                    }
                },
            }
        }
        self.discovery_source = self.output.with_name("openapi.json")
        self.discovery_source.write_text(json.dumps(self.discovery))
        source = self.output.with_name("source.yml")
        source.write_text(yaml.safe_dump({**self.base, "spec": str(self.discovery_source)}))
        self.assertEqual(probes.main(source, self.output, report, **options), 0)
        return yaml.safe_load(self.output.read_text())

    def test_fuzz_holds_address_at_the_recipient_the_fixtures_make(self):
        plan = json.loads((ROOT / "tests/contract/fixtureplan/defaults.json").read_text())
        self.assertEqual(
            self.fuzz_config()["fuzz"]["pin"]["address"], plan["delivery_recipient"]["address"]
        )

    def test_fuzz_holds_domain_at_the_domain_the_run_hosts(self):
        plan = json.loads(probes.FIXTURE_PLAN.read_text())
        config = self.fuzz_config()
        held = json.loads(Path(config["spec"]).read_text())
        expected = json.loads(json.dumps(self.discovery))
        expected["paths"]["/.well-known/restmail"]["get"]["parameters"][0]["schema"]["enum"] = [plan["domain"]]
        expected["paths"]["/.well-known/restmail"]["get"]["parameters"][0]["examples"]["missing"]["value"] = plan["domain"]
        self.assertEqual(held, expected)
        self.assertEqual(json.loads(self.discovery_source.read_text()), self.discovery)
        self.assertEqual(self.config["spec"], str(ROOT / ".cache/contract/openapi.json"))

    def test_discovery_hold_fails_if_its_parameter_disappears(self):
        config = self.fuzz_config()
        document = json.loads(Path(config["spec"]).read_text())
        document["paths"]["/.well-known/restmail"]["get"]["parameters"] = []
        Path(config["spec"]).write_text(json.dumps(document))
        with self.assertRaisesRegex(ValueError, "no domain query parameter"):
            probes.hold_discovery_domain(config, self.output)

    def test_live_description_uses_the_identical_saved_snapshot_for_the_hold(self):
        config = self.fuzz_config()
        config["spec"] = "http://127.0.0.1:8080/api/docs/openapi.json"
        with patch.dict(os.environ, {"CONTRACT_SPEC": str(self.discovery_source)}):
            probes.hold_discovery_domain(config, self.output)
        self.assertEqual(Path(config["spec"]).name, "fuzz-openapi.json")
        with patch.dict(os.environ, {"CONTRACT_SPEC": ""}):
            with self.assertRaisesRegex(ValueError, "saved description"):
                probes.hold_discovery_domain({"spec": "http://localhost/openapi.json"}, self.output)

    def test_report_configuration_keeps_the_held_description_including_pin_retry(self):
        config = self.fuzz_config()
        subprocess.run(
            [sys.executable, str(ROOT / "tests/contract/reports.py"), str(self.output), str(self.output), str(self.report.with_name("fuzz")), "--narrowed"],
            env={**os.environ, "CONTRACT_RUN_SPEC": "", "CONTRACT_SPEC": ""},
            check=True,
        )
        self.assertEqual(yaml.safe_load(self.output.read_text())["spec"], config["spec"])
        script = (ROOT / "tests/contract/run.sh").read_text()
        self.assertEqual(script.count('CONTRACT_RUN_SPEC= CONTRACT_SPEC= python3 -B tests/contract/reports.py'), 2)

    def test_coverage_does_not_hold_address_so_its_type_probes_are_sent(self):
        self.assertNotIn("address", self.config["fuzz"]["pin"])

    def test_a_narrowed_fuzz_run_can_drop_each_pin_it_does_not_reach(self):
        coverage_pins = COVERAGE_PINS
        config = self.fuzz_config(unpinned=coverage_pins)
        self.assertEqual(config["fuzz"]["pin"].keys(), {"address"})
        config = self.fuzz_config(unpinned=["address"])
        self.assertEqual(config["fuzz"]["pin"].keys(), set(coverage_pins))

    def test_the_fuzz_phase_makes_the_recipient_before_it_pins_it(self):
        script = (ROOT / "tests/contract/run.sh").read_text()
        phase = script[script.index("\nfuzz)\n") :]
        self.assertLess(
            phase.index("chore test:contract-delivery-recipient"),
            phase.index("python3 -B tests/contract/probes.py"),
        )
        chores = (ROOT / "chores.yml").read_text()
        self.assertIn("go run ./tests/contract/fixtures --test-domain=restmail.test --delivery-recipient", chores)

    def test_the_held_snooze_date_is_in_the_future_the_server_requires(self):
        until = datetime.fromisoformat(
            self.config["fuzz"]["pin"]["snoozed_until"].replace("Z", "+00:00")
        )
        self.assertGreater(until, datetime.now(timezone.utc))

    def test_full_examples_keep_their_surface_gate_and_have_no_pins(self):
        self.assertEqual(self.source.read_text(), self.before)
        self.assertEqual(self.base["surface"]["require"], 100)
        self.assertFalse(self.base.get("fuzz", {}).get("pin"))
        self.assertNotIn("surface", self.config)

    def test_generated_run_preserves_checks_and_all_nonrefusal_transactions(self):
        for key in (
            "endpoint",
            "auth",
            "checks",
            "exclude-operation-id",
            "hooks-worker-timeout",
        ):
            self.assertEqual(self.config[key], self.base[key])
        self.assertEqual(
            self.config["exclude-matching"],
            self.base["exclude-matching"] + [probes.REFUSALS],
        )
        pattern = re.compile(probes.REFUSALS)
        for status in [
            "200",
            "201",
            "204",
            "301",
            "400",
            "401",
            "403",
            "404",
            "422",
            "429",
            "500",
        ]:
            with self.subTest(status=status):
                self.assertEqual(
                    bool(
                        pattern.search(
                            "/path > summary > " + status + " > application/json"
                        )
                    ),
                    status.startswith("4"),
                )

    def test_the_password_is_held_only_on_valid_requests_of_two_operations(self):
        valid = self.config["fuzz"]["pin-valid"]
        self.assertEqual(valid.keys(), {"createAccount", "updateMailbox", "createCertificate"})
        self.assertEqual(valid["createAccount"]["password_hash"], "")
        self.assertEqual(
            valid["createAccount"]["password"], valid["updateMailbox"]["password"]
        )
        # A fuzz.pin on the same field would win, and hold the invalid requests too.
        pinned = self.config["fuzz"]["pin"]
        self.assertNotIn("password", pinned)
        self.assertNotIn("password_hash", pinned)

    def test_only_valid_certificate_requests_name_the_isolated_domain(self):
        domain = json.loads(probes.FIXTURE_PLAN.read_text())["certificate_domain"]
        self.assertEqual(
            self.config["fuzz"]["pin-valid"]["createCertificate"],
            {"domain_id": domain["id"]},
        )
        self.assertNotIn("domain_id", self.config["fuzz"]["pin"])

    def test_a_send_is_held_immediate_so_its_success_example_stays_a_201(self):
        self.assertIsNone(self.config["fuzz"]["pin"]["scheduled_at"])

    def test_only_the_key_and_the_two_dates_are_held_and_paths_survive_generated_config_location(
        self,
    ):
        fuzz = dict(self.config["fuzz"])
        fuzz["pin"] = dict(fuzz["pin"])
        self.assertTrue(fuzz["pin"].pop("cert_pem").startswith("-----BEGIN CERTIFICATE-----"))
        self.assertIn("PRIVATE KEY-----", fuzz["pin"].pop("key_pem"))
        self.assertEqual(
            fuzz,
            {
                "pin": {
                    "private_key": probes.DKIM_KEY.read_text(),
                    "snoozed_until": probes.SNOOZE_UNTIL,
                    "scheduled_at": None,
                    "expires_at": probes.EXPIRES_AT,
                    "permissions": probes.KEY_PERMISSIONS,
                    "time_zone": probes.TIME_ZONE,
                    "script": probes.SCRIPT,
                    "raw_message": probes.RAW_MESSAGE,
                    "recipients": probes.RECIPIENTS,
                    "date": probes.DELIVERY_DATE,
                    "filter_name": probes.FILTER_NAME,
                },
                "pin-valid": {
                    **self.base["fuzz"]["pin-valid"],
                    "createCertificate": {
                        "domain_id": json.loads(probes.FIXTURE_PLAN.read_text())["certificate_domain"]["id"],
                    },
                },
            },
        )
        self.assertEqual(
            self.config["spec"], str(ROOT / ".cache/contract/openapi.json")
        )
        self.assertEqual(
            self.config["hookfiles"], str(ROOT / "tests/contract/entrypoint.py")
        )
        self.assertEqual(self.config["reporter"], ["cli", "junit"])
        self.assertEqual(self.config["output"], ["", str(self.report.resolve())])

    def test_live_description_url_survives_probe_configuration(self):
        url = "https://fixture.test/api/docs/openapi.json?revision=one"
        config = dict(self.base, spec=url)
        source = self.output.parent / "live.yml"
        source.write_text(yaml.safe_dump(config))
        probes.main(source, self.output, self.report)
        self.assertEqual(yaml.safe_load(self.output.read_text())["spec"], url)

    def test_workflow_enforces_unpinned_examples_before_coverage_and_uses_coverage_report(
        self,
    ):
        # CI's E2E job runs `chore contract`, the same command a person runs,
        # and it runs tests/contract/run.sh gate, then the secret scan (#627).
        workflow = (ROOT / ".github/workflows/e2e-suite.yml").read_text()
        self.assertIn("run: chore contract", workflow)
        chores = (ROOT / "chores.yml").read_text()
        task = chores[
            chores.index("\n  contract:\n") : chores.index("\n  contract:run:\n")
        ]
        self.assertLess(
            task.index("tests/contract/run.sh gate"),
            task.index("tests/contract/secret_scan.py"),
        )
        script = (ROOT / "tests/contract/run.sh").read_text()
        examples = script.index("--sequence --phases examples")
        # The phases run in the case at the end; the helper above it only defines.
        generated = script.index(
            "python3 -B tests/contract/probes.py", script.index('case "$phase" in')
        )
        coverage = script.index("--sequence --phases coverage")
        repeated = script.index("python3 -B tests/contract/repeat_fixtures.py")
        queue = script.index("chore test:contract-fixtures", repeated)
        self.assertLess(examples, repeated)
        self.assertLess(repeated, queue)
        self.assertLess(queue, generated)
        self.assertLess(examples, generated)
        self.assertLess(generated, coverage)
        # The coverage gate judges the description this server serves (#627).
        self.assertIn(
            'gate.py "$CONTRACT_SPEC" vertrag.yml "$out/coverage.xml" tests/contract/coverage-baseline.txt',
            script,
        )
        # The first command must fail the job immediately on any example or
        # surface failure; only coverage findings may defer to the ratchet.
        command = (
            script[script.rindex("\n", 0, examples) : generated].strip().splitlines()[0]
        )
        self.assertTrue(command.startswith('"$CONTRACT_VERTRAG" run'), command)
        self.assertNotIn("||", command)
        self.assertIn("set -euo pipefail", script)


if __name__ == "__main__":
    unittest.main()
