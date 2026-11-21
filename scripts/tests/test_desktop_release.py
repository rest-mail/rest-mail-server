"""Exercise desktop workflow boundaries and real release helpers with fixtures."""

import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest import mock
import zipfile

import yaml

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "desktop/scripts/release.py"
SPEC = importlib.util.spec_from_file_location("desktop_release", SCRIPT)
RELEASE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(RELEASE)


class DesktopWorkflowTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workflow = yaml.safe_load((ROOT / ".github/workflows/desktop.yml").read_text())
        cls.triggers = cls.workflow.get("on", cls.workflow.get(True))

    def test_path_filters_include_both_modules_and_no_server_tree(self):
        expected = {"desktop/**", "webmail/**", "tasks/desktop.yml", ".github/workflows/desktop.yml", "go.mod", "go.sum"}
        self.assertEqual(set(self.triggers), {"push", "pull_request", "workflow_dispatch"})
        self.assertEqual(self.triggers["push"]["branches"], ["main"])
        for event in ("push", "pull_request"):
            self.assertEqual(set(self.triggers[event]["paths"]), expected)

    def test_ci_never_creates_releases(self):
        self.assertNotIn("release", self.workflow["jobs"])
        self.assertNotIn("gh release create", json.dumps(self.workflow))
        self.assertEqual(set(self.workflow["jobs"]), {"macos", "windows", "linux-gate", "linux"})

    def test_no_job_can_write_contents(self):
        self.assertEqual(self.workflow["permissions"], {"contents": "read"})
        for name, job in self.workflow["jobs"].items():
            self.assertEqual(job.get("permissions", self.workflow["permissions"]), {"contents": "read"})
            self.assertNotIn("secrets.GITHUB_TOKEN", json.dumps(job))

    def test_all_actions_are_sha_pinned(self):
        for job in self.workflow["jobs"].values():
            for step in job["steps"]:
                if "uses" in step:
                    if step["uses"].startswith("./"):
                        action = yaml.safe_load((ROOT / step["uses"] / "action.yml").read_text())
                        for nested in action["runs"]["steps"]:
                            if "uses" in nested:
                                self.assertRegex(nested["uses"], r"^[\w/-]+@[0-9a-f]{40}$")
                    else:
                        self.assertRegex(step["uses"], r"^[\w/-]+@[0-9a-f]{40}$")

    def test_native_jobs_install_pinned_toolchain_and_keep_history(self):
        build = self.workflow["jobs"]["macos"]
        jobs = self.workflow["jobs"]
        for name, target, arch in (("macos", "darwin:package", "arm64"),
                                   ("windows", "windows:build", "amd64"),
                                   ("linux", "linux:build", "amd64")):
            job = jobs[name]
            self.assertEqual(job["env"]["BUILD_TARGET"], target)
            self.assertEqual(job["env"]["BUILD_ARCH"], arch)
            checkout = next(step for step in job["steps"] if step.get("uses", "").startswith("actions/checkout@"))
            self.assertEqual(checkout["with"]["fetch-depth"], 0)
        steps = build["steps"]
        setup = next(step for step in steps if step.get("uses", "").startswith("actions/setup-go@"))
        self.assertEqual(setup["with"]["go-version-file"], "desktop/go.mod")
        scripts = "\n".join(step.get("run", "") for step in steps)
        for expected in ("wails/v3/cmd/wails3@v3.1.0", "git rev-list --count HEAD",
                         "APP_VERSION=$APP_VERSION", "APP_COMMIT=$APP_COMMIT", "APP_BUILT=$APP_BUILT",
                         "GITHUB_STEP_SUMMARY", "libgtk-4-dev", "libwebkitgtk-6.0-dev"):
            self.assertIn(expected, scripts)

    def test_every_platform_uploads_its_archive(self):
        for name in ("macos", "windows", "linux"):
            upload = self.workflow["jobs"][name]["steps"][-1]
            self.assertTrue(upload["uses"].startswith("actions/upload-artifact@"))
            self.assertEqual(upload["with"], {"name": "desktop-${{ env.BUILD_PLATFORM }}",
                                               "path": "release-assets/*", "if-no-files-found": "error"})

    def test_only_named_macos_and_windows_jobs_are_directly_hosted(self):
        jobs = self.workflow["jobs"]
        self.assertEqual(jobs["macos"]["runs-on"], "macos-latest")
        self.assertEqual(jobs["windows"]["runs-on"], "windows-latest")
        self.assertEqual(jobs["linux-gate"]["runs-on"], "antimatter-vps-gate")
        self.assertEqual(jobs["linux"]["runs-on"], "${{ needs.linux-gate.outputs.runner }}")
        self.assertEqual(jobs["linux"]["needs"], "linux-gate")
        self.assertEqual(jobs["linux-gate"]["steps"], [{"id": "pick", "run": "runner-gate pick linux-x64"}])

    def test_all_platforms_embed_binary_metadata(self):
        for system in ("darwin", "linux", "windows"):
            taskfile = (ROOT / f"desktop/build/{system}/Taskfile.yml").read_text()
            for field in ("version={{.APP_VERSION}}", "commit={{.APP_COMMIT}}", "builtAt={{.APP_BUILT}}"):
                self.assertIn(f"-X main.{field}", taskfile)

    def test_local_and_ci_metadata_are_written_before_signing(self):
        tasks = yaml.safe_load((ROOT / "tasks/desktop.yml").read_text())["tasks"]
        commands = "\n".join(tasks["build"]["cmds"])
        self.assertLess(commands.index("Resources/release.json"), commands.index("codesign --force"))
        self.assertNotIn("Resources/release.json", "\n".join(str(item) for item in tasks["release"]["cmds"]))
        mac = next(step for step in self.workflow["jobs"]["macos"]["steps"] if step.get("name") == "Sign and zip macOS app")
        self.assertLess(mac["run"].index("Resources/release.json"), mac["run"].index("codesign --force"))


class ReleaseNotesTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.git("init", "-q")
        self.git("config", "user.email", "release@example.test")
        self.git("config", "user.name", "Release test")
        self.previous = self.commit("desktop/start.txt", "first desktop")
        self.git("tag", "desktop-v0.1.1")
        self.commit("server.txt", "server only")
        self.commit("webmail/mail.txt", "shared webmail")
        self.commit("desktop/next.txt", "next desktop")

    def git(self, *args):
        return subprocess.check_output(["git", *args], cwd=self.root, text=True, stderr=subprocess.PIPE).strip()

    def commit(self, name, message):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(message)
        self.git("add", name)
        self.git("commit", "-qm", message)
        return self.git("rev-parse", "HEAD")

    def run_tool(self, *args):
        return subprocess.run([os.sys.executable, str(SCRIPT), *args], cwd=self.root,
                              text=True, capture_output=True, check=False)

    def test_ci_notes_are_scoped_since_previous_desktop_tag(self):
        result = self.run_tool("notes")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("shared webmail", result.stdout)
        self.assertIn("next desktop", result.stdout)
        self.assertNotIn("server only", result.stdout)
        self.assertNotIn("first desktop", result.stdout)

    def test_first_release_lists_all_scoped_history(self):
        self.git("tag", "-d", "desktop-v0.1.1")
        self.git("tag", "v99.0.0")
        result = self.run_tool("notes")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("first desktop", result.stdout)
        self.assertNotIn("server only", result.stdout)

    def test_installed_dirty_commit_is_compared_with_head(self):
        installed = self.root / "installed"
        manifest = installed / "RESTMail.app/Contents/Resources/release.json"
        manifest.parent.mkdir(parents=True)
        manifest.write_text(json.dumps({"version": "0.1.1-dirty", "commit": self.previous + "-dirty"}))
        result = self.run_tool("installed-notes", "--install-dir", str(installed))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("shared webmail", result.stdout)
        self.assertNotIn("first desktop", result.stdout)

    def test_missing_install_has_an_explicit_message(self):
        result = self.run_tool("installed-notes", "--install-dir", str(self.root / "absent"))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("No installed release", result.stdout)

    def test_invalid_installed_commit_fails_without_treating_it_as_a_git_option(self):
        manifest = self.root / "RESTMail.app/Contents/Resources/release.json"
        manifest.parent.mkdir(parents=True)
        manifest.write_text(json.dumps({"commit": "--all"}))
        result = self.run_tool("installed-notes", "--install-dir", str(self.root))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("no valid Git commit", result.stderr)

    def test_stamp_round_trips_metadata(self):
        path = self.root / "bin/release.json"
        result = self.run_tool("stamp", "--version", "0.1.12-dirty", "--commit", self.previous,
                               "--built", "2026-11-21T19:00:00Z", "--output", str(path))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(path.read_text()), {"app": "REST Mail", "version": "0.1.12-dirty",
                                                       "commit": self.previous, "builtAt": "2026-11-21T19:00:00Z"})

    def test_dirty_detection_includes_untracked_desktop_files_but_not_server_files(self):
        expression = yaml.safe_load((ROOT / "tasks/desktop.yml").read_text())["vars"]["APP_DIRTY"]["sh"]
        def dirty():
            return subprocess.check_output(["sh", "-c", expression], cwd=self.root, text=True).strip()
        self.assertEqual(dirty(), "")
        (self.root / "untracked-server.txt").write_text("server")
        self.assertEqual(dirty(), "")
        (self.root / "desktop/untracked.txt").write_text("desktop")
        self.assertEqual(dirty(), "-dirty")
        self.git("add", "desktop/untracked.txt")
        self.assertEqual(dirty(), "-dirty")

    def publish_fixture(self, remote=True, existing=False, dirty=False):
        build = self.root / "desktop/bin"
        resources = build / "RESTMail.app/Contents/Resources"
        resources.mkdir(parents=True)
        metadata = {"version": "0.1.12" + ("-dirty" if dirty else ""),
                    "commit": self.git("rev-parse", "HEAD"), "builtAt": "2026-11-21T19:00:00Z"}
        for path in (build / "release.json", resources / "release.json"):
            path.write_text(json.dumps(metadata))
        if remote:
            origin = self.root / "origin.git"
            self.git("clone", "--quiet", "--bare", str(self.root), str(origin))
            self.git("remote", "add", "origin", str(origin))
        tools = self.root / "tools"
        tools.mkdir()
        capture = self.root / "calls.jsonl"
        fake = '''#!/usr/bin/env python3
import json, os, pathlib, sys, zipfile
name = pathlib.Path(sys.argv[0]).name
args = sys.argv[1:]
record = {"tool": name, "args": args}
if name == "gh" and args[0] == "api":
    print(os.environ["FAKE_RELEASES"])
elif name == "gh" and args[:2] == ["release", "create"]:
    record["notes"] = pathlib.Path(args[args.index("--notes-file") + 1]).read_text()
    record["metadata"] = json.loads(pathlib.Path(args[4]).read_text())
    with zipfile.ZipFile(args[3]) as archive:
        record["entries"] = archive.namelist()
elif name == "ditto":
    bundle = pathlib.Path(args[-2])
    with zipfile.ZipFile(args[-1], "w") as archive:
        for source in bundle.rglob("*"):
            if source.is_file():
                archive.write(source, str(source.relative_to(bundle.parent)))
elif name != "codesign":
    raise RuntimeError("Unexpected command")
with open(os.environ["CAPTURE"], "a") as output:
    output.write(json.dumps(record) + "\\n")
'''
        for name in ("gh", "ditto", "codesign"):
            executable = tools / name
            executable.write_text(fake)
            executable.chmod(0o755)
        env = dict(os.environ, PATH=str(tools) + os.pathsep + os.environ["PATH"], CAPTURE=str(capture),
                   FAKE_RELEASES=json.dumps([[{"tag_name": "desktop-v0.1.12"}] if existing else []]))
        return build, capture, env, metadata

    def publish_tool(self, build, env):
        return subprocess.run([os.sys.executable, str(SCRIPT), "publish", "--build-dir", str(build)],
                              cwd=self.root, env=env, text=True, capture_output=True, check=False)

    def test_publish_uses_last_build_commit_and_attaches_zip_manifest_and_scoped_notes(self):
        build, capture, env, metadata = self.publish_fixture()
        self.commit("desktop/later.txt", "not in the built version")
        result = self.publish_tool(build, env)
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = [json.loads(line) for line in capture.read_text().splitlines()]
        publishing = next(call for call in calls if call["args"][:2] == ["release", "create"])
        args = publishing["args"]
        self.assertEqual(args[2], "desktop-v0.1.12")
        self.assertEqual(args[args.index("--target") + 1], metadata["commit"])
        self.assertEqual(args[args.index("--title") + 1], "REST Mail desktop 0.1.12")
        self.assertEqual(publishing["metadata"], metadata)
        self.assertIn("RESTMail.app/Contents/Resources/release.json", publishing["entries"])
        self.assertIn("shared webmail", publishing["notes"])
        self.assertNotIn("first desktop", publishing["notes"])
        self.assertNotIn("not in the built version", publishing["notes"])
        self.assertNotIn("server only", publishing["notes"])
        self.assertEqual(next(call for call in calls if call["tool"] == "ditto")["args"][:3],
                         ["-c", "-k", "--keepParent"])

    def test_publish_refuses_dirty_build_without_contacting_github(self):
        build, capture, env, metadata = self.publish_fixture(dirty=True)
        result = self.publish_tool(build, env)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("dirty build", result.stderr)
        self.assertFalse(capture.exists())

    def test_publish_refuses_unpushed_commit_and_prints_push_command(self):
        build, capture, env, metadata = self.publish_fixture(remote=False)
        result = self.publish_tool(build, env)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(f"git push origin {metadata['commit']}:refs/heads/desktop-release-0.1.12", result.stderr)
        self.assertFalse(capture.exists())

    def test_publish_refuses_existing_release_without_packaging_or_overwriting(self):
        build, capture, env, metadata = self.publish_fixture(existing=True)
        result = self.publish_tool(build, env)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("already exists", result.stderr)
        calls = [json.loads(line) for line in capture.read_text().splitlines()]
        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0]["args"][0], "api")


class LatestReleaseTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.installed = self.root / "Applications"
        self.old = self.installed / "RESTMail.app/old.txt"
        self.old.parent.mkdir(parents=True)
        self.old.write_text("preserve until verified")
        self.archive = self.root / "fixture.zip"
        self.calls = []
        self.metadata = {"version": "0.1.12", "commit": "abc1234", "builtAt": "2026-11-21T19:00:00Z"}
        self.make_archive()
        releases = [[{"tag_name": "v99", "draft": False, "prerelease": False, "published_at": "2026-11-22"},
                     {"tag_name": "desktop-v0.1.13", "draft": True, "prerelease": False, "published_at": "2026-11-22"}],
                    [{"tag_name": "desktop-v0.1.12", "draft": False, "prerelease": False, "published_at": "2026-11-21"},
                     {"tag_name": "desktop-v0.1.11", "draft": False, "prerelease": False, "published_at": "2026-11-20"}]]
        for patcher in (mock.patch.object(RELEASE.platform, "system", return_value="Darwin"),
                        mock.patch.object(RELEASE.platform, "machine", return_value="arm64"),
                        mock.patch.object(RELEASE, "output", return_value=json.dumps(releases)),
                        mock.patch.object(RELEASE.subprocess, "run", side_effect=self.command)):
            patcher.start()
            self.addCleanup(patcher.stop)

    def make_archive(self, extra=None):
        with zipfile.ZipFile(self.archive, "w") as package:
            package.writestr("RESTMail.app/Contents/Resources/release.json", json.dumps(self.metadata))
            package.writestr("RESTMail.app/Contents/MacOS/RESTMail", "fixture binary")
            if extra:
                package.writestr(extra, "unsafe")

    def command(self, command, check):
        self.assertTrue(check)
        self.calls.append(command)
        self.assertTrue(self.old.is_file(), "old installation must survive until verification")
        if command[:2] == ["gh", "release"]:
            self.assertEqual(command[2:6], ["download", "desktop-v0.1.12", "--pattern", "RESTMail-0.1.12-darwin-arm64.zip"])
            shutil.copyfile(self.archive, Path(command[-1]) / command[5])
        elif command[0] == "ditto":
            with zipfile.ZipFile(command[3]) as package:
                package.extractall(command[4])
        elif command[0] != "codesign":
            self.fail(f"Unexpected command: {command}")

    def test_downloads_newest_desktop_release_across_pages_and_installs_to_scratch(self):
        with contextlib.redirect_stdout(io.StringIO()) as captured:
            RELEASE.latest(self.installed)
        self.assertFalse(self.old.exists())
        self.assertEqual(json.loads((self.old.parent / "Contents/Resources/release.json").read_text()), self.metadata)
        self.assertEqual(self.calls[-1][:4], ["codesign", "--verify", "--deep", "--strict"])
        self.assertIn('"version": "0.1.12"', captured.getvalue())
        self.assertIn('"commit": "abc1234"', captured.getvalue())

    def test_rejects_unsafe_zip_without_replacing_installation(self):
        self.make_archive("../escape")
        with self.assertRaisesRegex(ValueError, "unsafe path"):
            RELEASE.latest(self.installed)
        self.assertTrue(self.old.exists())

    def test_rejects_mismatched_metadata_without_replacing_installation(self):
        self.metadata["version"] = "0.1.1"
        self.make_archive()
        with self.assertRaisesRegex(ValueError, "does not match"):
            RELEASE.latest(self.installed)
        self.assertTrue(self.old.exists())

    def test_intel_mac_fails_before_downloading(self):
        with mock.patch.object(RELEASE.platform, "machine", return_value="x86_64"):
            with self.assertRaisesRegex(ValueError, "Apple Silicon"):
                RELEASE.latest(self.installed)
        self.assertEqual(self.calls, [])


if __name__ == "__main__":
    unittest.main()
