import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]


class ParallelTopology(unittest.TestCase):
    def setUp(self):
        self.tasks = yaml.safe_load((ROOT / "chores.yml").read_text())["tasks"]

    def test_every_instance_starts_before_any_wait_and_all_failures_are_joined(self):
        for failed in ("", "e2e:mail1", "e2e:mail2", "e2e:restmail"):
            with self.subTest(failed=failed), tempfile.TemporaryDirectory() as directory:
                work = Path(directory)
                fake = work / "chore"
                fake.write_text(
                    f"#!{sys.executable}\n"
                    "import os, pathlib, sys, time\n"
                    "work = pathlib.Path(os.environ['WORK'])\n"
                    "task = sys.argv[1]\n"
                    "(work / (task + '.started')).touch()\n"
                    "deadline = time.monotonic() + 600\n"
                    "while len(list(work.glob('*.started'))) < 3:\n"
                    "    if time.monotonic() > deadline: sys.exit(9)\n"
                    "    time.sleep(0.01)\n"
                    "(work / (task + '.finished')).touch()\n"
                    "sys.exit(2 if os.environ['FAILED'] == task else 0)\n"
                )
                fake.chmod(0o755)
                result = subprocess.run(
                    ["sh", "-c", self.tasks["e2e:instances"]["cmds"][0]],
                    env={**os.environ, "CHORE_BIN": str(fake), "WORK": directory, "FAILED": failed},
                    capture_output=True, text=True, timeout=600,
                )
                self.assertEqual(result.returncode, int(bool(failed)), result.stderr)
                self.assertEqual(len(list(work.glob("*.finished"))), 3)

    def test_every_existing_bringup_proof_remains(self):
        startup = self.tasks["e2e:up"]["cmds"]
        tasks = [command["task"] for command in startup if isinstance(command, dict)]
        # The shared testbed is started by a command, not a task call, so that a private
        # topology (#2062), whose substrate is already up, can skip it.
        self.assertTrue(any("testbed:up" in command for command in startup if isinstance(command, str)))
        for name in ("mailserver:init", "mailserver:certs:issue", "instance:certs:issue", "instance:mtls:issue", "instance:dns:register", "e2e:instances", "instance:dkim", "instance:dkim:check"):
            self.assertIn(name, tasks)
        self.assertLess(tasks.index("instance:mtls:issue"), tasks.index("e2e:instances"))
        self.assertLess(tasks.index("e2e:instances"), tasks.index("instance:dkim:check"))
        for reference in ("mail1", "mail2"):
            # CONFIG is the reference server's name, which a private topology replaces (#2062).
            variable = "TOPOLOGY_REF1" if reference == "mail1" else "TOPOLOGY_REF2"
            self.assertTrue(any(f'dkim:provision\n        CONFIG={{{{.{variable} | default "{reference}"}}}}' in command or f'dkim:provision CONFIG={{{{.{variable} | default "{reference}"}}}}' in command for command in startup if isinstance(command, str)))
            waits = [command for command in self.tasks[f"e2e:{reference}"]["cmds"] if isinstance(command, dict) and command["task"] == "mailserver:wait"]
            self.assertEqual([wait["vars"]["CONFIG"] for wait in waits], [f'{{{{.{variable} | default "{reference}"}}}}'])
        self.assertIn("RESTMAIL_INTERNAL_MTLS=true", self.tasks["e2e:restmail"]["cmds"][0])

    def test_frontend_failures_still_fail_normal_instance_startup(self):
        for skipped, failed, expected in (("true", "", []), ("false", "", ["webmail:up", "admin:up"]), ("false", "webmail:up", ["webmail:up"]), ("false", "admin:up", ["webmail:up", "admin:up"])):
            with self.subTest(skipped=skipped, failed=failed), tempfile.TemporaryDirectory() as directory:
                work = Path(directory)
                fake = work / "chore"
                calls = work / "calls"
                fake.write_text(
                    f"#!{sys.executable}\n"
                    "import os, pathlib, sys\n"
                    "with pathlib.Path(os.environ['CALLS']).open('a') as calls: calls.write(sys.argv[1] + '\\n')\n"
                    "sys.exit(2 if sys.argv[1] == os.environ['FAILED'] else 0)\n"
                )
                fake.chmod(0o755)
                result = subprocess.run(
                    ["sh", "-c", self.tasks["instance:frontends:up"]["cmds"][0]],
                    env={**os.environ, "CHORE_BIN": str(fake), "CALLS": str(calls), "FAILED": failed, "RESTMAIL_SKIP_FRONTENDS": skipped},
                    capture_output=True, text=True, timeout=600,
                )
                self.assertEqual(result.returncode, 2 if failed else 0, result.stderr)
                self.assertEqual(calls.read_text().splitlines() if calls.exists() else [], expected)

    def test_frontends_are_opt_in_to_skip_and_health_waits_fail_closed(self):
        self.assertIn("RESTMAIL_SKIP_FRONTENDS=true", self.tasks["e2e:restmail"]["cmds"][0])
        frontends = self.tasks["instance:frontends:up"]["cmds"][0]
        self.assertIn('${RESTMAIL_SKIP_FRONTENDS:-false}', frontends)
        self.assertIn("webmail:up", frontends)
        self.assertIn("admin:up", frontends)
        postgres = next(command for command in self.tasks["instance:up:rest-mail"]["cmds"] if isinstance(command, str) and "postgres healthy" in command)
        self.assertIn("sleep 1", postgres)
        self.assertIn('if [ "$ready" != true ]', postgres)
        self.assertIn("exit 1", postgres)
        self.assertIn("--health-interval 1s", (ROOT / "tasks/postgres.yml").read_text())
        for name in ("gateways:wait", "mailserver:wait"):
            probe = self.tasks[name]["cmds"][0]
            self.assertIn(") &", probe)
            self.assertIn('wait "$pid" || fail=1', probe)
            self.assertIn("sleep 1", probe)


class IndependentReports(unittest.TestCase):
    def setUp(self):
        workflows = ROOT / ".github/workflows"
        self.jobs = yaml.safe_load((workflows / "e2e.yml").read_text())["jobs"]
        self.suite = yaml.safe_load((workflows / "e2e-suite.yml").read_text())["jobs"]

    def test_reports_run_beside_the_gate_but_do_not_decide_it(self):
        self.assertEqual(self.jobs["e2e-reports"]["needs"], ["preflight", "changes"])
        self.assertEqual(self.jobs["e2e-reports"]["with"]["contract-phases"], "reports")
        for job in ("e2e", "e2e-run", "e2e-proofs", "e2e-live"):
            self.assertNotIn("e2e-reports", self.jobs[job].get("needs", []))
        report = next(step for step in self.suite["suite"]["steps"] if step.get("run") == "chore contract:reports")
        self.assertIs(report["continue-on-error"], True)
        gate = next(step for step in self.suite["suite"]["steps"] if step.get("id") == "contract")
        self.assertEqual(gate["env"]["CONTRACT_REPORTS"], "false")
        bringup = next(step for step in self.suite["suite"]["steps"] if step.get("run") == "chore e2e:up")
        self.assertIn("inputs.contract-phases == 'reports'", bringup["env"]["API_COVER"])
        coverage = next(step for step in self.suite["suite"]["steps"] if step.get("name") == "API code coverage of the contract run (report)")
        self.assertIn("inputs.contract-phases == 'reports'", coverage["if"])
        for name in ("Run e2e stages", "Assert stages executed (guard against silent skips)"):
            setup = next(step for step in self.suite["suite"]["steps"] if step.get("name") == name)
            self.assertEqual(setup["if"], "inputs.contract-phases != 'live' && inputs.contract-phases != 'pairs'")

    def test_live_runs_independently_and_the_proof_union_still_gates(self):
        live = self.jobs["e2e-live"]
        self.assertEqual(live["needs"], ["preflight", "changes"])
        self.assertEqual(live["with"]["contract-phases"], "live")
        self.assertIn("e2e-live", self.jobs["e2e-proofs"]["needs"])
        self.assertTrue(self.jobs["e2e-proofs"]["if"].startswith("always()"))
        self.assertIn("e2e-proofs", self.jobs["e2e"]["needs"])
        self.assertNotIn("e2e-live", self.jobs["e2e"]["needs"])
        isolation = self.suite["js-filter-isolation"]
        self.assertEqual(isolation["if"], "inputs.contract-phases == 'gate'")
        self.assertEqual(isolation["env"]["RESTMAIL_SKIP_FRONTENDS"], "true")
        self.assertIn("RESTMAIL_DEV_GOCACHE_VOLUME", isolation["env"])


class IndependentContractPairs(unittest.TestCase):
    def setUp(self):
        self.jobs = yaml.safe_load((ROOT / ".github/workflows/e2e.yml").read_text())["jobs"]
        self.suite = yaml.safe_load((ROOT / ".github/workflows/e2e-suite.yml").read_text())["jobs"]["suite"]
        self.tasks = yaml.safe_load((ROOT / "chores.yml").read_text())["tasks"]

    def test_pairs_run_beside_the_topology_and_both_gate_the_union(self):
        pairs = self.jobs["e2e-contract-pairs"]
        self.assertEqual(pairs["needs"], ["preflight", "changes"])
        self.assertEqual(pairs["with"]["contract-phases"], "pairs")
        self.assertEqual(pairs["uses"], self.jobs["e2e-run"]["uses"])
        for name in ("e2e", "e2e-proofs"):
            self.assertIn("e2e-contract-pairs", self.jobs[name]["needs"])
            self.assertIn("e2e-run", self.jobs[name]["needs"])
        contract = next(step for step in self.suite["steps"] if step.get("id") == "contract")
        self.assertEqual(contract["run"], "chore contract:topology")
        pair_step = next(step for step in self.suite["steps"] if step.get("run") == "chore contract:pairs")
        self.assertEqual(pair_step["if"], "inputs.contract-phases == 'pairs'")
        self.assertEqual(pair_step["env"]["DB_HOST"], "10.99.0.43")
        self.assertFalse(pair_step.get("continue-on-error", False))
        self.assertTrue(any(step.get("run") == "chore e2e:up" for step in self.suite["steps"]))
        self.assertIn("CONTRACT_SKIP_PAIRS=true", self.tasks["contract:topology"]["cmds"][0])
        self.assertIn("chore test:contract-fixtures", self.tasks["contract"]["cmds"][0])

    def test_required_gate_rejects_every_unsuccessful_pair_result(self):
        script = self.jobs["e2e"]["steps"][0]["run"]
        for event in ("merge_group", "workflow_dispatch"):
            for result in ("success", "failure", "cancelled", "skipped"):
                with self.subTest(event=event, result=result):
                    execution = subprocess.run(
                        ["bash", "-c", script], capture_output=True, text=True,
                        env={**os.environ, "PREFLIGHT_RESULT": "success", "CHANGES_RESULT": "success", "RUN_E2E": "true",
                             "E2E_RESULT": "success", "PAIRS_RESULT": result,
                             "PROOFS_RESULT": "success", "EVENT_NAME": event},
                    )
                    self.assertEqual(execution.returncode, int(result != "success"), execution.stdout)

    def test_artifact_names_do_not_collide_and_live_inputs_are_unchanged(self):
        upload = next(step for step in self.suite["steps"] if step.get("name") == "Upload container logs")
        pairs = next(step for step in self.suite["steps"] if step.get("name") == "Upload example-pair logs")
        self.assertEqual(pairs["with"]["name"], "e2e-contract-pairs-logs")
        self.assertEqual(pairs["if"], "always() && inputs.contract-phases == 'pairs'")
        self.assertEqual(upload["with"]["name"], "e2e-container-logs")
        self.assertIn("inputs.contract-phases != 'pairs'", upload["if"])
        download = next(step for step in self.jobs["e2e-proofs"]["steps"] if step.get("name") == "Download the live proofs")
        self.assertEqual(download["with"]["name"], "live-example-proofs")


if __name__ == "__main__":
    unittest.main()
