"""One pin manifest selects the substrate locally and in CI, and the preflight proves it (#1751)."""

import io
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import substrate_pins  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
A = "a" * 40
B = "b" * 40

TASKS = """tasks:
  dkim:provision:
    cmds:
      - docker run --rm -v "$(pwd)/$KEYDIR:/keys" alpine:3.20 sh -c 'opendkim-genkey'
      - chmod 644 "$KEYDIR/$DOMAIN.$SEL.key"
"""


def git(repo, *args):
    return subprocess.run(
        ["git", "-C", str(repo), "-c", "user.name=t", "-c", "user.email=t@t.test",
         "-c", "commit.gpgsign=false", *args],
        capture_output=True, text=True, check=True,
    ).stdout.strip()


def commit(repo, files):
    for name, text in files.items():
        (repo / name).write_text(text)
    git(repo, "add", "-A")
    git(repo, "commit", "--quiet", "-m", "c")
    return git(repo, "rev-parse", "HEAD")


class Manifest(unittest.TestCase):
    def test_the_repository_manifest_pins_every_substrate(self):
        pinned = substrate_pins.read_pins(ROOT / "scripts/substrate-pins.yml")
        self.assertEqual(set(pinned), {"testbed", "reference-mailserver"})
        for commit_ in pinned.values():
            self.assertRegex(commit_, r"^[0-9a-f]{40}$")

    def test_reads_pins_and_ignores_comments(self):
        text = f"# pins\n\ntestbed: {A}\nreference-mailserver: {B}  \n"
        self.assertEqual(
            substrate_pins.pins(text), {"testbed": A, "reference-mailserver": B}
        )

    def test_rejects_what_is_not_one_full_commit_per_substrate(self):
        for text, message in (
            (f"testbed: {A}\n", "no pin for reference-mailserver"),
            (f"testbed: {A}\ntestbed: {A}\nreference-mailserver: {B}\n", "twice"),
            (f"testbed: {A[:12]}\nreference-mailserver: {B}\n", "not a full"),
            (f"testbed: master\nreference-mailserver: {B}\n", "not a full"),
            (f"testbed: {A}\nreference-mailserver: {B}\nwebsite: {A}\n", "no substrate"),
            (f"testbed {A}\n", "not `name: commit`"),
        ):
            with self.subTest(text=text):
                with self.assertRaisesRegex(ValueError, message):
                    substrate_pins.pins(text)

    def test_ci_and_the_init_tasks_read_the_manifest_not_their_own_pins(self):
        workflow = (ROOT / ".github/workflows/e2e-suite.yml").read_text()
        chores = (ROOT / "chores.yml").read_text()
        for pin in substrate_pins.read_pins(ROOT / "scripts/substrate-pins.yml").values():
            self.assertNotIn(pin, workflow)
            self.assertNotIn(pin, chores)
        self.assertNotRegex(workflow, r"(TESTBED|REFERENCE_MAILSERVER)_REF")
        self.assertIn("chore testbed:init\n", workflow)
        self.assertIn("chore mailserver:init\n", workflow)
        self.assertIn("scripts/substrate_pins.py preflight\n", workflow)
        self.assertIn("substrate_pins.py sync testbed", chores)
        self.assertIn("substrate_pins.py sync reference-mailserver", chores)
        self.assertNotRegex(chores, r"git clone git@github\.com:rest-mail/(testbed|reference-mailserver)")


class Preparation(unittest.TestCase):
    def test_runs_the_host_chmod_in_a_container_once(self):
        prepared = substrate_pins.prepared_text(TASKS)
        self.assertNotIn(substrate_pins.DKIM_HOST_CHMOD, prepared)
        self.assertIn(substrate_pins.DKIM_CONTAINER_CHMOD, prepared)
        self.assertEqual(substrate_pins.prepared_text(prepared), prepared)

    def test_fails_when_upstream_changed_the_task(self):
        with self.assertRaisesRegex(ValueError, "changed upstream"):
            substrate_pins.prepared_text("tasks: {}\n")

    def test_only_reference_mailserver_on_linux_is_prepared(self):
        self.assertTrue(substrate_pins.needs_preparation("reference-mailserver", "Linux"))
        self.assertFalse(substrate_pins.needs_preparation("reference-mailserver", "Darwin"))
        self.assertFalse(substrate_pins.needs_preparation("testbed", "Linux"))


class Clones(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        base = Path(self.tmp.name)
        self.upstream = {}
        self.commits = {}
        for name in substrate_pins.REPOS:
            repo = base / "upstream" / name
            repo.mkdir(parents=True)
            git(repo, "init", "--quiet")
            first = commit(repo, {"chores.yml": TASKS, "README": "one\n"})
            second = commit(repo, {"README": "two\n", "chores.yml": TASKS + "# two\n"})
            self.upstream[name] = repo
            self.commits[name] = (first, second)
        self.workspace = base / "workspace"
        self.server = base / "server"
        self.server.mkdir()
        git(self.server, "init", "--quiet")
        self.server_head = commit(self.server, {"go.mod": "module x\n"})
        self.manifest = base / "substrate-pins.yml"
        self.pin(first=True)
        patcher = mock.patch.object(substrate_pins, "PIN_FILE", str(self.manifest))
        patcher.start()
        self.addCleanup(patcher.stop)
        env = mock.patch.dict(os.environ)
        env.start()
        self.addCleanup(env.stop)
        os.environ.pop("RESTMAIL_SUBSTRATE", None)

    def pin(self, first):
        index = 0 if first else 1
        self.manifest.write_text(
            "".join(f"{name}: {pair[index]}\n" for name, pair in self.commits.items())
        )

    def sync(self, name, system="Linux"):
        pinned = substrate_pins.read_pins()
        return substrate_pins.sync(
            name, pinned[name], workspace=str(self.workspace),
            remote=str(self.upstream[name]), system=system, out=io.StringIO(),
        )

    def preflight(self, report=False, system="Linux"):
        out = io.StringIO()
        code = substrate_pins.preflight(
            report=report, root=str(self.server), workspace=str(self.workspace),
            system=system, out=out,
        )
        return code, out.getvalue()

    def sync_all(self, system="Linux"):
        for name in substrate_pins.REPOS:
            self.sync(name, system)

    def test_a_fresh_clone_is_checked_out_at_its_pin_and_prepared(self):
        self.sync_all()
        for name, (first, _) in self.commits.items():
            self.assertEqual(git(self.workspace / name, "rev-parse", "HEAD"), first)
        tasks = (self.workspace / "reference-mailserver/chores.yml").read_text()
        self.assertIn(substrate_pins.DKIM_CONTAINER_CHMOD, tasks)
        self.assertEqual(
            (self.workspace / "testbed/chores.yml").read_text(), TASKS
        )
        code, receipt = self.preflight()
        self.assertEqual(code, 0, receipt)
        self.assertIn(f"server                 {self.server_head}\n", receipt)
        for name, (first, _) in self.commits.items():
            self.assertRegex(receipt, rf"{name} +{first}  pin {first}  ok")
        self.assertIn("dkim:provision chmod in a container", receipt)

    def test_docker_desktop_needs_no_preparation(self):
        self.sync_all(system="Darwin")
        tasks = (self.workspace / "reference-mailserver/chores.yml").read_text()
        self.assertEqual(tasks, TASKS)
        self.assertEqual(self.preflight(system="Darwin")[0], 0)

    def test_sync_is_idempotent_and_moves_a_prepared_clone_to_a_new_pin(self):
        self.sync_all()
        self.sync_all()
        self.assertEqual(self.preflight()[0], 0)
        self.pin(first=False)
        code, receipt = self.preflight()
        self.assertEqual(code, 1)
        second = self.commits["testbed"][1]
        self.assertIn(f"FAIL: testbed is at {self.commits['testbed'][0]}, its pin is {second}", receipt)
        self.assertIn("run chore testbed:init", receipt)
        self.sync_all()
        for name, (_, second) in self.commits.items():
            self.assertEqual(git(self.workspace / name, "rev-parse", "HEAD"), second)
        self.assertEqual(self.preflight()[0], 0)

    def test_a_clone_on_a_branch_moves_to_the_pin_when_clean(self):
        self.sync_all()
        git(self.workspace / "testbed", "checkout", "--quiet", "-b", "work", self.commits["testbed"][1])
        self.assertEqual(self.preflight()[0], 1)
        self.sync("testbed")
        self.assertEqual(git(self.workspace / "testbed", "rev-parse", "HEAD"), self.commits["testbed"][0])

    def test_sync_refuses_to_move_over_uncommitted_work(self):
        self.sync_all()
        repo = self.workspace / "testbed"
        git(repo, "checkout", "--quiet", self.commits["testbed"][1])
        (repo / "README").write_text("mine\n")
        with self.assertRaisesRegex(RuntimeError, "uncommitted changes to README"):
            self.sync("testbed")
        self.assertEqual((repo / "README").read_text(), "mine\n")

    def test_preflight_fails_on_work_in_a_pinned_clone_and_an_unprepared_task_file(self):
        self.sync_all()
        (self.workspace / "testbed/README").write_text("mine\n")
        git(self.workspace / "reference-mailserver", "checkout", "--quiet", "--", "chores.yml")
        code, receipt = self.preflight()
        self.assertEqual(code, 1)
        self.assertIn("FAIL: testbed has uncommitted changes to README", receipt)
        self.assertIn("chores.yml is not prepared for native-Linux Docker", receipt)

    def test_a_missing_clone_names_its_init_task(self):
        self.sync("testbed")
        code, receipt = self.preflight()
        self.assertEqual(code, 1)
        self.assertRegex(receipt, r"reference-mailserver +not cloned")
        self.assertIn("FAIL: reference-mailserver is not cloned: run chore mailserver:init", receipt)

    def test_report_and_unpinned_runs_warn_instead_of_failing(self):
        self.sync("testbed")
        code, receipt = self.preflight(report=True)
        self.assertEqual(code, 0)
        self.assertIn("warning: reference-mailserver is not cloned", receipt)
        os.environ["RESTMAIL_SUBSTRATE"] = "unpinned"
        self.assertEqual(self.preflight()[0], 0)

    def test_unpinned_keeps_an_existing_checkout(self):
        self.sync_all()
        repo = self.workspace / "testbed"
        git(repo, "checkout", "--quiet", self.commits["testbed"][1])
        os.environ["RESTMAIL_SUBSTRATE"] = "unpinned"
        self.sync("testbed")
        self.assertEqual(git(repo, "rev-parse", "HEAD"), self.commits["testbed"][1])

    def test_the_receipt_marks_an_uncommitted_server(self):
        (self.server / "go.mod").write_text("module y\n")
        self.sync_all()
        code, receipt = self.preflight()
        self.assertEqual(code, 0, receipt)
        self.assertIn(f"{self.server_head} with uncommitted changes", receipt)

    def test_the_command_line_prints_a_pin(self):
        out = io.StringIO()
        with mock.patch("sys.stdout", out):
            self.assertEqual(substrate_pins.main(["ref", "testbed"]), 0)
        self.assertEqual(out.getvalue().strip(), self.commits["testbed"][0])
        with mock.patch("sys.stderr", io.StringIO()):
            self.assertEqual(substrate_pins.main(["ref", "website"]), 2)
            self.assertEqual(substrate_pins.main(["nonsense"]), 2)


if __name__ == "__main__":
    unittest.main()
