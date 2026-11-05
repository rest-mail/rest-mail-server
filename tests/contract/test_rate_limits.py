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
            with patch.dict(os.environ, {rate_limits.SWITCH: value}):
                self.assertIs(rate_limits.raised(), want)
        with patch.dict(os.environ, {}, clear=True):
            self.assertFalse(rate_limits.raised())


class Wiring(unittest.TestCase):
    def test_only_the_contract_step_runs_raised(self):
        all_steps = steps()
        raise_at = index(all_steps, "Raise the API's rate limits")
        contract = index(all_steps, "Contract tests (example pairs")
        restore = index(all_steps, "Put the API's default rate limits back")
        self.assertLess(raise_at, contract)
        self.assertLess(contract, restore)
        for later in ("Contract live proofs", "Limits suite", "Contract teardown"):
            self.assertLess(restore, index(all_steps, later), later)
        self.assertEqual(all_steps[raise_at]["run"].strip(), "chore contract:limits:raise")
        self.assertEqual(all_steps[restore]["run"].strip(), "chore contract:limits:default")
        self.assertTrue(all_steps[restore]["if"].startswith("always()"))
        # The raise and its restore build the API the way bring-up does.
        bring_up = all_steps[index(all_steps, "Bring up e2e topology")]["env"]["API_COVER"]
        self.assertEqual(all_steps[raise_at]["env"]["API_COVER"], bring_up)
        self.assertEqual(all_steps[restore]["env"]["API_COVER"], bring_up)
        # The switch is the contract step's alone, and no step names the file.
        for i, step in enumerate(all_steps):
            env = step.get("env") or {}
            if i == contract:
                self.assertEqual(env.get(rate_limits.SWITCH), "1")
            else:
                self.assertNotIn(rate_limits.SWITCH, env, step.get("name"))
            self.assertNotIn("RESTMAIL_API_LIMITS_ENV", env, step.get("name"))
            self.assertNotIn("raised-limits.env", str(step.get("run", "")), step.get("name"))

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
        # A leak of the contract phases' configuration fails the Limits phase
        # before the suite spends a single limit.
        cmds = [c if isinstance(c, str) else str(c) for c in chore("test:limits")["cmds"]]
        check = next(i for i, c in enumerate(cmds) if "rate_limits.py check default" in c)
        suite = next(i for i, c in enumerate(cmds) if "TestLimits" in c)
        restart = next(i for i, c in enumerate(cmds) if "docker restart" in c)
        self.assertLess(restart, check)
        self.assertLess(check, suite)
        text = "\n".join(cmds)
        self.assertNotIn("raised-limits.env", text)
        self.assertNotIn("RESTMAIL_API_LIMITS_ENV", text)
        self.assertNotIn("CONTRACT_RAISED_LIMITS=1", text)


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
            with patch.dict(os.environ, {rate_limits.SWITCH: ""}), patch.object(module.time, "sleep") as sleep:
                pace({})
            sleep.assert_called_once_with(seconds)
            with patch.dict(os.environ, {rate_limits.SWITCH: "1"}), patch.object(module.time, "sleep") as sleep:
                pace({})
            sleep.assert_not_called()


if __name__ == "__main__":
    unittest.main()
