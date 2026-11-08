"""Release frontend outages still allow checksum-verified installs via the asset API."""

import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest import mock
import urllib.error
import urllib.request

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from ci import github_release
import oasdiff_pin
import vertrag_pin

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "ci"))
import tool_cache

ROOT = Path(__file__).resolve().parents[2]


def response(body):
    return io.BytesIO(body)


class ReleaseAPITests(unittest.TestCase):
    def test_each_python_downloader_uses_the_exact_asset_after_a_504(self):
        for module, repository, name in (
            (vertrag_pin, "antimatter-studios/vertrag", "checksums.txt"),
            (oasdiff_pin, "oasdiff/oasdiff", "oasdiff_1.34.0_linux_amd64.tar.gz"),
        ):
            url = f"https://github.com/{repository}/releases/download/v1.34.0/{name}"
            metadata = {"assets": [{"name": "other", "id": 1}, {"name": name, "id": 42}]}
            with (
                self.subTest(module=module.__name__),
                mock.patch.dict(os.environ, {"GITHUB_TOKEN": "test-token"}),
                mock.patch("urllib.request.urlopen", side_effect=[
                    urllib.error.HTTPError(url, 504, "Gateway Time-out", {}, None),
                    response(json.dumps(metadata).encode()),
                    response(b"verified elsewhere"),
                ]) as open_url,
                mock.patch.object(module.time, "sleep") as sleep,
                mock.patch("sys.stderr", io.StringIO()),
            ):
                self.assertEqual(module.fetch(url), b"verified elsewhere")
                sleep.assert_not_called()
                requests = [call.args[0] for call in open_url.call_args_list]
                api = f"https://api.github.com/repos/{repository}/releases"
                self.assertEqual(requests[1].full_url, f"{api}/tags/v1.34.0")
                self.assertEqual(requests[2].full_url, f"{api}/assets/42")
                self.assertEqual(requests[2].get_header("Accept"), "application/octet-stream")
                self.assertEqual(requests[2].get_header("Authorization"), "Bearer test-token")
                self.assertIsNone(requests[0].get_header("Authorization") if hasattr(requests[0], "get_header") else None)

    def test_token_is_not_forwarded_to_the_release_asset_redirect(self):
        with (
            mock.patch.dict(os.environ, {"GITHUB_TOKEN": "test-token"}),
            mock.patch("urllib.request.urlopen", return_value=response(b"archive")) as open_url,
        ):
            github_release.request_bytes("https://api.github.com/repos/example/tool/releases/assets/42", "application/octet-stream")
        request = open_url.call_args.args[0]
        redirect = urllib.request.HTTPRedirectHandler().redirect_request(
            request, None, 302, "Found", {}, "https://release-assets.githubusercontent.com/archive"
        )
        self.assertIsNone(redirect.get_header("Authorization"))

    def test_missing_duplicate_and_invalid_asset_ids_are_refused(self):
        for assets in ([], [{"name": "archive", "id": 42}] * 2,
                       [{"name": "archive", "id": "../untrusted"}],
                       [{"name": "archive", "id": -1}]):
            with (
                self.subTest(assets=assets),
                mock.patch.object(github_release, "request_bytes", return_value=json.dumps({"assets": assets}).encode()) as request,
                self.assertRaisesRegex(ValueError, "unique asset"),
            ):
                github_release.fetch_asset("https://github.com/example/tool/releases/download/v1/archive")
            self.assertEqual(request.call_count, 1)

    def test_non_github_urls_never_receive_the_token(self):
        with mock.patch.object(github_release, "request_bytes") as request:
            for url in ("http://github.com/example/tool/releases/download/v1/archive",
                        "https://github.com.attacker.test/example/tool/releases/download/v1/archive"):
                with self.assertRaises(ValueError):
                    github_release.fetch_asset(url)
            request.assert_not_called()

    def test_failed_api_fallback_keeps_the_original_retry_budget(self):
        for module in (vertrag_pin, oasdiff_pin):
            url = "https://github.com/example/tool/releases/download/v1/archive"
            with (
                self.subTest(module=module.__name__),
                mock.patch("urllib.request.urlopen", side_effect=urllib.error.URLError("frontend outage")) as download,
                mock.patch.object(github_release, "fetch_asset", side_effect=urllib.error.URLError("API outage")) as fallback,
                mock.patch.object(module.time, "sleep") as sleep,
                mock.patch("sys.stderr", io.StringIO()),
                self.assertRaises(urllib.error.URLError),
            ):
                module.fetch(url)
            self.assertEqual(download.call_count, len(module.RETRY_DELAYS) + 1)
            self.assertEqual(fallback.call_count, len(module.RETRY_DELAYS) + 1)
            self.assertEqual(sleep.call_count, len(module.RETRY_DELAYS))

    def test_oasdiff_rejects_a_bad_api_archive_before_cache_or_install(self):
        with tempfile.TemporaryDirectory() as directory:
            with (
                mock.patch.dict(os.environ, {"OASDIFF_DOWNLOAD_CACHE": directory + "/cache"}),
                mock.patch("urllib.request.urlopen", side_effect=urllib.error.URLError("outage")),
                mock.patch.object(github_release, "fetch_asset", return_value=b"untrusted archive"),
                mock.patch("sys.stderr", io.StringIO()),
                self.assertRaisesRegex(ValueError, "pinned release checksum"),
            ):
                oasdiff_pin.install(directory + "/bin")
            self.assertFalse((Path(directory) / "cache").exists())
            self.assertFalse((Path(directory) / "bin").exists())

    def test_vertrag_rejects_a_bad_api_archive_before_cache_or_install(self):
        version, checksum = vertrag_pin.pinned(Path(vertrag_pin.PIN_FILE).read_text())
        checksums = f"{checksum}  {vertrag_pin.asset(version)}\n".encode()
        with tempfile.TemporaryDirectory() as directory:
            with (
                mock.patch.dict(os.environ, {"VERTRAG_DOWNLOAD_CACHE": directory + "/cache"}),
                mock.patch.object(vertrag_pin, "this_machine", return_value=("linux", "x86_64")),
                mock.patch("urllib.request.urlopen", side_effect=urllib.error.URLError("outage")),
                mock.patch.object(github_release, "fetch_asset", side_effect=[checksums, b"untrusted archive"]),
                mock.patch("sys.stderr", io.StringIO()),
                self.assertRaisesRegex(ValueError, "its release says"),
            ):
                vertrag_pin.install(directory + "/bin")
            self.assertFalse((Path(directory) / "cache").exists())
            self.assertFalse((Path(directory) / "bin").exists())

    def test_old_base_tip_downloader_also_falls_back_and_checks_its_pin(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "oasdiff_pin.py"
            checksum = hashlib.sha256(b"trusted archive").hexdigest()
            source.write_text(
                "import hashlib, pathlib\nVERSION = 'old'\n"
                f"CHECKSUMS = {{'linux_amd64': '{checksum}'}}\n"
                "def fetch(url):\n    raise OSError('release outage')\n"
                "def install(directory):\n"
                "    body = fetch('https://github.com/oasdiff/oasdiff/releases/download/vold/oasdiff_old_linux_amd64.tar.gz')\n"
                "    pathlib.Path(directory).write_bytes(body)\n"
            )
            with (
                mock.patch.dict(os.environ, {"OASDIFF_DOWNLOAD_CACHE": str(root / "cache")}),
                mock.patch("github_release.fetch_asset", return_value=b"trusted archive") as fallback,
            ):
                tool_cache.install_base_oasdiff(str(source), str(root / "bin"))
                fallback.assert_called_once()
                self.assertEqual((root / "bin").read_bytes(), b"trusted archive")
                next((root / "cache").iterdir()).write_bytes(b"bad cache")
                fallback.return_value = b"bad API download"
                with self.assertRaisesRegex(ValueError, "base-tip pinned checksum"):
                    tool_cache.install_base_oasdiff(str(source), str(root / "uninstalled"))
                self.assertFalse((root / "uninstalled").exists())

    def test_linter_fallback_is_still_checksum_verified(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            shims = root / "shims"
            shims.mkdir()
            for name, text in {
                "curl": "#!/bin/sh\nexit 22\n",
                "python3": '#!/bin/sh\ncase "$1" in */github_release.py) ;; *) exit 99 ;; esac\nprintf "%s" "$2" > "$TEST_URL"\ncp "$TEST_ARCHIVE" "$3"\n',
            }.items():
                path = shims / name
                path.write_text(text)
                path.chmod(0o755)
            archive = root / "archive"
            name = "golangci-lint-2.11.4-linux-amd64"
            with tarfile.open(archive, "w:gz") as release:
                member = tarfile.TarInfo(name + "/golangci-lint")
                member.size = len(b"binary")
                release.addfile(member, io.BytesIO(b"binary"))
            checksum = hashlib.sha256(archive.read_bytes()).hexdigest()
            for want, code in ((checksum, 0), ("0" * 64, 1)):
                target = root / want
                result = subprocess.run(
                    [str(ROOT / "scripts/ci/install-golangci-lint"), str(target)],
                    capture_output=True, text=True, env={
                        **os.environ, "PATH": str(shims) + os.pathsep + os.environ["PATH"],
                        "INSTALL_LINT_BASE_URL": "https://github.com/golangci/golangci-lint/releases/download",
                        "INSTALL_LINT_SHA256": want, "GITHUB_PATH": "", "GOLANGCI_LINT_DOWNLOAD_CACHE": "",
                        "TEST_URL": str(root / "url"), "TEST_ARCHIVE": str(archive),
                    },
                )
                self.assertEqual(result.returncode, code, result.stderr)
                self.assertEqual((root / "url").read_text(), f"https://github.com/golangci/golangci-lint/releases/download/v2.11.4/{name}.tar.gz")
                self.assertEqual(target.exists(), code == 0)


if __name__ == "__main__":
    unittest.main()
