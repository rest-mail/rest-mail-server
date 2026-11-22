"""Verify devstack isolation, secret reuse, and recorded-resource ownership."""

import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

import yaml


ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("desktop_devstack", ROOT / "desktop/scripts/devstack.py")
DEVSTACK = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(DEVSTACK)


class DevstackTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.state = Path(self.directory.name)
        (self.state / "secrets.env").write_text("export JWT_SECRET='existing-jwt'\nexport MASTER_KEY=existing-master\n")
        self.stack = DEVSTACK.Stack(self.state, self.state, 18081)

    def test_reuses_persisted_keys_and_the_existing_nonstandard_database_port(self):
        environment = DEVSTACK.environment(self.state)
        self.assertEqual(environment["JWT_SECRET"], "existing-jwt")
        self.assertEqual(environment["MASTER_KEY"], "existing-master")
        self.assertEqual(environment["DB_PORT"], "55480")
        self.assertNotIn("RESTMAIL_BOOTSTRAP_ADMIN_PASSWORD", environment)
        self.assertEqual(environment["INTERNAL_MTLS_ENABLED"], "true")
        self.assertIn("smtp-delivery", environment["INTERNAL_MTLS_CLIENT_CERT"])
        self.assertIn("smtp-submission", environment["INTERNAL_MTLS_SUBMISSION_CERT"])
        self.assertEqual(environment["SMTP_PORT_SUBMISSION_TLS"], "0")
        self.assertEqual(environment["SMTP_METRICS_PORT"], "0")

    def test_missing_keys_fail_without_printing_secret_values(self):
        (self.state / "secrets.env").write_text("JWT_SECRET=never-print-me\n")
        with self.assertRaisesRegex(RuntimeError, "must contain JWT_SECRET and MASTER_KEY") as caught:
            DEVSTACK.saved_secrets(self.state / "secrets.env")
        self.assertNotIn("never-print-me", str(caught.exception))

    def test_secret_file_is_parsed_not_executed(self):
        (self.state / "secrets.env").write_text("JWT_SECRET='$(touch /tmp/not-executed)'\nMASTER_KEY=literal\n")
        self.assertEqual(DEVSTACK.saved_secrets(self.state / "secrets.env")["JWT_SECRET"], "$(touch /tmp/not-executed)")

    def test_totp_is_deterministic_for_the_current_time_step(self):
        self.assertEqual(DEVSTACK.totp("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", 59), "287082")
        self.assertEqual(DEVSTACK.totp("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", 30), "287082")

    def test_mounts_are_read_only_and_name_only_selected_files(self):
        self.assertEqual(DEVSTACK.mounts(Path("/private/state"), "api", "certs/internal-ca.crt"), ["--mount", "type=bind,source=/private/state/api,target=/api,readonly", "--mount", "type=bind,source=/private/state/certs/internal-ca.crt,target=/certs/internal-ca.crt,readonly"])

    def test_fixture_provisioning_changes_only_the_inbound_greylist_filter(self):
        (self.state / "admin.pw").write_text("test-only-password")
        (self.state / "totp.secret").write_text("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ")
        filters = [{"name": "size_check", "enabled": True}, {"name": "greylist", "enabled": True}, {"name": "dkim_verify", "enabled": True}]
        pipelines = [{"id": 2, "domain_id": 1, "direction": "inbound", "filters": filters}, {"id": 3, "domain_id": 9, "direction": "inbound", "filters": [{"name": "greylist", "enabled": True}]}]
        responses = [{"data": {"access_token": "never-log-this"}}, {"data": [{"id": 1, "name": "desktop.test", "active": True}]}, {"data": pipelines}, {}]
        with mock.patch.object(DEVSTACK, "request", side_effect=responses) as request:
            self.stack.activate_domain("http://127.0.0.1:18081")
        self.assertEqual(request.call_args_list[-1], mock.call("http://127.0.0.1:18081/api/v1/admin/pipelines/2", "PATCH", {"filters": [{"name": "size_check", "enabled": True}, {"name": "greylist", "enabled": False}, {"name": "dkim_verify", "enabled": True}]}, "never-log-this"))
        self.assertTrue(pipelines[1]["filters"][0]["enabled"])

    def test_up_publishes_only_the_companion_api_on_loopback(self):
        calls = []

        def run(*arguments, **kwargs):
            calls.append(arguments)
            if arguments[:3] == ("docker", "network", "create"):
                return "network-id"
            if arguments[:3] == ("docker", "network", "inspect"):
                return json.dumps([{"IPAM": {"Config": [{"Subnet": "172.31.99.0/24"}]}}])
            return "container-id-" + str(len(calls))

        with mock.patch.object(DEVSTACK, "run", side_effect=run), mock.patch.object(DEVSTACK.subprocess, "run", return_value=subprocess.CompletedProcess([], 0)), mock.patch.object(DEVSTACK.socket, "create_connection"), mock.patch.object(DEVSTACK.socket, "socket"), mock.patch.object(DEVSTACK.time, "sleep"), mock.patch.object(self.stack, "wait"), mock.patch.object(self.stack, "activate_domain"), mock.patch.object(self.stack, "owned", return_value={"State": {"Running": True}}):
            self.stack.up()
        containers = [arguments for arguments in calls if arguments[:2] == ("docker", "run")]
        published = [arguments[index + 1] for arguments in containers for index, value in enumerate(arguments) if value == "-p"]
        self.assertEqual(published, ["127.0.0.1:18081:8080"])
        self.assertEqual(len(containers), 3)
        self.assertNotIn("internal-ca.key", str(containers))
        self.assertNotIn("stack.env", str(containers[-1]))
        self.assertNotIn("5432", (self.state / "stack.env").read_text())
        self.assertEqual((self.state / "stack.env").stat().st_mode & 0o777, 0o600)

    def test_down_stops_only_recorded_ids_in_reverse_order_after_ownership_check(self):
        resources = {"containers": ["api-id", "smtp-id", "https-id"], "network": "network-id"}
        self.stack.save(resources)
        with mock.patch.object(self.stack, "owned", return_value={"owned": True}) as owned, mock.patch.object(DEVSTACK, "run") as run:
            self.stack.down()
        self.assertEqual(owned.call_args_list, [mock.call("container", "https-id"), mock.call("container", "smtp-id"), mock.call("container", "api-id"), mock.call("network", "network-id")])
        self.assertEqual(run.call_args_list, [
            mock.call("docker", "container", "stop", "--time", "15", "https-id"),
            mock.call("docker", "container", "rm", "https-id"),
            mock.call("docker", "container", "stop", "--time", "15", "smtp-id"),
            mock.call("docker", "container", "rm", "smtp-id"),
            mock.call("docker", "container", "stop", "--time", "15", "api-id"),
            mock.call("docker", "container", "rm", "api-id"),
            mock.call("docker", "network", "rm", "network-id"),
        ])
        self.assertEqual(json.loads(self.stack.manifest.read_text()), {"containers": []})

    def test_missing_manifest_never_touches_docker(self):
        with mock.patch.object(DEVSTACK, "run") as run:
            self.stack.down()
        run.assert_not_called()

    def test_foreign_container_is_refused_even_if_recorded(self):
        result = subprocess.CompletedProcess([], 0, json.dumps([{"Config": {"Labels": {"other": "owner"}}}]))
        self.stack.save({"containers": ["foreign-id"]})
        with mock.patch.object(DEVSTACK.subprocess, "run", return_value=result), mock.patch.object(DEVSTACK, "run") as run:
            with self.assertRaisesRegex(RuntimeError, "refusing to touch unowned"):
                self.stack.down()
            run.assert_not_called()

    def test_up_is_idempotent_only_for_three_live_owned_containers(self):
        self.stack.save({"containers": ["api-id", "smtp-id", "https-id"], "url": "http://127.0.0.1:18081"})
        with mock.patch.object(self.stack, "owned", return_value={"State": {"Running": True}}), mock.patch.object(DEVSTACK, "run") as run:
            self.stack.up()
            run.assert_not_called()
        with mock.patch.object(self.stack, "owned", return_value=None):
            with self.assertRaisesRegex(RuntimeError, "stopped or incomplete"):
                self.stack.up()

    def test_chore_tasks_use_the_same_script_and_never_release_or_install(self):
        tasks = yaml.safe_load((ROOT / "tasks/desktop.yml").read_text())["tasks"]
        for action in ("up", "down"):
            task = tasks[f"devstack:{action}"]
            self.assertEqual(task["dotenv"], [])
            self.assertEqual(task["cmds"], [f'python3 -B "{{{{.DESKTOP_DIR}}}}/scripts/devstack.py" {action} {{{{.CLI_ARGS}}}}'])
            self.assertNotIn("deps", task)


if __name__ == "__main__":
    unittest.main()
