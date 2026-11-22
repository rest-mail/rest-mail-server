"""Floating container/action controls and the repository scan (#2113 AC01)."""

from pathlib import Path
import sys
import unittest


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))

import check_ci_pins


class PinnedCIInputs(unittest.TestCase):
    def test_ac01_external_images_and_actions_are_immutable(self):
        files = {str(path.relative_to(ROOT)): path.read_text() for path in check_ci_pins.source_files(ROOT)}
        self.assertTrue(any(name.startswith(".github/workflows/") for name in files))
        self.assertTrue(any(name.startswith(".github/actions/") for name in files))
        self.assertTrue(any(name.startswith("tasks/") and name.endswith(".sh") for name in files))
        self.assertIn("chores.yml", files)
        self.assertIn(".devcontainer/Dockerfile", files)
        self.assertIn("webmail/Dockerfile", files)
        self.assertIn("scripts/source_volume.py", files)
        self.assertEqual(check_ci_pins.problems(files), [])

    def test_ac01_floating_service_action_task_helper_and_dockerfile_controls_fail(self):
        for filename, source in (
            (".github/workflows/control.yml", "jobs: {test: {services: {db: {image: postgres:16-alpine}}}}"),
            (".github/actions/control/action.yml", "runs: {steps: [{uses: actions/checkout@main}]}"),
            (".github/workflows/control.yml", "jobs: {test: {container: ubuntu}}"),
            (".github/workflows/control.yml", "jobs: {test: {steps: [{uses: 'docker://alpine:3.20'}]}}"),
            ("tasks/control.yml", "vars: {HELPER_IMAGE: '{{.HELPER_IMAGE | default \"alpine:3.20\"}}'}"),
            ("tasks/control.yml", "vars: {HELPER_IMAGE: registry.test/helper, HELPER_TAG: latest}"),
            ("tasks/control.sh", 'docker run --rm --env FOO=bar --volume "$(pwd):/src" alpine:3.20 true'),
            ("tasks/control.sh", 'docker pull registry.test/helper'),
            ("tasks/control.sh", 'docker run --rm $mounts -v "$volume:/to" alpine:3.20 true'),
            ("tasks/control.sh", 'docker run --unknown-argument "$value" alpine:3.20'),
            ("chores.yml", 'tasks:\n  control:\n    cmds:\n      - docker run --rm --label "$LABEL" \\\n          alpine:3.20 true'),
            ("Dockerfile", "FROM --platform=$BUILDPLATFORM alpine:3.20 AS base\nFROM base"),
            ("Dockerfile", "ARG BASE=alpine:3.20\nFROM $BASE"),
            ("Dockerfile", "ARG BASE\nFROM ${BASE}"),
            ("scripts/control.py", 'ALPINE_IMAGE = "alpine:3.20"'),
            ("scripts/control.py", 'env = {"BOOTSTRAP_IMAGE": "golang:1.26.11"}'),
            ("scripts/control.py", 'run(["docker", "run", "--rm", "alpine:3.20", "true"])'),
            ("scripts/control.py", 'container = start("postgres", "postgres:16-alpine", {})'),
        ):
            with self.subTest(filename=filename, source=source):
                self.assertTrue(check_ci_pins.problems({filename: source}))

    def test_ac01_pinned_inputs_stages_and_locally_built_outputs_pass(self):
        digest = "@sha256:" + "a" * 64
        files = {
            "Dockerfile": f"FROM alpine:3.20{digest} AS base\nFROM base AS build\nFROM scratch",
            "control/Dockerfile": f"ARG BASE=alpine:3.20{digest}\nFROM ${{BASE}}",
            "tasks/control.sh": f'docker run --rm --env KEY=value --volume "$(pwd):/src" alpine:3.20{digest} true',
            ".github/actions/control/action.yml": "runs: {steps: [{uses: 'actions/checkout@" + "a" * 40 + "'}, {uses: './.github/actions/local'}]}",
            "tasks/control.yml": 'vars: {SERVICE_IMAGE: "{{.REGISTRY}}/service:{{.TAG}}"}',
            "tasks/build.sh": 'docker build --target prod -t local:smoke .\ndocker run --rm local:smoke',
        }
        self.assertEqual(check_ci_pins.problems(files), [])

    def test_ac01_own_service_images_are_outputs_but_other_rest_mail_images_are_inputs(self):
        # The repository builds its own service images before the topology runs them, so a
        # Dockerfile default naming one is an output. A reference-* image comes from another
        # repository and is downloaded, so it must be pinned.
        own = {"projects/api-topology.Dockerfile": "ARG API_RUNTIME_IMAGE=ghcr.io/rest-mail/api:dev\nFROM ${API_RUNTIME_IMAGE}"}
        self.assertEqual(check_ci_pins.problems(own), [])
        downloaded = {"scripts/plan.py": 'DNSMASQ_IMAGE = "ghcr.io/rest-mail/reference-dnsmasq:0.1.0"'}
        self.assertTrue(check_ci_pins.problems(downloaded))


if __name__ == "__main__":
    unittest.main()
