"""Website helpers stay inside the upstream working copy; no network or servers."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
HELPER = ROOT / "scripts/website.sh"
REAL_GIT = shutil.which("git")
UPSTREAM = "https://github.com/rest-mail/website"

STUB = r'''#!/usr/bin/env python3
import json, os, pathlib, sys
name = pathlib.Path(sys.argv[0]).name
args = sys.argv[1:]
with open(os.environ['CALLS'], 'a') as log:
    log.write(json.dumps([name, args, os.getcwd()]) + '\n')
if name == 'git' and args[:1] == ['-C']:
    args = args[2:]
if [name, args] == json.loads(os.environ.get('FAIL_CALL', 'null')):
    sys.exit(int(os.environ.get('FAIL_CODE', '37')))
if name == 'git':
    if args[:1] == ['clone']:
        pathlib.Path(args[-1]).mkdir(parents=True)
    elif args == ['rev-parse', '--show-toplevel']:
        print(pathlib.Path(os.environ['WEBSITE_DIR']).resolve())
    elif args[:2] == ['remote', 'get-url']:
        key = 'PUSH_URL' if '--push' in args else 'FETCH_URL'
        print(os.environ.get(key, 'https://github.com/rest-mail/website'))
    elif args[:1] == ['status']:
        print(os.environ.get('DIRTY', ''), end='')
    elif args[:1] == ['symbolic-ref']:
        print('feature/site')
    elif args[:1] == ['rev-list']:
        print(os.environ.get('AHEAD', '0'))
if name == 'editor':
    pathlib.Path(os.environ['EDITED']).write_text(json.dumps(sys.argv[1:]))
if name == 'docker':
    sys.exit(99)
'''


class Helpers(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="website-helper-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        for name in ("git", "npm", "editor", "docker"):
            path = self.bin / name
            path.write_text(STUB)
            path.chmod(0o755)
        self.copy = self.root / "website with spaces"
        self.copy.mkdir()
        self.calls = self.root / "calls"
        self.env = dict(os.environ, PATH=f"{self.bin}:{os.environ['PATH']}",
                        WEBSITE_DIR=str(self.copy), CALLS=str(self.calls),
                        EDITED=str(self.root / "edited"), VISUAL="", EDITOR="editor --wait",
                        MESSAGE="", DISCARD_LOCAL_WORK="")

    def run_helper(self, action, code=0, **env):
        result = subprocess.run([str(HELPER), action], cwd=ROOT,
                                env=dict(self.env, **env), text=True, capture_output=True)
        self.assertEqual(result.returncode, code, result.stdout + result.stderr)
        return result

    def logged(self, command=None):
        calls = [json.loads(line) for line in self.calls.read_text().splitlines()]
        if command:
            return [call[1] for call in calls if call[0] == command]
        return calls

    def git_calls(self):
        return [args[2:] if args[:1] == ["-C"] else args for args in self.logged("git")]

    def test_missing_copy_clones_master_and_runs_upstream_dev(self):
        self.copy.rmdir()
        self.run_helper("dev")
        self.assertEqual(self.git_calls()[0], ["clone", "--branch", "master",
                         "git@github.com:rest-mail/website.git", str(self.copy)])
        self.assertEqual(self.logged("npm"), [["ci"], ["run", "dev"]])
        self.assertTrue(all(call[2] == str(self.copy) for call in self.logged() if call[0] == "npm"))
        self.assertEqual(self.logged("docker"), [])

    def test_existing_copy_is_never_cloned_or_updated_for_dev(self):
        self.run_helper("dev", DIRTY=" M src/index.tsx\n")
        self.assertFalse(any(args[0] in ("clone", "fetch", "reset", "clean") for args in self.git_calls()))
        self.assertEqual(self.logged("npm"), [["ci"], ["run", "dev"]])

    def test_editor_receives_flags_and_one_path_argument(self):
        self.run_helper("edit")
        self.assertEqual(json.loads((self.root / "edited").read_text()), ["--wait", str(self.copy)])

    def test_visual_takes_precedence(self):
        self.run_helper("edit", VISUAL="editor --visual")
        self.assertEqual(json.loads((self.root / "edited").read_text()), ["--visual", str(self.copy)])

    def test_wrong_fetch_or_push_remote_refused_before_action(self):
        for action in ("init", "dev", "edit", "push", "reset"):
            for key in ("FETCH_URL", "PUSH_URL"):
                with self.subTest(action=action, key=key):
                    self.calls.write_text("")
                    self.run_helper(action, code=1, **{key: "https://github.com/other/website"})
                    self.assertFalse(any(args[0] in ("add", "commit", "push", "fetch", "reset", "clean") for args in self.git_calls()))
                    self.assertEqual(self.logged("npm"), [])
                    self.assertEqual(self.logged("editor"), [])

    def test_additional_push_destination_is_refused(self):
        self.run_helper("push", code=1, PUSH_URL=UPSTREAM + "\nhttps://github.com/other/website")
        self.assertFalse(any(args[0] == "push" for args in self.git_calls()))

    def test_commit_and_push_current_branch_with_literal_message(self):
        message = "fix: Bob's page; $(touch /do-not-execute)"
        self.run_helper("push", DIRTY="?? new-page\n", MESSAGE=message)
        self.assertEqual(self.git_calls()[-3:], [["add", "-A"], ["commit", "-m", message],
                                              ["push", "origin", "HEAD:refs/heads/feature/site"]])

    def test_clean_push_can_retry_an_existing_commit_without_message(self):
        self.run_helper("push")
        self.assertEqual(self.git_calls()[-1], ["push", "origin", "HEAD:refs/heads/feature/site"])
        self.assertFalse(any(args[0] in ("add", "commit") for args in self.git_calls()))

    def test_dirty_push_without_message_never_stages(self):
        self.run_helper("push", code=1, DIRTY=" M index.html\n")
        self.assertFalse(any(args[0] in ("add", "commit", "push") for args in self.git_calls()))

    def test_dirty_reset_refused_before_fetch(self):
        self.run_helper("reset", code=1, DIRTY="?? new-page\n")
        self.assertFalse(any(args[0] in ("fetch", "reset", "clean") for args in self.git_calls()))

    def test_local_commits_refused_after_fetch(self):
        self.run_helper("reset", code=1, AHEAD="2")
        self.assertIn(["fetch", "origin", "master"], self.git_calls())
        self.assertFalse(any(args[0] in ("reset", "clean") for args in self.git_calls()))

    def test_clean_reset_uses_fetched_master_and_keeps_untracked_files(self):
        self.run_helper("reset")
        self.assertEqual(self.git_calls()[-1], ["reset", "--hard", "FETCH_HEAD"])
        self.assertFalse(any(args[0] == "clean" for args in self.git_calls()))

    def test_explicit_reset_discards_files_and_commits_but_not_ignored_files(self):
        self.run_helper("reset", DIRTY=" M page\n", AHEAD="1", DISCARD_LOCAL_WORK="true")
        self.assertEqual(self.git_calls()[-2:], [["reset", "--hard", "FETCH_HEAD"], ["clean", "-fd"]])

    def test_exact_failures_propagate_and_later_steps_do_not_run(self):
        cases = [
            ("init", "git", ["rev-parse", "--show-toplevel"]),
            ("init", "git", ["remote", "get-url", "--all", "origin"]),
            ("init", "git", ["remote", "get-url", "--push", "--all", "origin"]),
            ("dev", "npm", ["ci"]),
            ("dev", "npm", ["run", "dev"]),
            ("edit", "editor", ["--wait", str(self.copy)]),
            ("push", "git", ["symbolic-ref", "--quiet", "--short", "HEAD"]),
            ("push", "git", ["status", "--porcelain", "--untracked-files=all"]),
            ("push", "git", ["add", "-A"]),
            ("push", "git", ["commit", "-m", "change"]),
            ("push", "git", ["push", "origin", "HEAD:refs/heads/feature/site"]),
            ("reset", "git", ["fetch", "origin", "master"]),
            ("reset", "git", ["rev-list", "--count", "FETCH_HEAD..HEAD"]),
            ("reset", "git", ["reset", "--hard", "FETCH_HEAD"]),
            ("reset", "git", ["clean", "-fd"]),
        ]
        for action, command, args in cases:
            with self.subTest(action=action, command=command, args=args):
                self.calls.write_text("")
                self.run_helper(action, code=37, FAIL_CALL=json.dumps([command, args]),
                                DIRTY=" M page\n", MESSAGE="change", DISCARD_LOCAL_WORK="true")
                last = self.logged()[-1]
                actual = last[1][2:] if command == "git" and last[1][:1] == ["-C"] else last[1]
                self.assertEqual([last[0], actual], [command, args])

    def test_clone_failure_is_preserved(self):
        self.copy.rmdir()
        self.run_helper("init", code=37, FAIL_CALL=json.dumps(["git", ["clone", "--branch", "master",
                        "git@github.com:rest-mail/website.git", str(self.copy)]]))

    @unittest.skipUnless(shutil.which("chore"), "Chore is needed for CLI integration")
    def test_chore_exports_message_and_concrete_discard_flag(self):
        chore = shutil.which("chore")
        for action, args in (("push", ["--message", "a literal ' message"]),
                             ("reset", ["--discard-local-work"])):
            with self.subTest(action=action):
                self.calls.write_text("")
                result = subprocess.run([chore, f"website:{action}", *args], cwd=ROOT,
                                        env=dict(self.env, DIRTY=" M page\n"), text=True, capture_output=True)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                if action == "push":
                    self.assertIn(["commit", "-m", "a literal ' message"], self.git_calls())
                else:
                    self.assertIn(["clean", "-fd"], self.git_calls())


class RealWorktree(unittest.TestCase):
    """Exercise actual git status/root handling without creating a new checkout."""
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="website-worktree-")
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name) / "working copy"
        # A detached worktree of the existing server repo tests real git
        # semantics. Only the remote lookup is stubbed; shared config is untouched.
        subprocess.run([REAL_GIT, "-C", str(ROOT), "worktree", "add", "--detach", str(self.path), "HEAD"],
                       check=True, capture_output=True)
        self.addCleanup(lambda: subprocess.run([REAL_GIT, "-C", str(ROOT), "worktree", "remove", "--force", str(self.path)],
                                              check=True, capture_output=True))
        bin_dir = Path(self.temp.name) / "bin"
        bin_dir.mkdir()
        wrapper = bin_dir / "git"
        wrapper.write_text("#!/usr/bin/env python3\nimport os, sys\n"
                           "if sys.argv[3:5] == ['remote', 'get-url']:\n"
                           f"    print({UPSTREAM!r})\n"
                           "elif sys.argv[3:] == ['fetch', 'origin', 'master']:\n"
                           "    import pathlib, subprocess\n"
                           f"    fetch_head = subprocess.check_output([{REAL_GIT!r}, '-C', sys.argv[2], 'rev-parse', '--git-path', 'FETCH_HEAD'], text=True).strip()\n"
                           "    pathlib.Path(fetch_head).write_text(os.environ['FETCH_TARGET'] + '\\n')\n"
                           "elif any(arg in ('fetch', 'push', 'clone') for arg in sys.argv[1:]):\n"
                           "    sys.exit(99)\n"
                           "else:\n"
                           f"    os.execv({REAL_GIT!r}, [{REAL_GIT!r}] + sys.argv[1:])\n")
        wrapper.chmod(0o755)
        self.env = dict(os.environ, WEBSITE_DIR=str(self.path),
                        PATH=f"{bin_dir}:{os.environ['PATH']}", DISCARD_LOCAL_WORK="",
                        FETCH_TARGET=subprocess.check_output([REAL_GIT, "-C", str(self.path), "rev-parse", "HEAD"], text=True).strip())

    def run_helper(self, action):
        return subprocess.run([str(HELPER), action], cwd=ROOT, env=self.env, text=True, capture_output=True)

    def test_actual_dirty_worktree_is_preserved_and_owner_is_untouched(self):
        tracked = self.path / "README.md"
        tracked.write_text(tracked.read_text() + "\nlocal edit\n")
        untracked = self.path / "unsaved-page.html"
        untracked.write_text("keep me")
        result = self.run_helper("reset")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("dirty working copy", result.stderr)
        self.assertTrue(tracked.read_text().endswith("local edit\n"))
        self.assertEqual(untracked.read_text(), "keep me")

    def test_actual_explicit_reset_discards_only_requested_work(self):
        original = (self.path / "README.md").read_text()
        (self.path / "README.md").write_text("local edit")
        untracked = self.path / "unsaved-page.html"
        untracked.write_text("delete me")
        ignored = self.path / ".env.local"
        ignored.write_text("keep me")
        self.env["DISCARD_LOCAL_WORK"] = "true"
        result = self.run_helper("reset")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual((self.path / "README.md").read_text(), original)
        self.assertFalse(untracked.exists())
        self.assertEqual(ignored.read_text(), "keep me")

    def test_actual_clean_local_commit_is_not_discarded(self):
        subprocess.run([REAL_GIT, "-C", str(self.path), "-c", "user.name=Website helper test",
                        "-c", "user.email=website-helper@example.invalid", "-c", "core.hooksPath=/dev/null",
                        "commit", "--allow-empty", "-m", "local work"], check=True, capture_output=True)
        before = subprocess.check_output([REAL_GIT, "-C", str(self.path), "rev-parse", "HEAD"], text=True)
        result = self.run_helper("reset")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("local commits would be lost", result.stderr)
        self.assertEqual(subprocess.check_output([REAL_GIT, "-C", str(self.path), "rev-parse", "HEAD"], text=True), before)

    def test_nested_directory_is_not_accepted_as_website_repo(self):
        nested = self.path / "not-a-repo"
        nested.mkdir()
        self.env["WEBSITE_DIR"] = str(nested)
        result = self.run_helper("init")
        self.assertEqual(result.returncode, 1)
        self.assertIn("not a repository root", result.stderr)

    def test_detached_push_refuses_before_staging(self):
        (self.path / "unsaved-page.html").write_text("keep me")
        result = self.run_helper("push")
        self.assertEqual(result.returncode, 1)
        staged = subprocess.check_output([REAL_GIT, "-C", str(self.path), "diff", "--cached", "--name-only"], text=True)
        self.assertEqual(staged, "")


if __name__ == "__main__":
    unittest.main()
