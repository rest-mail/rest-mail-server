"""Race jobs need a working non-root compiler, not just CGO_ENABLED=1."""

import hashlib
import importlib.util
import io
import os
from pathlib import Path
import shlex
import subprocess
import tarfile
import tempfile
import unittest
from unittest import mock

import yaml


ROOT = Path(__file__).resolve().parents[2]


class WorkflowRaceCompiler(unittest.TestCase):
    def test_every_race_job_bootstraps_a_compiler_and_python_before_race(self):
        document = yaml.safe_load((ROOT / ".github/workflows/ci.yml").read_text())
        jobs = 0
        for name, job in document["jobs"].items():
            steps = job.get("steps", [])
            race = next((index for index, step in enumerate(steps) if "-race" in step.get("run", "")), None)
            if race is None:
                continue
            jobs += 1
            with self.subTest(job=name):
                setup = [index for index, step in enumerate(steps[:race])
                         if step.get("uses") == "./.github/actions/setup-cgo"]
                self.assertEqual(len(setup), 1)
                self.assertNotIn("if", steps[setup[0]])
                before = steps[:setup[0]]
                self.assertTrue(any(step.get("uses") == "./.github/actions/setup-python" for step in before))
                self.assertTrue(any(step.get("uses", "").startswith("actions/setup-go@") for step in before))
        self.assertEqual(jobs, 6)


class CgoInstallTests(unittest.TestCase):
    def setUp(self):
        spec = importlib.util.spec_from_file_location("install_cgo", ROOT / "scripts/ci/install_cgo.py")
        self.installer = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.installer)
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)

    def test_an_existing_compiler_is_probed_and_cgo_is_enabled(self):
        with mock.patch.object(self.installer.shutil, "which", return_value="/tools/clang"), \
                mock.patch.object(self.installer, "probe") as probe, \
                mock.patch.object(self.installer, "download_zig") as download:
            environment = self.installer.configure(self.directory, {"PATH": "/tools", "CGO_ENABLED": "0"})
        self.assertEqual(environment, {"CC": "/tools/clang", "CGO_ENABLED": "1"})
        self.assertEqual(probe.call_args.args[0], ["/tools/clang"])
        download.assert_not_called()

    def test_a_broken_explicit_compiler_is_not_silently_replaced(self):
        with mock.patch.object(self.installer.shutil, "which", return_value=None), \
                mock.patch.object(self.installer, "download_zig") as download:
            with self.assertRaisesRegex(ValueError, "explicit CC"):
                self.installer.configure(self.directory, {"CC": "missing-gcc", "PATH": "/tools"})
        download.assert_not_called()

    def test_an_unusable_compiler_fails_before_environment_is_published(self):
        with mock.patch.object(self.installer.shutil, "which", return_value="/tools/cc"), \
                mock.patch.object(self.installer, "probe", side_effect=subprocess.CalledProcessError(1, "cc")):
            with self.assertRaises(subprocess.CalledProcessError):
                self.installer.configure(self.directory, {"PATH": "/tools"})

    def test_a_compiler_path_with_spaces_is_preserved_as_one_argument(self):
        compiler = "/user tools/bin/cc"
        with mock.patch.object(self.installer.shutil, "which", return_value=compiler), \
                mock.patch.object(self.installer, "probe") as probe:
            environment = self.installer.configure(self.directory, {"CC": shlex.quote(compiler)})
        self.assertEqual(shlex.split(environment["CC"]), [compiler])
        self.assertEqual(probe.call_args.args[0], [compiler])

    def test_a_checksum_mismatch_is_never_extracted(self):
        with mock.patch.object(self.installer, "fetch", return_value=b"corrupt archive"), \
                mock.patch.object(self.installer.tarfile, "open") as extract:
            with self.assertRaisesRegex(ValueError, "checksum"):
                self.installer.download_zig(self.directory, "x86_64", {})
        extract.assert_not_called()

    def test_archive_cache_is_verified_before_reuse_or_replaced_from_download(self):
        contents = io.BytesIO()
        executable = f"zig-x86_64-linux-{self.installer.VERSION}/zig"
        with tarfile.open(fileobj=contents, mode="w:xz") as archive:
            entry = tarfile.TarInfo(executable)
            entry.mode = 0o755
            entry.size = 4
            archive.addfile(entry, io.BytesIO(b"tool"))
        payload = contents.getvalue()
        pin = {"sha256": hashlib.sha256(payload).hexdigest(), "url": "https://example.invalid/zig.tar.xz"}
        for cache_state in ("missing", "valid", "corrupt"):
            with self.subTest(cache=cache_state):
                directory = self.directory / cache_state
                cache = directory / "cache"
                cache.mkdir(parents=True)
                cached = cache / "zig.tar.xz"
                if cache_state != "missing":
                    cached.write_bytes(payload if cache_state == "valid" else b"untrusted")
                with mock.patch.dict(self.installer.PINS, {"x86_64": pin}), \
                        mock.patch.object(self.installer, "fetch", return_value=payload) as fetch, \
                        mock.patch.object(self.installer.subprocess, "run", return_value=subprocess.CompletedProcess(
                            [], 0, stdout=self.installer.VERSION + "\n")) as run:
                    result = self.installer.download_zig(directory / "install", "x86_64", {
                        "CGO_DOWNLOAD_CACHE": str(cache),
                    })
                self.assertEqual(fetch.call_count, 0 if cache_state == "valid" else 1)
                self.assertEqual(cached.read_bytes(), payload)
                self.assertEqual(result.read_bytes(), b"tool")
                self.assertEqual(run.call_args.args[0], [str(result), "version"])

    def test_missing_linux_compiler_gets_a_self_contained_wrapper(self):
        executable = self.directory / "zig tools/zig"
        executable.parent.mkdir()
        executable.write_text("#!/bin/sh\nexit 0\n")
        with mock.patch.object(self.installer.shutil, "which", return_value=None), \
                mock.patch.object(self.installer.platform, "system", return_value="Linux"), \
                mock.patch.object(self.installer.platform, "machine", return_value="x86_64"), \
                mock.patch.object(self.installer, "download_zig", return_value=executable), \
                mock.patch.object(self.installer, "probe") as probe:
            environment = self.installer.configure(self.directory, {"PATH": "/tools"})
        wrapper = Path(shlex.split(environment["CC"])[0])
        self.assertEqual(probe.call_args.args[0], [str(wrapper)])
        self.assertIn(shlex.quote(str(executable)), wrapper.read_text())
        self.assertIn('cc -target x86_64-linux-gnu "$@"', wrapper.read_text())
        self.assertEqual(environment["CGO_ENABLED"], "1")

    def test_archive_path_escape_is_rejected_even_after_checksum_verification(self):
        contents = io.BytesIO()
        with tarfile.open(fileobj=contents, mode="w:xz") as archive:
            entry = tarfile.TarInfo("../../escaped")
            entry.size = 4
            archive.addfile(entry, io.BytesIO(b"oops"))
        payload = contents.getvalue()
        pin = {"sha256": hashlib.sha256(payload).hexdigest(), "url": "https://example.invalid/zig.tar.xz"}
        with mock.patch.dict(self.installer.PINS, {"x86_64": pin}), \
                mock.patch.object(self.installer, "fetch", return_value=payload):
            with self.assertRaises(tarfile.FilterError):
                self.installer.download_zig(self.directory / "install", "x86_64", {})
        self.assertFalse((self.directory / "escaped").exists())
