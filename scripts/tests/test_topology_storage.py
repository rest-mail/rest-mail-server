"""Application scratch and Go state must not consume the runner's shared memory."""

import copy
import importlib.util
import io
import json
import sys
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest.mock import patch

import yaml

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
spec = importlib.util.spec_from_file_location("topology_storage", ROOT / "scripts/topology_storage.py")
storage = importlib.util.module_from_spec(spec)
spec.loader.exec_module(storage)


class TopologyStorageTests(unittest.TestCase):
    def container(self):
        return {
            "Name": "/fixture-api",
            "Config": {"Env": ["GOPATH=/cache/go", "GOMODCACHE=/cache/go/pkg/mod", "GOCACHE=/cache/build"]},
            "HostConfig": {"Tmpfs": {}, "ReadonlyRootfs": True},
            "Mounts": [{"Type": "volume", "Name": "cache", "Destination": "/cache", "RW": True}],
        }

    def test_disk_volumes_and_bind_mounts_pass(self):
        for kind in ("volume", "bind"):
            container = self.container()
            container["Mounts"][0]["Type"] = kind
            self.assertEqual(storage.violations([container], {}), [])

    def test_short_lived_build_tools_can_use_a_disk_backed_image_layer(self):
        container = self.container()
        container["Mounts"] = []
        container["HostConfig"]["ReadonlyRootfs"] = False
        self.assertEqual(storage.violations([container], {}), [])

    def test_every_explicit_tmpfs_is_rejected_even_without_go(self):
        container = self.container()
        container["Config"]["Env"] = []
        container["HostConfig"]["Tmpfs"] = {"/tmp": "rw,size=16m"}
        self.assertIn("fixture-api: tmpfs mount at /tmp", storage.violations([container], {}))

    def test_old_go_paths_on_tmpfs_fail(self):
        container = self.container()
        container["Config"]["Env"] = ["GOPATH=/tmp/go", "GOMODCACHE=/tmp/go/pkg/mod", "GOCACHE=/tmp/go-build-cache"]
        container["HostConfig"]["Tmpfs"] = {"/tmp": "rw,size=1g", "/app/tmp": "rw,size=128m"}
        failures = storage.violations([container], {})
        for variable in storage.GO_PATHS:
            self.assertTrue(any(variable + "=" in failure for failure in failures))
        self.assertEqual(len(failures), 5)

    def test_named_tmpfs_volume_is_also_rejected(self):
        errors = storage.violations([self.container()], {"cache": {"Options": {"type": "tmpfs"}}})
        self.assertEqual(len(errors), 4)

    def test_go_paths_require_writable_storage_and_path_boundaries(self):
        for changes in ({"RW": False}, {"Destination": "/cache-other"}, {"Type": "tmpfs"}):
            container = copy.deepcopy(self.container())
            container["Mounts"][0].update(changes)
            self.assertGreaterEqual(len(storage.violations([container], {})), 3)

    def test_nested_mount_wins_over_its_parent(self):
        container = self.container()
        container["Mounts"].append({"Type": "tmpfs", "Destination": "/cache/build", "RW": True})
        errors = storage.violations([container], {})
        self.assertTrue(any("GOCACHE=" in failure for failure in errors))
        self.assertFalse(any("GOPATH=" in failure for failure in errors))

    def test_service_launches_never_request_tmpfs(self):
        for service in ("api", "smtp-gateway", "imap-gateway", "pop3-gateway", "webmail", "admin", "js-filter"):
            with self.subTest(service=service):
                self.assertNotIn("--tmpfs", (ROOT / "tasks" / f"{service}.yml").read_text())
        self.assertNotIn("--tmpfs", (ROOT / ".github/workflows/ci.yml").read_text())

    def test_ci_and_isolated_topologies_select_prebuilt_services(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/e2e-suite.yml").read_text())
        for job in ("suite", "js-filter-isolation"):
            self.assertEqual(workflow["jobs"][job]["env"]["RESTMAIL_SERVICE_MODE"], "prod")
        from topology_plan import plan
        self.assertEqual(plan("fixture")["environment"]["RESTMAIL_SERVICE_MODE"], "prod")

    def check_live(self, filesystem):
        container = self.container()
        container["Id"] = "fixture-id"
        container["State"] = {"Running": True}
        replies = ["fixture-id\n", json.dumps([container]),
                   json.dumps([{"Name": "cache", "Options": {}}]), filesystem]
        with patch.object(storage, "docker", side_effect=replies) as docker:
            with redirect_stderr(io.StringIO()) as errors, redirect_stdout(io.StringIO()) as output:
                result = storage.main(["fixture"])
        return result, output.getvalue(), errors.getvalue(), docker.call_args_list

    def test_live_check_verifies_the_actual_go_filesystem(self):
        result, output, errors, calls = self.check_live("ext2/ext3\next2/ext3\next2/ext3\n")
        self.assertEqual(result, 0)
        self.assertIn("1 topology containers: no tmpfs", output)
        self.assertEqual(errors, "")
        self.assertEqual(calls[-1].args, ("exec", "fixture-id", "stat", "-f", "-c", "%T",
                                         "/cache/go", "/cache/go/pkg/mod", "/cache/build"))

    def test_a_tmpfs_hidden_behind_a_bind_is_rejected_live(self):
        result, output, errors, _ = self.check_live("tmpfs\ntmpfs\ntmpfs\n")
        self.assertEqual(result, 1)
        self.assertEqual(output, "")
        self.assertEqual(errors.count("is on tmpfs"), 3)

    def test_empty_topology_does_not_pass_vacuously(self):
        with patch.object(storage, "docker", return_value=""), redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit) as error:
                storage.main(["fixture"])
        self.assertEqual(error.exception.code, 2)

    def test_missing_ambiguous_or_unrelated_labels_are_refused(self):
        for arguments in ([], ["fixture", "--label", "rest-mail-loop=fixture"],
                          ["--label", "other=fixture"], ["--label", "rest-mail-loop="]):
            with self.subTest(arguments=arguments), patch.object(storage, "docker") as docker:
                with redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as error:
                    storage.main(arguments)
                self.assertEqual(error.exception.code, 2)
                docker.assert_not_called()


if __name__ == "__main__":
    unittest.main()
