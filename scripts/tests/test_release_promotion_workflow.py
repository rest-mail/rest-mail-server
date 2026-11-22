"""Execute the image promotion workflow's release boundary without a registry write."""

import os
import subprocess
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path

import yaml

import test_release_evidence as fixtures


ROOT = fixtures.evidence.ROOT


class ReleasePromotionWorkflowTests(unittest.TestCase):
    def setUp(self):
        fixture = fixtures.ReleaseEvidenceTests()
        fixture.setUp()
        self.addCleanup(fixture.doCleanups)
        self.fixture = fixture
        self.directory = fixture.directory
        self.digests = self.directory / "digests"
        self.digests.mkdir()
        now = datetime.now(timezone.utc).isoformat()
        registry = {}
        for name, config in fixtures.IMAGES.items():
            manifest = self.directory / f"{name}.manifest.json"
            fixtures.evidence.write_json(manifest, {"config": {"digest": config}})
            index = self.directory / f"{name}.index.json"
            fixtures.evidence.write_json(index, {"manifests": [{
                "digest": "sha256:" + fixtures.evidence.sha256(manifest),
                "platform": {"os": "linux", "architecture": "amd64"},
            }]})
            registry[name] = "sha256:" + fixtures.evidence.sha256(index)
            (self.digests / f"{name}.digest").write_text(registry[name] + "\n")
            fixture.change(f"{name}.json", lambda value: value.update(at=now))
        self.registry = registry
        fixture.change("builds.json", lambda value: value.update(platform="linux/amd64", registry_images=registry))
        fixture.change("fresh-start.json", lambda value: value.update(at=now))
        listeners = ["API internal mTLS", "IMAP implicit TLS", "POP3 implicit TLS", "SMTP submission TLS",
                     "SMTP STARTTLS", "API HTTPS front"]
        phases = ["renewed", "missing", "repaired", "invalid", "recovered"]
        fixtures.evidence.write_json(self.directory / "tls-reload.json", {
            "commit": fixtures.COMMIT, "images": fixtures.IMAGES, "at": now, "exit_code": 0,
            "observations": [{"listener": listener, "phase": phase, "hostname": "api" if listener == "API internal mTLS" else "mail.release.test",
                              "leaf_sha256": "a" * 64, "original_leaf_sha256": "b" * 64} for phase in phases for listener in listeners],
        })
        self.promotions = self.directory / "promotions.log"
        docker = self.directory / "docker"
        docker.write_text('#!/bin/bash\n[[ "$*" == "buildx imagetools create --tag "* ]] || exit 99\nprintf "%s\\n" "$*" >> "$PROMOTIONS"\n')
        docker.chmod(0o755)
        workflow = yaml.safe_load((ROOT / ".github/workflows/docker.yml").read_text())
        self.jobs = workflow["jobs"]
        job = workflow["jobs"].get("promote-release-images")
        self.assertIsNotNone(job, "release boundary missing: Build and push publishes SemVer before evidence")
        self.boundary = next(step["run"] for step in job["steps"] if step.get("name") == "Gate and promote release images")

    def run_boundary(self, command=None):
        env = {**os.environ, "PATH": f"{self.directory}:{os.environ['PATH']}", "PROMOTIONS": str(self.promotions),
               "RELEASE_DIGESTS": str(self.digests), "RELEASE_EVIDENCE": str(self.directory),
               "GITHUB_REF_NAME": "v1.2.3", "GITHUB_SHA": fixtures.COMMIT, "IMAGE_PREFIX": "ghcr.io/rest-mail"}
        return subprocess.run(["bash", "-e", "-o", "pipefail", "-c", command or self.boundary], cwd=ROOT,
                              env=env, capture_output=True, text=True)

    def assert_refused(self, reason):
        result = self.run_boundary()
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn(reason, result.stderr)
        self.assertFalse(self.promotions.exists(), "registry write preceded release authorization")

    def test_complete_fresh_matching_receipt_promotes_exact_digests(self):
        result = self.run_boundary()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("release-gate authorized", result.stdout)
        self.assertCountEqual(self.promotions.read_text().splitlines(), [
            f"buildx imagetools create --tag ghcr.io/rest-mail/{name}:1.2.3 ghcr.io/rest-mail/{name}@{digest}"
            for name, digest in self.registry.items()
        ])

    def test_git_tags_build_by_digest_and_promotion_requires_both_jobs(self):
        build = next(step for step in self.jobs["build-and-push"]["steps"] if step.get("id") == "build")["with"]
        for field in ("push", "tags"):
            self.assertIn("!startsWith(github.ref, 'refs/tags/v')", build[field])
        self.assertIn("push-by-digest=true", build["outputs"])
        self.assertEqual(self.jobs["promote-release-images"]["needs"], ["build-and-push", "release-evidence"])

    def test_missing_artifact_refuses_and_gate_removal_would_promote(self):
        (self.directory / "api.sbom.json").unlink()
        self.assert_refused("missing or malformed evidence")
        self.assertEqual(self.boundary.count("python3 scripts/release_gate.py"), 1)
        ungated = self.boundary.replace("python3 scripts/release_gate.py", "true", 1)
        result = self.run_boundary(ungated)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(len(self.promotions.read_text().splitlines()), 7)
        print("red-before (gate removed, SBOM missing): promotion wrote 7 version tags")

    def test_stale_receipt_refuses_before_any_registry_write(self):
        age = (datetime.now(timezone.utc) - timedelta(hours=27)).isoformat()
        self.fixture.change("api.json", lambda value: value.update(at=age))
        self.assert_refused("stale: api.json")

    def test_failed_receipt_refuses_before_any_registry_write(self):
        self.fixture.change("fresh-start.json", lambda value: value.update(exit_code=1))
        self.assert_refused("fresh-start evidence is failed")

    def test_wrong_build_digest_refuses_before_any_registry_write(self):
        (self.digests / "api.digest").write_text(self.registry["admin"] + "\n")
        self.assert_refused("wrong digest:")

    def test_missing_tls_phase_refuses_before_any_registry_write(self):
        self.fixture.change("tls-reload.json", lambda value: value["observations"].pop())
        self.assert_refused("TLS reload did not observe every listener and phase")

    def test_tampered_registry_manifest_refuses_before_any_registry_write(self):
        self.fixture.change("api.manifest.json", lambda value: value["config"].update(digest=fixtures.IMAGES["smtp-gateway"]))
        self.assert_refused("manifest bytes differ from the build digest chain")
