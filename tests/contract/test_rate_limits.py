"""Contract pacing can disappear only alongside raised API limits."""

import copy
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import rate_limits
import yaml

ROOT = Path(__file__).resolve().parents[2]


class ContractRateLimits(unittest.TestCase):
    def test_pacing_only_disappears_for_the_explicit_raised_setting(self):
        for setting in ("", "false", "TRUE", "true"):
            with self.subTest(setting=setting), patch.dict(
                os.environ, {"RESTMAIL_CONTRACT_LIMITS_RAISED": setting}
            ), patch.object(rate_limits.time, "sleep") as sleep:
                rate_limits.pace(1.1)
                if setting == "true":
                    sleep.assert_not_called()
                else:
                    sleep.assert_called_once_with(1.1)

    def test_all_hook_throttle_waits_use_the_same_pacing_policy(self):
        source = (ROOT / "tests/contract/hooks.py").read_text()
        self.assertEqual(source.count("rate_limits.pace("), 13)
        self.assertNotIn("time.sleep(1.1)", source)
        self.assertNotIn("time.sleep(0.55)", source)
        self.assertIn("time.sleep(int(retry_after))", source)
        self.assertIn("time.sleep(1)", source)
        for name in ("mfa_admin.py", "api_keys_live.py", "owner_key.py"):
            helper = (ROOT / "tests/contract" / name).read_text()
            self.assertIn("rate_limits.pace(1.1)", helper)
            self.assertNotIn("time.sleep(1.1)", helper)

    def test_topology_setting_and_api_flags_are_connected_and_opt_in(self):
        chores = yaml.safe_load((ROOT / "chores.yml").read_text())
        mapping = chores["includes"]["api"]["vars"]["API_CONTRACT_LIMITS_RAISED"]
        self.assertIn('.RESTMAIL_CONTRACT_LIMITS_RAISED | default "false"', mapping)
        api = yaml.safe_load((ROOT / "tasks/api.yml").read_text())
        self.assertIn('default "false"', api["vars"]["API_CONTRACT_LIMITS_RAISED"])
        startup = next(command for command in api["tasks"]["up"]["cmds"] if isinstance(command, str) and "CONTRACT_LIMITS=" in command)
        self.assertIn('if [ "{{.API_CONTRACT_LIMITS_RAISED}}" = "true" ]', startup)
        self.assertIn("tests/contract/rate_limits.py --docker-flags", startup)
        self.assertLess(startup.index('--env-file "$SECRETS_ENV"'), startup.index('$CONTRACT_LIMITS \\\n'))
        suite = yaml.safe_load((ROOT / ".github/workflows/e2e-suite.yml").read_text())["jobs"]["suite"]
        setting = suite["env"]["RESTMAIL_CONTRACT_LIMITS_RAISED"]
        for phase in ("gate", "pairs", "reports", "live"):
            self.assertIn(f"inputs.contract-phases == '{phase}'", setting)

    def test_raised_flags_keep_all_throttles_enabled(self):
        flags = rate_limits.docker_flags().split()
        self.assertEqual(flags[::2], ["-e"] * len(rate_limits.DEFAULTS))
        environment = dict(entry.split("=", 1) for entry in flags[1::2])
        self.assertEqual(set(environment), set(rate_limits.DEFAULTS))
        for name, value in environment.items():
            self.assertEqual(value, "true" if name.endswith("_ENABLED") else "10000")
        with self.assertRaisesRegex(ValueError, "requires the default"):
            rate_limits.assert_defaults([{"Config": {"Env": flags[1::2]}}])

    def test_defaults_guard_rejects_each_leaked_setting_without_printing_secrets(self):
        default_environment = [f"{name}={value}" for name, value in rate_limits.DEFAULTS.items()]
        for environment in ([], default_environment):
            rate_limits.assert_defaults([{"Config": {"Env": environment}}])
        for name in rate_limits.DEFAULTS:
            with self.subTest(name=name):
                environment = copy.copy(default_environment)
                environment.append(f"{name}=false" if name.endswith("_ENABLED") else f"{name}=10000")
                result = subprocess.run(
                    [sys.executable, str(ROOT / "tests/contract/rate_limits.py"), "--assert-defaults"],
                    input=json.dumps([{"Config": {"Env": environment + ["JWT_SECRET=do-not-print"]}}]),
                    capture_output=True, text=True,
                )
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(name, result.stderr)
                self.assertNotIn("do-not-print", result.stdout + result.stderr)

    def test_defaults_guard_accepts_unset_docker_environment_entries(self):
        unset_environment = [
            "OTEL_EXPORTER_OTLP_ENDPOINT",
            "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
            "OTEL_EXPORTER_OTLP_HEADERS",
            "OTEL_SERVICE_NAME",
            "OTEL_TRACES_SAMPLER",
            "OTEL_TRACES_SAMPLER_ARG",
        ]
        for environment in (
            unset_environment,
            unset_environment + list(rate_limits.DEFAULTS),
            unset_environment + [
                f"{name}={value}" for name, value in rate_limits.DEFAULTS.items()
            ],
            unset_environment + ["OTEL_EXPORTER_OTLP_HEADERS=key=value"],
        ):
            with self.subTest(environment=environment):
                rate_limits.assert_defaults([{"Config": {"Env": environment}}])

    def test_unset_otel_entries_do_not_hide_nondefault_or_empty_limits(self):
        for name in rate_limits.DEFAULTS:
            changed = "false" if name.endswith("_ENABLED") else "10000"
            for value in (changed, ""):
                with self.subTest(name=name, value=value):
                    environment = [
                        "OTEL_EXPORTER_OTLP_ENDPOINT",
                        f"{name}={value}",
                        "OTEL_SERVICE_NAME",
                    ]
                    with self.assertRaisesRegex(ValueError, f"requires the default {name}"):
                        rate_limits.assert_defaults([{"Config": {"Env": environment}}])

    def test_guard_defaults_match_the_production_constants(self):
        source = (ROOT / "internal/config/config.go").read_text()
        for prefix, constant in (("AUTH", "Auth"), ("PIPELINE_TEST", "PipelineTest"), ("TLSRPT", "TLSRPT")):
            for suffix in ("RPS", "BURST"):
                name = f"{prefix}_RATE_LIMIT_{suffix}"
                go_name = f"Default{constant}RateLimit{suffix.title() if suffix == 'BURST' else suffix}"
                value = re.search(rf"\b{go_name}\s*=\s*([\d.]+)", source)
                self.assertIsNotNone(value, go_name)
                self.assertEqual(float(value[1]), float(rate_limits.DEFAULTS[name]))

    def test_limits_recreates_the_api_with_both_overrides_off_before_running(self):
        tasks = yaml.safe_load((ROOT / "chores.yml").read_text())["tasks"]
        commands = tasks["test:limits"]["cmds"]
        with tempfile.TemporaryDirectory() as directory:
            chore = Path(directory) / "chore"
            chore.write_text(
                "#!/bin/sh\n"
                'test "$1" = api:up || exit 11\n'
                'test "$RESTMAIL_CONTRACT_LIMITS_RAISED" = false || exit 12\n'
                'test "$RESTMAIL_LOAD_LIMITS_OFF" = false || exit 13\n'
                'test "$RESTMAIL_INTERNAL_MTLS" = true || exit 14\n'
            )
            chore.chmod(0o755)
            result = subprocess.run(
                ["sh", "-c", commands[0]], capture_output=True, text=True,
                env={**os.environ, "CHORE_BIN": str(chore), "RESTMAIL_CONTRACT_LIMITS_RAISED": "true", "RESTMAIL_LOAD_LIMITS_OFF": "true"},
            )
            self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(commands[1], {"task": "health:wait"})
        self.assertIn("docker inspect", commands[2])
        self.assertIn("--assert-defaults", commands[2])
        self.assertIn("^TestLimits$", commands[3])
        teardown = tasks["test:contract-teardown"]["cmds"][0]
        self.assertIn("api:up", teardown)
        self.assertNotIn("RESTMAIL_CONTRACT_LIMITS_RAISED=false", teardown)


if __name__ == "__main__":
    unittest.main()
