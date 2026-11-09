"""Per-run scratch cleanup refuses anything but an owned, completed run (#2065).

The incident: a finished worker ran `rm -rf` on the shared scratch root and removed
other workers' gates, handoff file and key. These tests build disposable fixtures
that look like that shared root and prove the supported helper cannot do it.
"""

import contextlib
import importlib.util
import io
import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("scratch_cleanup", ROOT / "scripts/scratch_cleanup.py")
scratch_cleanup = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(scratch_cleanup)

ME = "worker-2065"
OTHER = "worker-2047"


class Fixture(unittest.TestCase):
    """A shared root with a handoff file, a key, and two other workers' runs."""

    def setUp(self):
        self._temp = tempfile.TemporaryDirectory(prefix="scratch-cleanup-test.")
        self.addCleanup(self._temp.cleanup)
        self.base = Path(self._temp.name).resolve()
        self.root = self.base / "rest-mail-loop"
        self.root.mkdir()
        (self.root / "handoff.md").write_text("handoff")
        (self.root / "github-git.pub").write_text("key")
        # A bystander outside the shared root altogether.
        self.outside = self.base / "outside"
        self.outside.mkdir()
        (self.outside / "precious").write_text("keep")

    def run_dir(self, run_id=ME + "-run", owner=ME, complete=True, files=("gate.log",)):
        path = scratch_cleanup.create_run(self.root, run_id, owner)
        for name in files:
            (path / name).write_text("data")
        if complete:
            scratch_cleanup.complete_run(self.root, run_id, owner)
        return path

    def assertRefused(self, *args, **kwargs):
        with self.assertRaises(scratch_cleanup.Refused):
            scratch_cleanup.cleanup_run(*args, **kwargs)

    def assertShared(self):
        """The shared root and everything the incident destroyed still exist."""
        self.assertTrue(self.root.is_dir())
        self.assertEqual((self.root / "handoff.md").read_text(), "handoff")
        self.assertEqual((self.root / "github-git.pub").read_text(), "key")
        self.assertEqual((self.outside / "precious").read_text(), "keep")


class CleansOwnedRun(Fixture):
    def test_completed_owned_run_is_removed_and_nothing_else(self):
        mine = self.run_dir()
        sibling = self.run_dir("worker-other-run", OTHER)
        scratch_cleanup.cleanup_run(self.root, ME + "-run", ME)
        self.assertFalse(mine.exists())
        self.assertTrue(sibling.is_dir())
        self.assertTrue((sibling / "gate.log").exists())
        self.assertShared()

    def test_nested_content_and_inner_symlinks_are_removed_without_following(self):
        mine = self.run_dir(files=())
        (mine / "tree" / "deep").mkdir(parents=True)
        (mine / "tree" / "deep" / "file").write_text("x")
        os.symlink(self.outside, mine / "link-to-outside")
        os.symlink(self.outside / "precious", mine / "link-to-file")
        scratch_cleanup.cleanup_run(self.root, ME + "-run", ME)
        self.assertFalse(mine.exists())
        self.assertShared()

    def test_check_and_list_remove_nothing(self):
        mine = self.run_dir()
        scratch_cleanup.check_removable(self.root, ME + "-run", ME)
        listing = scratch_cleanup.list_runs(self.root)
        self.assertTrue(mine.is_dir())
        by_id = {entry["run_id"]: entry for entry in listing}
        self.assertEqual(by_id[ME + "-run"]["status"], "complete")
        self.assertEqual(by_id[ME + "-run"]["owner"], ME)
        # Plain files in the shared root are listed as not removable runs, never as candidates.
        self.assertEqual(by_id["handoff.md"]["status"], "unowned")
        self.assertEqual(by_id["github-git.pub"]["status"], "unowned")
        self.assertShared()

    def test_dry_run_command_removes_nothing(self):
        mine = self.run_dir()
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            code = scratch_cleanup.main(["clean", "--root", str(self.root), "--run-id", ME + "-run", "--owner", ME, "--dry-run"])
        self.assertEqual(code, 0)
        self.assertTrue(mine.is_dir())
        self.assertIn("would remove", out.getvalue())


class RefusesSharedRootAndSiblings(Fixture):
    """The incident itself: aim the cleanup at the shared root or beside the run."""

    def test_shared_root_is_never_a_run(self):
        # Even a root carrying a valid completed marker of its own is not removable.
        marker = scratch_cleanup.marker_path(self.root)
        marker.write_text(
            '{"version": 1, "run_id": "rest-mail-loop", "owner": "%s", "status": "complete", "root": "%s"}'
            % (ME, self.root)
        )
        for run_id in (".", "", "..", "rest-mail-loop", str(self.root)):
            with self.subTest(run_id=run_id):
                self.assertRefused(self.root, run_id, ME)
        self.assertShared()

    def test_files_and_unmarked_siblings_are_refused(self):
        unmarked = self.root / "1857-live"
        unmarked.mkdir()
        (unmarked / "gate.log").write_text("live gate")
        for run_id in ("handoff.md", "github-git.pub", "1857-live", "missing-run"):
            with self.subTest(run_id=run_id):
                self.assertRefused(self.root, run_id, ME)
        self.assertTrue((unmarked / "gate.log").exists())
        self.assertShared()

    def test_a_common_prefix_does_not_confer_ownership(self):
        mine = self.run_dir()
        neighbour = self.root / (ME + "-run-other")
        neighbour.mkdir()
        (neighbour / "gate.log").write_text("not mine")
        self.assertRefused(self.root, ME + "-run-other", ME)
        self.assertRefused(self.root, ME, ME)
        self.assertTrue(neighbour.is_dir())
        self.assertTrue(mine.is_dir())

    def test_root_that_is_unsafe_is_refused(self):
        self.run_dir()
        home = Path(os.path.expanduser("~")).resolve()
        for root in (Path("/"), home, home.parent, self.root / "missing"):
            with self.subTest(root=str(root)):
                self.assertRefused(root, ME + "-run", ME)
        self.assertRefused("relative/root", ME + "-run", ME)
        self.assertShared()

    def test_home_and_its_parent_are_refused_even_with_a_valid_run_inside(self):
        home = self.base / "home"
        home.mkdir()
        for root in (home, self.base):
            run = root / "owned-run"
            run.mkdir(exist_ok=True)
            scratch_cleanup.write_marker(run, run_id="owned-run", owner=ME, status="complete", root=root)
        with mock.patch.dict(os.environ, {"HOME": str(home)}):
            self.assertRefused(home, "owned-run", ME)
            self.assertRefused(self.base, "owned-run", ME)
            # A real root below home is still fine.
            scratch_cleanup.cleanup_run(self.root, self.run_dir().name, ME)
        self.assertTrue((home / "owned-run").is_dir())
        self.assertTrue((self.base / "owned-run").is_dir())
        self.assertShared()

    def test_root_that_is_a_symlink_is_refused(self):
        self.run_dir()
        link = self.base / "root-link"
        os.symlink(self.root, link)
        self.assertRefused(link, ME + "-run", ME)
        self.assertShared()
        self.assertTrue((self.root / (ME + "-run")).is_dir())


class RefusesTraversalAndSymlinks(Fixture):
    def test_run_ids_that_leave_the_root_are_refused(self):
        self.run_dir()
        victim = self.base / "victim"
        victim.mkdir()
        (victim / "gate.log").write_text("x")
        scratch_cleanup.write_marker(victim, run_id="victim", owner=ME, status="complete", root=self.base)
        for run_id in (
            "../victim",
            "../outside",
            ME + "-run/..",
            ME + "-run/../" + ME + "-run",
            "sub/" + ME + "-run",
            str(victim),
            "-rf",
            "run\x00id",
            "run id",
        ):
            with self.subTest(run_id=run_id):
                self.assertRefused(self.root, run_id, ME)
        self.assertTrue((victim / "gate.log").exists())
        self.assertShared()

    def test_run_directory_that_is_a_symlink_to_an_owned_looking_directory(self):
        # The target outside the root carries a perfect completed marker for this owner.
        victim = self.base / "victim"
        victim.mkdir()
        (victim / "gate.log").write_text("x")
        scratch_cleanup.write_marker(victim, run_id="escape", owner=ME, status="complete", root=self.root)
        os.symlink(victim, self.root / "escape")
        self.assertRefused(self.root, "escape", ME)
        self.assertTrue((victim / "gate.log").exists())
        self.assertTrue(os.path.islink(self.root / "escape"))
        self.assertShared()

    def test_symlink_to_the_shared_root_or_a_sibling_is_refused(self):
        sibling = self.run_dir("worker-other-run", OTHER)
        os.symlink(self.root, self.root / "to-root")
        os.symlink(sibling, self.root / "to-sibling")
        for run_id in ("to-root", "to-sibling"):
            with self.subTest(run_id=run_id):
                self.assertRefused(self.root, run_id, ME)
                self.assertRefused(self.root, run_id, OTHER)
        self.assertTrue((sibling / "gate.log").exists())
        self.assertShared()

    def test_marker_that_is_a_symlink_to_a_valid_marker_is_refused(self):
        donor = self.run_dir("donor-run", ME)
        victim = self.root / "victim-run"
        victim.mkdir()
        (victim / "gate.log").write_text("x")
        os.symlink(scratch_cleanup.marker_path(donor), scratch_cleanup.marker_path(victim))
        self.assertRefused(self.root, "victim-run", ME)
        self.assertTrue((victim / "gate.log").exists())

    def test_marker_copied_from_another_run_is_refused(self):
        donor = self.run_dir("donor-run", ME)
        victim = self.root / "victim-run"
        victim.mkdir()
        (victim / "gate.log").write_text("x")
        scratch_cleanup.marker_path(victim).write_bytes(scratch_cleanup.marker_path(donor).read_bytes())
        self.assertRefused(self.root, "victim-run", ME)
        self.assertTrue((victim / "gate.log").exists())

    def test_marker_that_names_another_root_is_refused(self):
        run = self.run_dir()
        other_root = self.base / "other-root"
        other_root.mkdir()
        scratch_cleanup.write_marker(run, run_id=ME + "-run", owner=ME, status="complete", root=other_root)
        self.assertRefused(self.root, ME + "-run", ME)
        self.assertTrue(run.is_dir())


class RefusesMissingOrWrongOwnership(Fixture):
    def test_missing_marker(self):
        run = self.root / (ME + "-run")
        run.mkdir()
        (run / "gate.log").write_text("x")
        self.assertRefused(self.root, ME + "-run", ME)
        self.assertTrue(run.is_dir())

    def test_wrong_owner(self):
        run = self.run_dir("theirs-run", OTHER)
        self.assertRefused(self.root, "theirs-run", ME)
        self.assertTrue((run / "gate.log").exists())

    def test_empty_or_missing_caller_owner_is_refused(self):
        run = self.run_dir()
        for owner in ("", None, "  "):
            with self.subTest(owner=owner):
                self.assertRefused(self.root, ME + "-run", owner)
        self.assertTrue(run.is_dir())

    def test_owner_is_compared_exactly_not_by_prefix(self):
        run = self.run_dir(owner=ME + "-extra")
        for owner in (ME, ME + "-ext", ME + "-extra2", (ME + "-extra").upper()):
            with self.subTest(owner=owner):
                self.assertRefused(self.root, ME + "-run", owner)
        self.assertTrue(run.is_dir())

    def test_malformed_markers(self):
        for body in ("", "not json", "[]", '{"version": 1}', '{"version": 2, "run_id": "x"}', "\x00" * 10):
            with self.subTest(body=body):
                run = self.root / "bad-run"
                run.mkdir(exist_ok=True)
                (run / "gate.log").write_text("x")
                scratch_cleanup.marker_path(run).write_text(body)
                self.assertRefused(self.root, "bad-run", ME)
                self.assertTrue((run / "gate.log").exists())

    def test_marker_larger_than_a_marker_can_be_is_refused(self):
        run = self.root / "bad-run"
        run.mkdir()
        scratch_cleanup.marker_path(run).write_text(" " * 100_000)
        self.assertRefused(self.root, "bad-run", ME)
        self.assertTrue(run.is_dir())


class RefusesActiveRuns(Fixture):
    def test_active_foreign_run_is_refused_for_everyone(self):
        run = self.run_dir("theirs-run", OTHER, complete=False)
        for owner in (ME, OTHER, "", "root"):
            with self.subTest(owner=owner):
                self.assertRefused(self.root, "theirs-run", owner)
        self.assertTrue((run / "gate.log").exists())
        self.assertShared()

    def test_active_own_run_is_refused_until_it_is_completed(self):
        run = self.run_dir(complete=False)
        self.assertRefused(self.root, ME + "-run", ME)
        self.assertTrue(run.is_dir())
        scratch_cleanup.complete_run(self.root, ME + "-run", ME)
        scratch_cleanup.cleanup_run(self.root, ME + "-run", ME)
        self.assertFalse(run.exists())

    def test_only_the_owner_can_complete_a_run(self):
        self.run_dir(complete=False)
        with self.assertRaises(scratch_cleanup.Refused):
            scratch_cleanup.complete_run(self.root, ME + "-run", OTHER)
        self.assertRefused(self.root, ME + "-run", OTHER)
        self.assertRefused(self.root, ME + "-run", ME)

    def test_unknown_status_is_treated_as_active(self):
        run = self.run_dir(complete=False)
        scratch_cleanup.write_marker(run, run_id=ME + "-run", owner=ME, status="finished-ish", root=self.root)
        self.assertRefused(self.root, ME + "-run", ME)
        self.assertTrue(run.is_dir())

    def test_a_run_cannot_be_created_over_an_existing_directory(self):
        self.run_dir()
        with self.assertRaises(scratch_cleanup.Refused):
            scratch_cleanup.create_run(self.root, ME + "-run", OTHER)
        with self.assertRaises(scratch_cleanup.Refused):
            scratch_cleanup.create_run(self.root, "handoff.md", OTHER)
        self.assertEqual((self.root / "handoff.md").read_text(), "handoff")
        self.assertTrue((self.root / (ME + "-run") / "gate.log").exists())

    def test_incident_scenario_end_to_end(self):
        """A completed worker cleans up while other gates run and the shared files sit beside it."""
        finished = self.run_dir("2047-repair", "worker-2047")
        gate = self.run_dir("2064-gate", "worker-2064", complete=False)
        live = self.run_dir("1857-live", "worker-1857", complete=False)
        # What it did: aim at the shared root, then at every neighbour.
        for run_id in ("", ".", "2064-gate", "1857-live", "handoff.md", "github-git.pub", "../rest-mail-loop"):
            self.assertRefused(self.root, run_id, "worker-2047")
        scratch_cleanup.cleanup_run(self.root, "2047-repair", "worker-2047")
        self.assertFalse(finished.exists())
        self.assertTrue((gate / "gate.log").exists())
        self.assertTrue((live / "gate.log").exists())
        self.assertShared()


class CommandLine(Fixture):
    def call(self, *argv):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = scratch_cleanup.main(list(argv))
        return code, out.getvalue(), err.getvalue()

    def test_lifecycle(self):
        base = ("--root", str(self.root), "--run-id", "cli-run", "--owner", ME)
        self.assertEqual(self.call("create", *base)[0], 0)
        code, _, err = self.call("clean", *base)
        self.assertEqual(code, scratch_cleanup.REFUSED_EXIT)
        self.assertIn("refused", err)
        self.assertTrue((self.root / "cli-run").is_dir())
        self.assertEqual(self.call("complete", *base)[0], 0)
        self.assertEqual(self.call("clean", *base)[0], 0)
        self.assertFalse((self.root / "cli-run").exists())
        self.assertShared()

    def test_owner_and_run_id_are_required_never_inferred(self):
        self.run_dir()
        env = {"RESTMAIL_RUN_OWNER": ME, "USER": ME}
        old = {key: os.environ.get(key) for key in env}
        os.environ.update(env)
        self.addCleanup(lambda: [os.environ.pop(k, None) if v is None else os.environ.__setitem__(k, v) for k, v in old.items()])
        with self.assertRaises(SystemExit):
            self.call("clean", "--root", str(self.root), "--run-id", ME + "-run")
        with self.assertRaises(SystemExit):
            self.call("clean", "--root", str(self.root), "--owner", ME)
        self.assertTrue((self.root / (ME + "-run")).is_dir())

    def test_list_removes_nothing_and_names_each_entry(self):
        self.run_dir()
        code, out, _ = self.call("list", "--root", str(self.root))
        self.assertEqual(code, 0)
        self.assertIn(ME + "-run", out)
        self.assertIn("handoff.md", out)
        self.assertTrue((self.root / (ME + "-run")).is_dir())
        self.assertShared()


if __name__ == "__main__":
    unittest.main()
