"""Keep the stable queue example separate from boundary and authorization probes."""

import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import textwrap
import types
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]


class ContractQueueFixtureTests(unittest.TestCase):
    def setUp(self):
        hooks = types.ModuleType("vertrag_hooks")
        hooks.before = hooks.after = lambda *args: lambda function: function
        spec = importlib.util.spec_from_file_location("contract_hooks_test", ROOT / "tests/contract/hooks.py")
        self.module = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {"vertrag_hooks": hooks}):
            spec.loader.exec_module(self.module)

    def test_example_uses_the_held_fixture_without_changing_the_principal(self):
        for headers in ({}, {"Authorization": "Bearer synthetic-token"}):
            with self.subTest(headers=headers):
                transaction = {"fullPath": "/api/v1/admin/queue?status=pending", "request": {"headers": dict(headers)}}
                self.module.list_the_sent_messages_queue_entry(transaction)
                self.assertEqual(transaction["fullPath"], "/api/v1/admin/queue?recipient=contract-retry%40retry.invalid")
                self.assertEqual(transaction["request"]["headers"], headers)

    def test_other_queries_and_invalid_boundary_values_are_unchanged(self):
        for path in ("/api/v1/admin/queue?status=delivered", "/api/v1/admin/queue?status=not-a-status",
                     "/api/v1/admin/queue?limit=-1", "/api/v1/admin/queue?offset=1.5"):
            with self.subTest(path=path):
                transaction = {"fullPath": path}
                self.module.list_the_sent_messages_queue_entry(transaction)
                self.assertEqual(transaction["fullPath"], path)

    def test_task_selects_the_fixture_container_and_refuses_other_configs(self):
        source = (ROOT / "chores.yml").read_text()
        task = source.split("\n  test:contract-fixtures:\n", 1)[1].split("\n  test:e2e:\n", 1)[0]
        script = textwrap.dedent(task.split("      - |\n", 1)[1])
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory)
            capture = work / "docker.json"
            docker = work / "docker"
            docker.write_text("#!/usr/bin/env python3\nimport json, os, sys\n"
                              "open(os.environ['CAPTURE'], 'w').write(json.dumps(sys.argv[1:]))\n")
            docker.chmod(0o755)
            env = dict(os.environ, PATH=str(work) + os.pathsep + os.environ["PATH"],
                       CAPTURE=str(capture), CONTRACT_FIXTURE_CONFIG="restmail.test",
                       CONTRACT_FIXTURE_PROJECT="custom-test-project")
            result = subprocess.run(["sh", "-eu"], input=script, text=True,
                                    env=env, capture_output=True, timeout=5)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(json.loads(capture.read_text()),
                             ["exec", "--", "custom-test-project-api", "go", "run",
                              "./tests/contract/fixtures", "--test-domain=restmail.test"])
            capture.unlink()
            env["CONTRACT_FIXTURE_CONFIG"] = "production.example"
            result = subprocess.run(["sh", "-eu"], input=script, text=True,
                                    env=env, capture_output=True, timeout=5)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(capture.exists())


if __name__ == "__main__":
    unittest.main()
