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
        for name in ("mfa_admin.py", "api_keys_live.py", "owner_key.py"):
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




"""The contract phases' raised rate limits stay theirs, and the hooks' switch
matches what the API was given (#1979)."""

import importlib.util
import os
import re
import sys
import types
import unittest
from pathlib import Path
from unittest.mock import patch

import yaml

import rate_limits

ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = ROOT / ".github/workflows/e2e-suite.yml"
CHORES = ROOT / "chores.yml"
CONFIG = ROOT / "internal/config/config.go"
ENV_FILE = "tests/contract/raised-limits.env"
PACED_FILES = ("hooks.py", "coverage_target_hooks.py", "api_keys_live.py", "mfa_admin.py")


def steps():
    jobs = yaml.safe_load(WORKFLOW.read_text())["jobs"]
    for job in jobs.values():
        names = [step.get("name", "") for step in job.get("steps", [])]
        if any(name.startswith("Limits suite") for name in names):
            return job["steps"]
    raise AssertionError("no job in e2e-suite.yml runs the Limits suite")


def index(all_steps, prefix):
    found = [i for i, step in enumerate(all_steps) if step.get("name", "").startswith(prefix)]
    if len(found) != 1:
        raise AssertionError(f"want one step named {prefix!r}, found {len(found)}")
    return found[0]


def chore(name):
    return yaml.safe_load(CHORES.read_text())["tasks"][name]


def commands(task):
    return "\n".join(c if isinstance(c, str) else str(c) for c in task["cmds"])


class RaisedSettings(unittest.TestCase):
    def test_every_setting_is_one_the_server_reads_and_raises_it(self):
        config = CONFIG.read_text()
        wanted = rate_limits.settings()
        self.assertEqual(set(wanted), {
            f"{prefix}_RATE_LIMIT_{knob}"
            for prefix in ("AUTH", "PIPELINE_TEST", "TLSRPT") for knob in ("RPS", "BURST")
        })
        for key, value in wanted.items():
            read = re.search(r'getEnv\w*\("' + key + r'",\s*(Default\w+)\)', config)
            self.assertIsNotNone(read, f"internal/config/config.go does not read {key}")
            default = re.search(r"\b" + read[1] + r"\s*=\s*([0-9.]+)", config)
            self.assertIsNotNone(default, f"no value for {read[1]}")
            self.assertGreater(float(value), 10 * float(default[1]), f"{key}={value} does not raise {read[1]}")
        # The limiters stay on: only their rates move.
        for prefix in ("AUTH", "PIPELINE_TEST", "TLSRPT"):
            self.assertNotIn(f"{prefix}_RATE_LIMIT_ENABLED", wanted)


class Checker(unittest.TestCase):
    RAISED = [f"{k}={v}" for k, v in rate_limits.settings().items()] + ["DB_HOST=10.99.0.43"]
    DEFAULT = ["DB_HOST=10.99.0.43", "LOG_LEVEL=debug"]

    def test_raised_needs_every_setting_and_the_switch(self):
        self.assertEqual(rate_limits.problems("raised", self.RAISED, switch="1"), [])
        self.assertTrue(rate_limits.problems("raised", self.RAISED, switch=""))
        self.assertTrue(rate_limits.problems("raised", self.DEFAULT, switch="1"))
        self.assertTrue(rate_limits.problems("raised", self.RAISED[1:], switch="1"))
        changed = ["AUTH_RATE_LIMIT_RPS=1"] + [e for e in self.RAISED if not e.startswith("AUTH_RATE_LIMIT_RPS=")]
        self.assertTrue(rate_limits.problems("raised", changed, switch="1"))

    def test_a_raised_setting_or_the_switch_fails_the_defaults(self):
        self.assertEqual(rate_limits.problems("default", self.DEFAULT, switch=""), [])
        self.assertTrue(rate_limits.problems("default", self.RAISED, switch=""))
        self.assertTrue(rate_limits.problems("default", self.DEFAULT + ["TLSRPT_RATE_LIMIT_BURST=1000"], switch=""))
        self.assertTrue(rate_limits.problems("default", self.DEFAULT, switch="1"))

    def test_the_switch_reads_one_value(self):
        for value, want in (("1", True), ("", False), ("0", False), ("true", False)):
            with patch.dict(os.environ, {rate_limits.SWITCH: value, "RESTMAIL_CONTRACT_LIMITS_RAISED": "false"}):
                self.assertIs(rate_limits.raised(), want)
        with patch.dict(os.environ, {}, clear=True):
            self.assertFalse(rate_limits.raised())


class Wiring(unittest.TestCase):
    def test_only_independent_contract_topologies_run_raised(self):
        workflow = yaml.safe_load(WORKFLOW.read_text())
        setting = workflow["jobs"]["suite"]["env"]["RESTMAIL_CONTRACT_LIMITS_RAISED"]
        for phase in ("gate", "pairs", "reports", "live"):
            self.assertIn("inputs.contract-phases == '" + phase + "'", setting)
        self.assertNotIn("inputs.contract-phases == 'fuzz'", setting)
        self.assertNotIn("inputs.contract-phases == 'faults'", setting)
        for step in workflow["jobs"]["suite"]["steps"]:
            self.assertNotIn(rate_limits.SWITCH, step.get("env") or {})
            self.assertNotIn("raised-limits.env", str(step.get("run", "")))

    def test_the_chores_give_the_file_and_check_the_api(self):
        raise_cmds = commands(chore("contract:limits:raise"))
        self.assertIn(f'RESTMAIL_API_LIMITS_ENV="{{{{.ROOT_DIR}}}}/{ENV_FILE}"', raise_cmds)
        self.assertIn("api:up", raise_cmds)
        default_cmds = commands(chore("contract:limits:default"))
        self.assertIn("env -u RESTMAIL_API_LIMITS_ENV", default_cmds)
        self.assertIn("rate_limits.py check default", default_cmds)
        # chore contract refuses a switch the API does not match.
        self.assertIn('if [ -n "${CONTRACT_RAISED_LIMITS:-}" ]', commands(chore("contract")))
        self.assertIn("rate_limits.py check raised", commands(chore("contract")))
        api = (ROOT / "tasks/api.yml").read_text()
        self.assertIn('LIMITS_FLAGS="--env-file $RESTMAIL_API_LIMITS_ENV"', api)
        self.assertIn("$LIMITS_FLAGS", api)

    def test_the_limits_suite_checks_its_api_runs_on_the_defaults(self):
        cmds = [c if isinstance(c, str) else str(c) for c in chore("test:limits")["cmds"]]
        recreate = next(i for i, c in enumerate(cmds) if "api:up" in c)
        check = next(i for i, c in enumerate(cmds) if "--assert-defaults" in c)
        suite = next(i for i, c in enumerate(cmds) if "TestLimits" in c)
        self.assertLess(recreate, check)
        self.assertLess(check, suite)
        self.assertIn("RESTMAIL_CONTRACT_LIMITS_RAISED=false", cmds[recreate])
        self.assertNotIn("raised-limits.env", "\n".join(cmds))


class Pacing(unittest.TestCase):
    def load_hooks(self):
        hooks = types.ModuleType("vertrag_hooks")
        hooks.before = lambda pattern: lambda function: function
        hooks.after = lambda pattern: lambda function: function
        spec = importlib.util.spec_from_file_location("rate_limit_hooks", ROOT / "tests/contract/hooks.py")
        module = importlib.util.module_from_spec(spec)
        with patch.dict(sys.modules, {"vertrag_hooks": hooks}):
            spec.loader.exec_module(module)
        return module

    def test_every_rate_limit_wait_goes_through_the_switch(self):
        for name in PACED_FILES:
            source = (ROOT / "tests/contract" / name).read_text()
            for line in source.splitlines():
                if re.search(r"time\.sleep\((1\.1|0\.55)\)", line):
                    self.fail(f"{name}: {line.strip()} paces without rate_limits.pace")
        hooks = (ROOT / "tests/contract/hooks.py").read_text()
        tls = hooks[hooks.index("def pace_the_tls_reports"):]
        self.assertIn("rate_limits.pace(1)", tls[:tls.index("\n\n\n")])

    def test_paced_hooks_wait_only_on_the_default_limits(self):
        module = self.load_hooks()
        for pace, seconds in ((module.pace_auth_requests, 1.1), (module.pace_owner_requests, 1.1),
                              (module.pace_pipeline_previews, 0.55), (module.pace_the_tls_reports, 1)):
            with patch.dict(os.environ, {rate_limits.SWITCH: "", "RESTMAIL_CONTRACT_LIMITS_RAISED": "false"}), patch.object(module.time, "sleep") as sleep:
                pace({})
            sleep.assert_called_once_with(seconds)
            with patch.dict(os.environ, {rate_limits.SWITCH: "1"}), patch.object(module.time, "sleep") as sleep:
                pace({})
            sleep.assert_not_called()


if __name__ == "__main__":
    unittest.main()


if __name__ == "__main__":
    unittest.main()
