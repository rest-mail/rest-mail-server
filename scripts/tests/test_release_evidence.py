"""AC04 release evidence must cover the exact shipped image bytes."""

import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
spec = importlib.util.spec_from_file_location("release_evidence", SCRIPTS / "release_evidence.py")
evidence = importlib.util.module_from_spec(spec)
spec.loader.exec_module(evidence)

COMMIT = "a" * 40
IMAGES = {name: "sha256:" + f"{index:064x}" for index, name in enumerate(evidence.RELEASE_IMAGES, 1)}
FINDING = {"VulnerabilityID": "CVE-2025-1234", "Severity": "HIGH", "FixedVersion": "2.0"}


class ReleaseEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)
        evidence.write_json(self.directory / "builds.json", {"commit": COMMIT, "images": IMAGES})
        for name, digest in IMAGES.items():
            scan = {"SchemaVersion": 2, "ArtifactType": "container_image", "Metadata": {"ImageID": digest},
                    "Results": [{"Target": "alpine", "Vulnerabilities": []}]}
            sbom = {"bomFormat": "CycloneDX", "components": [{"name": "alpine-baselayout"}],
                    "metadata": {"component": {"properties": [{"name": "aquasecurity:trivy:ImageID", "value": digest}]}}}
            evidence.write_json(self.directory / f"{name}.scan.json", scan)
            evidence.write_json(self.directory / f"{name}.sbom.json", sbom)
            evidence.write_json(self.directory / f"{name}.json", {
                "commit": COMMIT, "image_digest": digest, "scanner": evidence.image_scan.TRIVY_IMAGE,
                "scan": f"{name}.scan.json", "scan_sha256": evidence.sha256(self.directory / f"{name}.scan.json"),
                "sbom": f"{name}.sbom.json", "sbom_sha256": evidence.sha256(self.directory / f"{name}.sbom.json"),
                "evaluation": evidence.evaluate(scan, (evidence.ROOT / ".trivyignore").read_text())})
        contract = self.directory / "contract"
        contract.mkdir()
        (contract / "examples.xml").write_text('<testsuite tests="1" failures="0" errors="0" skipped="0"><testcase name="health"/></testsuite>')
        evidence.write_json(contract / "openapi.json", {"paths": {"/api/health": {"get": {"operationId": "health", "security": []}}}})
        evidence.write_json(self.directory / "fresh-start.json", {
            "commit": COMMIT, "images": IMAGES, "exit_code": 0, "counts": [1, 0, 0], "operations": ["health"],
            "junit_sha256": evidence.sha256(contract / "examples.xml"), "spec_sha256": evidence.sha256(contract / "openapi.json")})

    def change(self, name, mutate):
        path = self.directory / name
        value = json.loads(path.read_text())
        mutate(value)
        evidence.write_json(path, value)

    def refused(self, reason):
        self.assertTrue(any(reason in message for message in evidence.check(self.directory, COMMIT, IMAGES)))

    def test_complete_digest_bound_evaluated_fresh_start_authorizes(self):
        self.assertEqual(evidence.check(self.directory, COMMIT, IMAGES), [])

    def test_report_without_policy_evaluation_is_refused(self):
        self.change("api.json", lambda value: value.pop("evaluation"))
        self.refused("missing or incorrect policy evaluation")

    def test_scan_for_another_built_image_digest_is_refused(self):
        self.change("api.json", lambda value: value.update(image_digest=IMAGES["admin"]))
        self.refused("scan image digest")

    def test_scanner_payload_for_another_image_is_refused(self):
        self.change("api.scan.json", lambda value: value["Metadata"].update(ImageID=IMAGES["admin"]))
        self.change("api.json", lambda value: value.update(scan_sha256=evidence.sha256(self.directory / "api.scan.json")))
        self.refused("scanner inspected another image")

    def test_missing_fresh_start_evidence_is_refused(self):
        with patch.object(Path, "read_text", autospec=True, side_effect=self.read_without_fresh):
            self.refused("missing or malformed evidence")

    def read_without_fresh(self, path, *args, **kwargs):
        if path.name == "fresh-start.json":
            raise FileNotFoundError("fresh-start.json")
        with path.open(*args, **kwargs) as source:
            return source.read()

    def test_fresh_start_on_different_images_is_refused(self):
        self.change("fresh-start.json", lambda value: value["images"].update(api=IMAGES["admin"]))
        self.refused("running image digests")

    def test_missing_image_is_refused(self):
        expected = dict(IMAGES)
        expected.pop("admin")
        self.assertTrue(evidence.check(self.directory, COMMIT, expected))

    def test_changed_report_bytes_are_refused(self):
        self.change("api.scan.json", lambda value: value.update(extra="unrecorded"))
        self.refused("scan or SBOM bytes")

    def test_sbom_for_another_image_is_refused(self):
        self.change("api.sbom.json", lambda value: value["metadata"]["component"]["properties"][0].update(value=IMAGES["admin"]))
        self.refused("SBOM is empty or names another image")

    def test_empty_sbom_is_refused(self):
        self.change("api.sbom.json", lambda value: value.update(components=[]))
        self.refused("SBOM is empty")

    def test_failed_empty_and_skipped_fresh_start_are_refused(self):
        for counts in ([0, 0, 0], [1, 1, 0], [1, 0, 1]):
            with self.subTest(counts=counts):
                self.change("fresh-start.json", lambda value: value.update(counts=counts))
                self.refused("failed, empty, skipped or inconsistent")

    def test_fresh_start_operation_omission_is_refused(self):
        self.change("fresh-start.json", lambda value: value.update(operations=[]))
        self.refused("baseline operations")

    def test_other_commit_is_refused(self):
        self.assertTrue(evidence.check(self.directory, "b" * 40, IMAGES))

    def test_missing_scanner_pin_is_refused(self):
        self.change("api.json", lambda value: value.pop("scanner"))
        self.refused("pinned tool")

    def test_missing_policy_scan_scope_is_refused(self):
        for report in ({}, {"SchemaVersion": 2, "ArtifactType": "container_image", "Results": []}):
            with self.assertRaises(ValueError):
                evidence.evaluate(report, "")

    def test_fixable_high_critical_and_high_secrets_are_refused(self):
        report = json.loads((self.directory / "api.scan.json").read_text())
        for severity in ("HIGH", "CRITICAL"):
            report["Results"][0]["Vulnerabilities"] = [{**FINDING, "Severity": severity}]
            self.assertEqual(evidence.evaluate(report, "")["verdict"], "fail")
        report["Results"][0]["Vulnerabilities"] = []
        report["Results"][0]["Secrets"] = [{"RuleID": "private-key", "Severity": "HIGH"}]
        self.assertEqual(evidence.evaluate(report, "")["verdict"], "fail")

    def test_unfixed_lower_severity_and_disposed_findings_are_retained(self):
        report = json.loads((self.directory / "api.scan.json").read_text())
        report["Results"][0]["Vulnerabilities"] = [FINDING, {**FINDING, "VulnerabilityID": "CVE-2025-2345", "FixedVersion": ""},
                                                         {**FINDING, "VulnerabilityID": "CVE-2025-3456", "Severity": "MEDIUM"}]
        result = evidence.evaluate(report, "# Not reachable: test package unused. Remove with https://github.com/rest-mail/rest-mail-server/issues/2104.\nCVE-2025-1234\n")
        self.assertEqual(result["verdict"], "pass")
        self.assertEqual(len(result["observed"]), 3)
        self.assertEqual(result["ignored"], [FINDING["VulnerabilityID"]])

    def test_undocumented_ignore_is_not_a_disposition(self):
        for ignore in ("CVE-2025-1234\n", "# https://github.com/rest-mail/rest-mail-server/issues/2104\nCVE-2025-1234\n"):
            with self.assertRaises(ValueError):
                evidence.dispositions(ignore)

    def test_collect_refuses_uncommitted_release_sources(self):
        with patch.object(evidence, "run", return_value=type("Result", (), {"stdout": " M Dockerfile\n"})()):
            with self.assertRaisesRegex(ValueError, "clean committed checkout"):
                evidence.collect(None)

    def test_checker_is_offline_and_needs_no_docker_daemon(self):
        arguments = ["check", "--directory", str(self.directory), "--commit", COMMIT]
        for name, digest in IMAGES.items():
            arguments += ["--image", f"{name}={digest}"]
        with patch.object(evidence.image_scan, "daemon_arch", side_effect=AssertionError("offline check called Docker")):
            self.assertEqual(evidence.main(arguments), 0)

    def test_running_image_observation_refuses_stopped_container(self):
        inspected = [{"State": {"Running": False}, "Image": IMAGES["api"]}]
        with patch.object(evidence, "run", return_value=type("Result", (), {"stdout": json.dumps(inspected)})()):
            with self.assertRaisesRegex(ValueError, "not running"):
                evidence.image_ids({"api": "container"})

    def test_failed_fresh_run_invalidates_previous_pass(self):
        with patch.object(evidence, "image_ids", side_effect=ValueError("stopped container")):
            with self.assertRaises(ValueError):
                evidence.fresh_start(SimpleNamespace(directory=self.directory, container=["api=stopped"]))
        self.refused("running image digests")

    def test_junit_claiming_exchanges_without_testcases_is_refused(self):
        path = self.directory / "contract/examples.xml"
        path.write_text('<testsuite tests="8" failures="0" skipped="0"/>')
        self.refused("counts do not match recorded exchanges")

    def test_junit_cannot_hide_a_failed_or_skipped_exchange(self):
        path = self.directory / "contract/examples.xml"
        for result in ("failure", "error", "skipped"):
            with self.subTest(result=result):
                path.write_text(f'<testsuite tests="1" failures="0" skipped="0"><testcase><{result}/></testcase></testsuite>')
                self.refused("counts do not match recorded exchanges")

    def test_stored_pass_cannot_override_policy_findings(self):
        self.change("api.scan.json", lambda value: value["Results"][0].update(Vulnerabilities=[FINDING]))
        self.change("api.json", lambda value: value.update(scan_sha256=evidence.sha256(self.directory / "api.scan.json")))
        self.refused("policy refused CVE-2025-1234")


if __name__ == "__main__":
    unittest.main()
