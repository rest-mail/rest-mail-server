"""First-start CI must provision its real authenticator tool without privilege."""

import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

import yaml


ROOT = Path(__file__).resolve().parents[2]


class WorkflowRootlessTools(unittest.TestCase):
    def test_first_start_installs_oathtool_without_sudo(self):
        document = yaml.safe_load((ROOT / ".github/workflows/ci.yml").read_text())
        steps = document["jobs"]["first-start"]["steps"]
        tools = next(step for step in steps if step.get("name") == "Tools")
        self.assertEqual(tools["run"], 'python3 scripts/ci/install_oathtool.py "$RUNNER_TEMP/oathtool"')
        setup = [step for step in steps[:steps.index(tools)]
                 if step.get("uses") == "./.github/actions/setup-python"]
        self.assertEqual(len(setup), 1)
        self.assertNotIn("if", setup[0])


class OathtoolInstallTests(unittest.TestCase):
    def setUp(self):
        spec = importlib.util.spec_from_file_location("install_oathtool", ROOT / "scripts/ci/install_oathtool.py")
        self.installer = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.installer)
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name).resolve()
        self.destination = self.directory / "user tools"
        self.github_path = self.directory / "github-path"
        self.github_env = self.directory / "github-env"
        self.environment = {**os.environ, "GITHUB_PATH": str(self.github_path), "GITHUB_ENV": str(self.github_env)}

    def test_signed_package_failure_stops_before_extraction_and_path_publication(self):
        with mock.patch.object(self.installer.shutil, "which", return_value=None), \
                mock.patch.object(self.installer.subprocess, "run", side_effect=subprocess.CalledProcessError(100, "apt-get")) as command:
            with self.assertRaises(subprocess.CalledProcessError):
                self.installer.install(self.destination, self.environment)
        self.assertEqual(command.call_count, 1)
        self.assertFalse(self.github_path.exists())
        self.assertFalse(self.github_env.exists())
        self.assertFalse(self.destination.exists())

    def test_dependency_versions_are_resolved_then_extracted_without_installing_packages(self):
        calls = []

        def execute(arguments, **kwargs):
            calls.append(arguments)
            if arguments[:2] == ["apt-get", "update"]:
                config = Path(kwargs["env"]["APT_CONFIG"]).read_text()
                self.assertIn(f'Dir::Etc::parts "{Path(kwargs["env"]["APT_CONFIG"]).parent}/config-parts";', config)
                self.assertIn('APT::Get::AllowUnauthenticated "false";', config)
                self.assertNotIn("/var/lib/apt", config)
            if "--simulate" in arguments:
                return subprocess.CompletedProcess(arguments, 0, stdout="Inst oathtool (2.6.12-1 test [amd64])\nInst liboath0 [old] (2.6.12-1 test [amd64])\n")
            if arguments[:2] == ["apt-get", "download"]:
                self.assertEqual(arguments[2:], ["oathtool=2.6.12-1", "liboath0=2.6.12-1"])
                (Path(kwargs["cwd"]) / "oathtool.deb").write_bytes(b"verified by apt")
            if arguments[:2] == ["dpkg-deb", "--extract"]:
                binary = self.destination / "usr/bin/oathtool"
                binary.parent.mkdir(parents=True)
                binary.write_text("fixture binary")
                (self.destination / "usr/lib/x86_64-linux-gnu").mkdir(parents=True)
            return subprocess.CompletedProcess(arguments, 0)

        with mock.patch.object(self.installer.shutil, "which", return_value=None), \
                mock.patch.object(self.installer.platform, "machine", return_value="x86_64"), \
                mock.patch.object(self.installer.subprocess, "run", side_effect=execute):
            self.installer.install(self.destination, self.environment)
        self.assertEqual(self.github_path.read_text(), f"{self.destination}/usr/bin\n")
        self.assertIn(str(self.destination / "usr/lib/x86_64-linux-gnu"), self.github_env.read_text())
        self.assertEqual(calls[-1], [str(self.destination / "usr/bin/oathtool"), "--version"])
        self.assertFalse(any("sudo" in command for command in calls))
        self.assertFalse(any(command[:2] == ["dpkg", "--install"] for command in calls))

    def test_malformed_simulation_is_not_treated_as_an_empty_install(self):
        with self.assertRaisesRegex(ValueError, "package plan"):
            self.installer.packages("Inst oathtool unexpected output\n")

    def test_unavailable_package_is_not_a_successful_bootstrap(self):
        with self.assertRaisesRegex(ValueError, "oathtool"):
            self.installer.packages("Conf something (1 test)\n")
