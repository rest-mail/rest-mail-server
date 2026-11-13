"""Use the private pool only for jobs its rootless runners can execute."""

from pathlib import Path
import ast
import re
import tempfile
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[2]
RUNNER_POOL = "antimatter-vps-linux-x64"
DOCKER_COMMAND = re.compile(r"(?m)(?:^\s*(?:(?:if|then|exec|!)\s+)?|[;&|]\s*|\$\()(?:sudo\s+)?(?:docker(?:-compose)?|podman|kind|minikube|k3d)(?:\s|$)")
CONTAINER_TASK = re.compile(r"\bchore\s+(?:e2e:(?:up|down)|testbed:up|test:load)\b")
SCRIPT_PATH = re.compile(r"(?:\./)?(?:scripts|\.github/scripts|helm/restmail/tests)/[\w./-]+")


def requires_docker(job, root=ROOT, seen=None):
    seen = set() if seen is None else seen
    if job.get("services") or job.get("container"):
        return True
    for step in job.get("steps", []):
        action = step.get("uses", "")
        if action.startswith(("docker/", "docker://")) or any(
            name in action for name in ("kind-action@", "setup-kind@", "setup-minikube@", "setup-k3d@")
        ):
            return True
        if action.startswith("./") and action not in seen:
            seen.add(action)
            for filename in ("action.yml", "action.yaml"):
                path = root / action / filename
                if path.is_file():
                    runs = yaml.safe_load(path.read_text())["runs"]
                    if runs.get("using") == "docker" or requires_docker(runs, root, seen):
                        return True
        command = step.get("run", "")
        if DOCKER_COMMAND.search(command) or CONTAINER_TASK.search(command):
            return True
        for filename in SCRIPT_PATH.findall(command):
            path = root / filename
            if path.is_file() and filename not in seen:
                seen.add(filename)
                source = path.read_text()
                if path.suffix == ".py":
                    commands = {"docker", "docker-compose", "podman", "kind", "minikube", "k3d"}
                    for call in ast.walk(ast.parse(source)):
                        if not isinstance(call, ast.Call) or not call.args:
                            continue
                        arguments = call.args[0]
                        if isinstance(arguments, (ast.List, ast.Tuple)) and arguments.elts:
                            first = arguments.elts[0]
                            if isinstance(first, ast.Constant) and first.value in commands:
                                return True
                elif requires_docker({"steps": [{"run": source}]}, root, seen):
                    return True
    return False


def requires_github_cli(job, root=ROOT, seen=None):
    seen = set() if seen is None else seen
    for step in job.get("steps", []):
        action = step.get("uses", "")
        if action.startswith("./") and action not in seen:
            seen.add(action)
            for filename in ("action.yml", "action.yaml"):
                path = root / action / filename
                if path.is_file() and requires_github_cli(yaml.safe_load(path.read_text())["runs"], root, seen):
                    return True
        command = step.get("run", "")
        if re.search(r"(?m)(?:^\s*(?:(?:if|then|exec|!)\s+)?|[;&|]\s*|[<$]\()gh(?:\s|$)", command):
            return True
        for filename in SCRIPT_PATH.findall(command):
            path = root / filename
            if not path.is_file() or filename in seen:
                continue
            seen.add(filename)
            source = path.read_text()
            if path.suffix == ".py":
                for call in ast.walk(ast.parse(source)):
                    if isinstance(call, ast.Call) and call.args:
                        arguments = call.args[0]
                        if isinstance(arguments, (ast.List, ast.Tuple)) and arguments.elts:
                            first = arguments.elts[0]
                            if isinstance(first, ast.Constant) and first.value == "gh":
                                return True
            elif requires_github_cli({"steps": [{"run": source}]}, root, seen):
                return True
    return False


def requires_hosted_runner(job):
    return requires_docker(job) or requires_github_cli(job) or any(
        step.get("uses", "").startswith("actions/setup-node@")
        and "node-version-file" in step.get("with", {})
        for step in job.get("steps", [])
    )


class PrivateRunnerRouting(unittest.TestCase):
    def test_every_executable_job_uses_a_capable_runner(self):
        executable_jobs = 0
        for workflow in sorted((ROOT / ".github/workflows").glob("*.yml")):
            document = yaml.safe_load(workflow.read_text())
            for name, job in document["jobs"].items():
                with self.subTest(workflow=workflow.name, job=name):
                    if "uses" in job:
                        self.assertNotIn("runs-on", job)
                    else:
                        executable_jobs += 1
                        expected = "ubuntu-latest" if requires_hosted_runner(job) else RUNNER_POOL
                        self.assertEqual(job.get("runs-on"), expected)
        self.assertGreater(executable_jobs, 0)

    def test_docker_requirements_cannot_use_the_private_pool(self):
        jobs = [
            {"services": {"postgres": {"image": "postgres:16-alpine"}}},
            {"container": "ubuntu:24.04"},
            {"steps": [{"uses": "docker/setup-buildx-action@pinned"}]},
            {"steps": [{"uses": "docker/build-push-action@pinned"}]},
            {"steps": [{"run": "python3 scripts/garage-attachment-test.py"}]},
            {"steps": [{"run": "bash helm/restmail/tests/dane-resolver-test.sh"}]},
            {"steps": [{"run": "chore e2e:up"}]},
        ]
        jobs.extend({"steps": [{"run": command}]} for command in (
            "docker pull postgres:16-alpine", "docker compose up", "docker-compose up",
            "kind create cluster", "podman run image", "k3d cluster create",
        ))
        for job in jobs:
            with self.subTest(job=job):
                self.assertTrue(requires_docker(job))

    def test_github_cli_consumers_require_a_runner_that_provides_it(self):
        commands = (
            "gh pr view 1", "labels=$(gh pr view 1)",
            "while read -r label; do true; done < <(gh pr view 1)",
            "python3 scripts/contract_compat.py base.json head.json",
        )
        for command in commands:
            with self.subTest(command=command):
                job = {"steps": [{"run": command}]}
                self.assertTrue(requires_github_cli(job))
                self.assertTrue(requires_hosted_runner(job))
        self.assertFalse(requires_github_cli({"steps": [{"run": "echo github"}]}))

    def test_package_selected_node_requires_the_hosted_runtime(self):
        job = {"steps": [{"uses": "actions/setup-node@pinned", "with": {"node-version-file": "webmail/package.json"}}]}
        self.assertFalse(requires_docker(job))
        self.assertTrue(requires_hosted_runner(job))
        self.assertFalse(requires_hosted_runner({"steps": [{"run": "go build ./..."}]}))

    def test_local_actions_carry_their_docker_requirement_to_the_job(self):
        for runs in (
            {"using": "docker", "image": "Dockerfile"},
            {"using": "composite", "steps": [{"run": "if docker info; then echo ready; fi"}]},
            {"using": "composite", "steps": [{"uses": "docker/setup-buildx-action@pinned"}]},
        ):
            with self.subTest(runs=runs), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                action = root / ".github/actions/fixture/action.yml"
                action.parent.mkdir(parents=True)
                action.write_text(yaml.safe_dump({"runs": runs}))
                job = {"steps": [{"uses": "./.github/actions/fixture"}]}
                self.assertTrue(requires_docker(job, root))

    def test_node_versions_come_from_the_package(self):
        for filename in ("ci.yml", "email-explained.yml", "scheduled.yml"):
            document = yaml.safe_load((ROOT / ".github/workflows" / filename).read_text())
            for name, job in document["jobs"].items():
                for step in job.get("steps", []):
                    if step.get("uses", "").startswith("actions/setup-node@") and name != "js-filter":
                        with self.subTest(workflow=filename, job=name):
                            self.assertIn("node-version-file", step["with"])
                            self.assertNotIn("node-version", step["with"])

    def test_python_consumers_bootstrap_before_the_first_use(self):
        consumers = 0
        for workflow in sorted((ROOT / ".github/workflows").glob("*.yml")):
            document = yaml.safe_load(workflow.read_text())
            for name, job in document["jobs"].items():
                steps = job.get("steps", [])
                for index, step in enumerate(steps):
                    command = step.get("run", "")
                    if not (
                        "python3" in command
                        or "scripts/ci/install-goreleaser" in command
                        or "scripts/ci/install-golangci-lint" in command
                        or step.get("uses") == "./.github/actions/cache-pinned-tool"
                    ):
                        continue
                    consumers += 1
                    with self.subTest(workflow=workflow.name, job=name):
                        bootstrap = [
                            item for item in steps[:index]
                            if item.get("uses") == "./.github/actions/setup-python"
                        ]
                        self.assertEqual(len(bootstrap), 1)
                        self.assertNotIn("if", bootstrap[0])
                    break
        self.assertGreater(consumers, 0)

    def test_python_bootstrap_provides_pip_and_the_local_gate_dependencies(self):
        action = ROOT / ".github/actions/setup-python/action.yml"
        self.assertTrue(action.is_file(), "Python consumers need an explicit bootstrap")
        steps = yaml.safe_load(action.read_text())["runs"]["steps"]
        self.assertEqual(
            steps[0]["uses"],
            "actions/setup-python@ece7cb06caefa5fff74198d8649806c4678c61a1",
        )
        self.assertEqual(steps[0]["with"]["python-version"], "3.13")
        self.assertEqual(
            steps[1]["run"],
            "python3 -m pip install --disable-pip-version-check PyYAML==6.0.3 idna==3.11 'jsonschema[format]' requests",
        )
        self.assertEqual(steps[1]["shell"], "bash")

    def test_pnpm_is_enabled_only_after_node_is_installed(self):
        consumers = 0
        for workflow in sorted((ROOT / ".github/workflows").glob("*.yml")):
            document = yaml.safe_load(workflow.read_text())
            for name, job in document["jobs"].items():
                steps = job.get("steps", [])
                for index, step in enumerate(steps):
                    if "corepack enable pnpm" not in step.get("run", ""):
                        continue
                    consumers += 1
                    with self.subTest(workflow=workflow.name, job=name):
                        setup = [
                            item for item in steps[:index]
                            if item.get("uses", "").startswith("actions/setup-node@")
                        ]
                        self.assertEqual(len(setup), 1)
                        self.assertIn("node-version-file", setup[0]["with"])
                        self.assertIn("npm install --global corepack@0.34.7", step["run"])
        self.assertEqual(consumers, 3)
