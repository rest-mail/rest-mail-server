"""CI reuses only images built for the requested mode, coverage and fault tags."""

import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[2]


class PrebuiltImageTests(unittest.TestCase):
    def build(self, service, metadata, cover="false", tags="", tools=True, prebuilt="true", topology=False):
        prefix = service.upper().replace("-", "_")
        task = yaml.safe_load((ROOT / "tasks" / f"{service}.yml").read_text())
        script = task["tasks"]["build"]["cmds"][0]
        script = re.sub(r"{{`(.*?)`}}", lambda match: match[1], script, flags=re.DOTALL)
        values = {prefix + "_MODE": "prod", prefix + "_IMAGE": "fixture/service:run", "API_COVER": cover, "API_BUILD_TAGS": tags}
        script = re.sub(r"{{\.(\w+)}}", lambda match: values[match[1]], script)
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            capture = directory / "calls.jsonl"
            docker = directory / "docker"
            docker.write_text(
                "#!/usr/bin/env python3\n"
                "import json, os, sys\n"
                "with open(os.environ['CAPTURE'], 'a') as output: output.write(json.dumps(sys.argv[1:]) + '\\n')\n"
                "if sys.argv[1:3] == ['image', 'inspect']:\n"
                "    if sys.argv[-1].endswith('-coverage-tools'): sys.exit(int(os.environ['TOOLS_MISSING']))\n"
                "    print(os.environ['IMAGE_METADATA'])\n"
            )
            docker.chmod(0o755)
            subprocess.run(
                ["sh", "-eu"], input=script, text=True, capture_output=True, check=True,
                env={**os.environ, "PATH": str(directory) + os.pathsep + os.environ["PATH"],
                     "CAPTURE": str(capture), "IMAGE_METADATA": metadata, "TOOLS_MISSING": str(int(not tools)),
                     "RESTMAIL_TOPOLOGY_STORAGE": "true" if topology else "false",
                     "RESTMAIL_PREBUILT_IMAGES": prebuilt, "OUTPUT": "/dev/null"},
            )
            return [json.loads(line) for line in capture.read_text().splitlines()]

    def test_matching_prod_images_are_not_built_again(self):
        for service in ("api", "smtp-gateway", "imap-gateway", "pop3-gateway"):
            with self.subTest(service=service):
                calls = self.build(service, "prod|false|" if service == "api" else "prod")
                self.assertFalse(any(call[0] == "build" for call in calls))

    def test_a_dev_image_cannot_be_reused_as_prod(self):
        for service in ("api", "smtp-gateway", "imap-gateway", "pop3-gateway"):
            with self.subTest(service=service):
                calls = self.build(service, "dev|false|" if service == "api" else "dev")
                build = next(call for call in calls if call[0] == "build")
                self.assertEqual(build[build.index("--target") + 1], "prod")
                self.assertIn("rest-mail.mode=prod", build)

    def test_coverage_or_fault_tag_changes_require_a_new_api_image(self):
        for cover, tags in (("true", ""), ("false", "faultinject,fault_health_data_as_list")):
            with self.subTest(cover=cover, tags=tags):
                calls = self.build("api", "prod|false|", cover=cover, tags=tags)
                build = next(call for call in calls if call[0] == "build")
                self.assertIn(f"API_COVER={cover}", build)
                self.assertIn(f"API_BUILD_TAGS={tags}", build)
                self.assertIn(f"rest-mail.api.cover={cover}", build)
                self.assertIn(f"rest-mail.api.tags={tags}", build)

    def test_cover_image_requires_its_matching_toolchain_collector(self):
        calls = self.build("api", "prod|true|", cover="true", tools=False)
        builds = [call for call in calls if call[0] == "build"]
        self.assertEqual(len(builds), 2)
        self.assertEqual(builds[1][builds[1].index("--target") + 1], "deps")
        calls = self.build("api", "prod|true|", cover="true")
        self.assertFalse(any(call[0] == "build" for call in calls))

    def test_normal_production_builds_never_reuse_a_stale_image(self):
        calls = self.build("api", "prod|false|", prebuilt="false")
        self.assertEqual(calls[0][0], "build")

    def test_only_the_topology_overlay_contains_prebuilt_fixture_tools(self):
        calls = self.build("api", "dev|false|", topology=True)
        builds = [call for call in calls if call[0] == "build"]
        self.assertEqual(builds[0][builds[0].index("-t") + 1], "fixture/service:run-runtime")
        self.assertEqual(builds[1][builds[1].index("-t") + 1], "fixture/service:run")
        self.assertIn("projects/api-topology.Dockerfile", builds[1])
        runtime = (ROOT / "projects/api-topology.Dockerfile").read_text().split("FROM ${API_RUNTIME_IMAGE}", 1)[1]
        self.assertNotIn("RUN ", runtime)
        for command in ("restmail-fixtures", "restmail-seed", "restmail-migrate", "restmail-instance"):
            self.assertIn(f"/usr/local/bin/{command}", runtime)
            self.assertNotIn(f"/usr/local/bin/{command}", (ROOT / "Dockerfile").read_text())

    def test_fixture_dispatch_preserves_dev_and_uses_binaries_in_prod(self):
        environment = yaml.safe_load((ROOT / "chores.yml").read_text())["env"]
        for mode in ("dev", "prod"):
            for name, path in (("FIXTURE", "tests/contract/fixtures"), ("API", "cmd/api"), ("SEED", "cmd/seed"), ("MIGRATE", "cmd/migrate"), ("INSTANCE", "cmd/instance")):
                with self.subTest(mode=mode, command=name):
                    result = subprocess.check_output(
                        ["sh", "-c", environment[f"RESTMAIL_{name}_EXEC"]["sh"]],
                        env={**os.environ, "RESTMAIL_SERVICE_MODE": mode}, text=True,
                    ).strip()
                    expected = "restmail-" + ("fixtures" if name == "FIXTURE" else name.lower()) if mode == "prod" else "go run ./" + path
                    self.assertEqual(result, expected)

    def test_dkim_check_streams_the_host_manifest_without_a_source_mount(self):
        task = yaml.safe_load((ROOT / "chores.yml").read_text())["tasks"]["instance:dkim:check"]["cmds"][0]
        for mode in ("dev", "prod"):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as temporary:
                directory = Path(temporary)
                manifest = "domain: fixture.test\n"
                (directory / "manifest.yml").write_text(manifest)
                docker = directory / "docker"
                docker.write_text("#!/usr/bin/env python3\nimport json, sys\nprint(json.dumps([sys.argv[1:], sys.stdin.read()]))\n")
                docker.chmod(0o755)
                command = task
                for variable, value in (("RESTMAIL_PROJECT", "fixture"), ("CONFIG_DIR", str(directory)),
                                        ("ADMIN_ADDRESS", "admin@fixture.test"), ("ADMIN_PASS", "fixture-password")):
                    command = command.replace("{{." + variable + "}}", value)
                executable = "restmail-instance" if mode == "prod" else "go run ./cmd/instance"
                result = subprocess.check_output(["sh", "-c", command], text=True,
                    env={**os.environ, "PATH": str(directory) + os.pathsep + os.environ["PATH"],
                         "RESTMAIL_INSTANCE_EXEC": executable})
                arguments, received = json.loads(result)
                self.assertEqual(received, manifest)
                self.assertEqual(arguments[:3], ["exec", "-i", "fixture-api"])
                self.assertEqual(arguments[3:3 + len(executable.split())], executable.split())
                self.assertEqual(arguments[-1], "/dev/stdin")

    def test_primary_and_additional_dkim_provisioning_use_the_prebuilt_tool(self):
        tasks = yaml.safe_load((ROOT / "chores.yml").read_text())["tasks"]
        for command in tasks["instance:dkim"]["cmds"]:
            self.assertIn("${RESTMAIL_INSTANCE_EXEC:-go run ./cmd/instance} dkim-provision", command)
            self.assertNotIn("\n          go run ./cmd/instance dkim-provision", command)
        self.assertIn("instance:dkim:check", str(tasks["e2e:up"]["cmds"]))


if __name__ == "__main__":
    unittest.main()
