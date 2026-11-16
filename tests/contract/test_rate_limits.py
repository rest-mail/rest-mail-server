"""Contract pacing can disappear only alongside raised API limits."""

import ast
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
    def test_coverage_sends_stay_under_the_outbound_pipelines_rate(self):
        # The default outbound rate_limit filter allows 20 a minute; the spacing
        # holds whether or not the API's own limits are raised.
        templates = (ROOT / "internal/pipeline/templates.go").read_text()
        per_minute = re.search(r'"max_per_minute": *(\d+)', templates)
        self.assertIsNotNone(per_minute)
        self.assertGreater(rate_limits.SEND_SPACING * int(per_minute.group(1)), 60)
        for setting in ("", "true"):
            with self.subTest(raised=setting), patch.dict(
                os.environ, {"RESTMAIL_CONTRACT_LIMITS_RAISED": setting}
            ), patch.object(rate_limits.time, "sleep") as sleep:
                rate_limits.pace_sends()
                sleep.assert_called_once_with(rate_limits.SEND_SPACING)

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
        paced = {}
        for function in ast.walk(ast.parse(source)):
            if not isinstance(function, ast.FunctionDef):
                continue
            waits = [
                ast.literal_eval(call.args[0])
                for call in ast.walk(function)
                if isinstance(call, ast.Call)
                and isinstance(call.func, ast.Attribute)
                and isinstance(call.func.value, ast.Name)
                and call.func.value.id == "rate_limits"
                and call.func.attr == "pace"
            ]
            if waits:
                paced[function.name] = waits
        self.assertEqual(paced, {
            "a_credential_of_its_own": [1.1],
            "an_api_key_of_its_own_for_a_probe": [1.1],
            "an_api_key_of_its_own_to_edit": [1.1],
            "pace_auth_requests": [1.1],
            "pace_owner_requests": [1.1],
            "pace_pipeline_previews": [0.55],
            "pace_the_tls_reports": [1],
            "recover_an_account_of_its_own": [1.1],
            "sign_the_status_with_the_runs_owner_key": [1.1],
            "mint_for_an_account_of_its_own": [1.1],
            "reset_an_account_of_its_own": [1.1],
            "refuse_a_status_key_that_is_not_listed": [1.1],
            "refuse_a_token_to_a_key_that_is_not_an_owner": [1.1],
            "refuse_a_token_for_an_account_that_does_not_exist": [1.1],
            "a_throwaway_grant": [1.1],
        })
        self.assertNotIn("time.sleep(1.1)", source)
        self.assertNotIn("time.sleep(0.55)", source)
        self.assertIn("time.sleep(int(retry_after))", source)
        self.assertIn("time.sleep(1)", source)
        for name in ("mfa_admin.py", "api_keys_live.py", "owner_key.py", "coverage_target_hooks.py"):
            helper = (ROOT / "tests/contract" / name).read_text()
            self.assertIn("rate_limits.pace(1.1)", helper)
            self.assertNotIn("time.sleep(1.1)", helper)

    def test_each_missing_pacing_call_is_detected(self):
        path = ROOT / "tests/contract/hooks.py"
        source = path.read_text()
        calls = [
            node for node in ast.walk(ast.parse(source))
            if isinstance(node, ast.Call)
            and isinstance(node.func, ast.Attribute)
            and isinstance(node.func.value, ast.Name)
            and node.func.value.id == "rate_limits"
            and node.func.attr == "pace"
        ]
        self.assertEqual(len(calls), 15)
        for call in calls:
            with self.subTest(line=call.lineno):
                lines = source.splitlines(keepends=True)
                line = lines[call.lineno - 1]
                lines[call.lineno - 1] = line[:call.col_offset] + "None" + line[call.end_col_offset:]
                with patch.object(Path, "read_text", return_value="".join(lines)):
                    with self.assertRaises(AssertionError):
                        self.test_all_hook_throttle_waits_use_the_same_pacing_policy()

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
        own_data = tasks["test:contract-own-data"]["cmds"][0]
        self.assertIn("api:up", own_data)
        self.assertNotIn("RESTMAIL_CONTRACT_LIMITS_RAISED=false", own_data)


class ContainerRateLimitChecks(unittest.TestCase):
    def test_settings_match_the_existing_raised_flags(self):
        settings = rate_limits.settings()
        self.assertEqual(set(settings), set(rate_limits.DEFAULTS))
        self.assertEqual(rate_limits.docker_flags().split()[1::2], [
            f"{name}={value}" for name, value in settings.items()
        ])
        for name, value in settings.items():
            if name.endswith("_ENABLED"):
                self.assertEqual(value, "true")
            else:
                self.assertGreater(float(value), 10 * float(rate_limits.DEFAULTS[name]))

    def test_raised_checks_every_setting_and_the_pacing_switch(self):
        settings = rate_limits.settings()
        environment = [f"{name}={value}" for name, value in settings.items()]
        self.assertEqual(rate_limits.problems("raised", environment, switch="true"), [])
        for switch in ("", "false", "TRUE"):
            self.assertTrue(rate_limits.problems("raised", environment, switch=switch))
        for name in settings:
            with self.subTest(name=name):
                missing = [entry for entry in environment if not entry.startswith(name + "=")]
                changed = missing + [f"{name}={rate_limits.DEFAULTS[name]}"]
                self.assertTrue(rate_limits.problems("raised", missing, switch="true"))
                if settings[name] != rate_limits.DEFAULTS[name]:
                    self.assertTrue(rate_limits.problems("raised", changed, switch="true"))

    def test_defaults_allow_explicit_defaults_but_not_overrides_or_a_raised_switch(self):
        defaults = [f"{name}={value}" for name, value in rate_limits.DEFAULTS.items()]
        for environment in ([], defaults, defaults + ["OTEL_SERVICE_NAME"]):
            self.assertEqual(rate_limits.problems("default", environment, switch="false"), [])
            self.assertTrue(rate_limits.problems("default", environment, switch="true"))
        for name in rate_limits.DEFAULTS:
            changed = "false" if name.endswith("_ENABLED") else "10000"
            self.assertTrue(rate_limits.problems("default", defaults + [f"{name}={changed}"], switch="false"))

    def test_cli_checks_the_container_environment_without_printing_secrets(self):
        raised = [f"{name}={value}" for name, value in rate_limits.settings().items()]
        with tempfile.TemporaryDirectory() as directory:
            docker = Path(directory) / "docker"
            docker.write_text(
                '#!/bin/sh\n'
                'test "$1" = inspect && test "$4" = test-api || exit 90\n'
                'printf "%s" "$RATE_TEST_ENV"\n'
            )
            docker.chmod(0o755)
            for expect, environment, switch, code in (
                ("raised", raised, "true", 0),
                ("raised", raised, "false", 1),
                ("default", [], "false", 0),
                ("default", raised, "false", 1),
                ("default", [], "true", 1),
            ):
                with self.subTest(expect=expect, switch=switch, code=code):
                    result = subprocess.run(
                        [sys.executable, str(ROOT / "tests/contract/rate_limits.py"), "check", expect, "test-api"],
                        capture_output=True, text=True,
                        env={**os.environ, "PATH": directory + os.pathsep + os.environ["PATH"],
                             rate_limits.SWITCH: switch,
                             "RATE_TEST_ENV": json.dumps(environment + ["JWT_SECRET=do-not-print"])},
                    )
                    self.assertEqual(result.returncode, code, result.stderr)
                    self.assertNotIn("do-not-print", result.stdout + result.stderr)

    def test_raise_restore_and_contract_checks_preserve_the_existing_limits_gate(self):
        tasks = yaml.safe_load((ROOT / "chores.yml").read_text())["tasks"]
        self.assertEqual(tasks["contract:limits:raise"]["env"][rate_limits.SWITCH], "true")
        self.assertIn("raised-limits.env", tasks["contract:limits:raise"]["cmds"][0])
        self.assertIn("check raised", tasks["contract:limits:raise"]["cmds"][-1])
        self.assertEqual(tasks["contract:limits:default"]["env"][rate_limits.SWITCH], "false")
        self.assertIn("env -u RESTMAIL_API_LIMITS_ENV", tasks["contract:limits:default"]["cmds"][0])
        self.assertIn("check default", tasks["contract:limits:default"]["cmds"][-1])
        self.assertIn("check raised", tasks["contract"]["cmds"][0])
        self.assertEqual(tasks["test:limits"]["env"][rate_limits.SWITCH], "false")
        self.assertIn("env -u RESTMAIL_API_LIMITS_ENV", tasks["test:limits"]["cmds"][0])


if __name__ == "__main__":
    unittest.main()
