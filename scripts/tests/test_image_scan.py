"""The local image scan judges what the Docker Build & Push job judges (#1947)."""

import importlib.util
import io
import json
import re
import shutil
import sys
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = ROOT / ".github" / "workflows" / "docker.yml"
IGNORE_FILE = ROOT / ".trivyignore"
JS_FILTER_DOCKERFILE = ROOT / "projects" / "js-filter-sidecar" / "Dockerfile"

spec = importlib.util.spec_from_file_location(
    "image_scan", ROOT / "scripts" / "image_scan.py"
)
image_scan = importlib.util.module_from_spec(spec)
sys.modules["image_scan"] = image_scan
spec.loader.exec_module(image_scan)

FINDING = {
    "VulnerabilityID": "CVE-2026-59873",
    "PkgName": "tar",
    "InstalledVersion": "7.0.0",
    "FixedVersion": "7.0.1",
    "Severity": "CRITICAL",
    "PkgPath": "usr/local/lib/node_modules/npm/node_modules/tar/package.json",
}


def report_with(*vulnerabilities, secrets=()):
    return {
        "Results": [
            {
                "Target": "image.tar",
                "Vulnerabilities": list(vulnerabilities),
                "Secrets": list(secrets),
            }
        ]
    }


CLEAN_REPORT = {"Results": [{"Target": "image.tar (alpine 3.23.0)"}]}


class FakeDocker:
    """Answers as Docker and Trivy would, one scripted answer per image."""

    tag_prefix = "test-"

    def __init__(self, answers):
        self.answers = answers
        self.removed = []
        self.built = []
        self.volumes = {}

    def build(self, root, image, platform, commit):
        self.built.append((image.name, platform))
        code = self.answers[image.name].get("build", 0)
        return SimpleNamespace(returncode=code, stdout="", stderr="no such file")

    def prepare(self, volume, ignore):
        self.volumes[volume] = {}
        return SimpleNamespace(returncode=0, stdout="", stderr="")

    def save(self, image, platform, volume):
        self.volumes[volume]["image"] = image.name
        return SimpleNamespace(returncode=0, stdout="", stderr="")

    def scan(self, gate, volume, report_name):
        answer = self.answers[self.volumes[volume]["image"]]
        if "report" in answer:
            self.volumes[volume][report_name] = json.dumps(answer["report"])
        return SimpleNamespace(
            returncode=answer.get("scan", 0), stdout="", stderr="trivy said"
        )

    def read_report(self, volume, report_name):
        return self.volumes[volume].get(report_name)

    def remove_volume(self, volume):
        del self.volumes[volume]

    def remove(self, image, platform):
        self.removed.append((image.name, platform))


class WorkflowTests(unittest.TestCase):
    def setUp(self):
        self.text = WORKFLOW.read_text(encoding="utf-8")

    def test_the_matrix_lists_every_image(self):
        images = image_scan.read_matrix(self.text)
        self.assertEqual(
            [i.name for i in images],
            [
                "api",
                "smtp-gateway",
                "imap-gateway",
                "pop3-gateway",
                "js-filter",
                "webmail",
                "admin",
            ],
        )
        for image in images:
            with self.subTest(image.name):
                self.assertTrue((ROOT / image.dockerfile).is_file())
                self.assertTrue((ROOT / image.context).is_dir())

    def test_the_gate_settings_are_read_from_the_workflow(self):
        gate = image_scan.read_gate(self.text)
        self.assertEqual(gate.severity, "CRITICAL,HIGH")
        self.assertTrue(gate.ignore_unfixed)
        self.assertEqual(gate.ignore_file, ".trivyignore")
        self.assertEqual(gate.platforms, ("linux/amd64", "linux/arm64"))

    def test_the_local_trivy_is_the_workflows_trivy(self):
        gate = image_scan.read_gate(self.text)
        image_scan.check_trivy_pin(gate)
        with self.assertRaises(image_scan.WorkflowError):
            image_scan.check_trivy_pin(gate, "aquasec/trivy:0.1.0@sha256:00")

    def test_a_gate_that_stops_failing_is_refused(self):
        weakened = self.text.replace('exit-code: "1"', 'exit-code: "0"')
        with self.assertRaises(image_scan.WorkflowError):
            image_scan.read_gate(weakened)

    def test_gate_steps_that_disagree_are_refused(self):
        disagreeing = self.text.replace(
            "severity: CRITICAL,HIGH", "severity: CRITICAL", 1
        )
        with self.assertRaises(image_scan.WorkflowError):
            image_scan.read_gate(disagreeing)

    def test_the_scan_passes_the_gates_flags_to_trivy(self):
        gate = image_scan.read_gate(self.text)
        args = image_scan.trivy_args(gate, "report.json")
        self.assertEqual(args[args.index("--severity") + 1], "CRITICAL,HIGH")
        self.assertIn("--ignore-unfixed", args)
        self.assertIn("--ignorefile", args)
        self.assertNotIn("--scanners", args, "the gate scans with Trivy's defaults")

    def test_one_images_failure_does_not_cancel_the_others(self):
        match = re.search(r"(?m)^    strategy:\n((?:.*\n)*?)      matrix:", self.text)
        self.assertIsNotNone(match)
        self.assertRegex(match.group(1), r"(?m)^      fail-fast: false$")


class IgnoreFileTests(unittest.TestCase):
    def test_every_exception_has_a_reason_and_an_issue_link(self):
        lines = IGNORE_FILE.read_text(encoding="utf-8").splitlines()
        for number, line in enumerate(lines):
            if not line.strip() or line.startswith("#"):
                continue
            with self.subTest(line):
                reason = []
                for above in reversed(lines[:number]):
                    if not above.startswith("#"):
                        break
                    reason.append(above)
                self.assertRegex(
                    " ".join(reason),
                    r"github\.com/rest-mail/rest-mail-server/issues/\d+",
                )


class JudgeTests(unittest.TestCase):
    def test_a_clean_report_passes(self):
        self.assertEqual(image_scan.judge(0, CLEAN_REPORT)[0], "pass")

    def test_findings_fail_and_are_listed(self):
        verdict, rows, _ = image_scan.judge(
            image_scan.FINDINGS_EXIT, report_with(FINDING)
        )
        self.assertEqual(verdict, "findings")
        self.assertEqual(rows[0]["id"], "CVE-2026-59873")
        self.assertEqual(rows[0]["fixed"], "7.0.1")

    def test_a_trivy_failure_is_not_a_pass_or_a_finding(self):
        self.assertEqual(image_scan.judge(1, None)[0], "error")
        self.assertEqual(image_scan.judge(1, CLEAN_REPORT)[0], "error")

    def test_a_missing_report_is_not_a_pass(self):
        self.assertEqual(image_scan.judge(0, None)[0], "error")

    def test_a_report_that_covers_nothing_is_not_a_pass(self):
        self.assertEqual(image_scan.judge(0, {"Results": []})[0], "error")
        self.assertEqual(image_scan.judge(0, {})[0], "error")

    def test_findings_in_a_report_fail_whatever_the_status(self):
        self.assertEqual(image_scan.judge(0, report_with(FINDING))[0], "findings")

    def test_a_secrets_text_is_never_copied_out(self):
        secret = {
            "RuleID": "github-pat",
            "Severity": "CRITICAL",
            "Match": "ghp_the_secret_itself",
            "Code": {"Lines": [{"Content": "ghp_the_secret_itself"}]},
        }
        _, rows, _ = image_scan.judge(
            image_scan.FINDINGS_EXIT, report_with(secrets=[secret])
        )
        self.assertEqual(rows[0]["id"], "secret:github-pat")
        self.assertNotIn("ghp_the_secret_itself", json.dumps(rows))


class RunTests(unittest.TestCase):
    def setUp(self):
        self.work_dir = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.work_dir)
        self.gate = image_scan.Gate(
            trivy_version="v0.70.0",
            severity="CRITICAL,HIGH",
            ignore_unfixed=True,
            ignore_file=".trivyignore",
            platforms=("linux/amd64", "linux/arm64"),
        )
        self.images = [
            image_scan.Image("first", "first/Dockerfile", "first"),
            image_scan.Image("second", "second/Dockerfile", "second"),
        ]

    def run_scan(self, docker, keep=False):
        out = io.StringIO()
        code = image_scan.run(
            docker,
            ROOT,
            self.images,
            self.gate,
            self.gate.platforms,
            self.work_dir,
            "abc123",
            keep,
            out=out,
        )
        return code, out.getvalue()

    def test_every_image_clean_exits_zero(self):
        docker = FakeDocker(
            {"first": {"report": CLEAN_REPORT}, "second": {"report": CLEAN_REPORT}}
        )
        code, _ = self.run_scan(docker)
        self.assertEqual(code, 0)

    def test_one_images_findings_fail_the_run_and_the_next_image_still_scans(self):
        docker = FakeDocker(
            {
                "first": {
                    "report": report_with(FINDING),
                    "scan": image_scan.FINDINGS_EXIT,
                },
                "second": {"report": CLEAN_REPORT},
            }
        )
        code, output = self.run_scan(docker)
        self.assertEqual(code, 1)
        self.assertEqual(len(docker.built), 4, "both images on both platforms")
        self.assertIn("CVE-2026-59873", output)
        self.assertRegex(output, r"second\s+linux/arm64\s+pass")

    def test_a_build_that_breaks_fails_the_run_and_the_next_image_still_scans(self):
        docker = FakeDocker(
            {"first": {"build": 1}, "second": {"report": CLEAN_REPORT}}
        )
        code, output = self.run_scan(docker)
        self.assertEqual(code, 1)
        self.assertIn("no such file", output)
        self.assertRegex(output, r"first\s+linux/amd64\s+error")
        self.assertRegex(output, r"second\s+linux/amd64\s+pass")

    def test_a_report_left_by_an_earlier_scan_is_not_read(self):
        stale = self.work_dir / "first-amd64"
        stale.mkdir()
        (stale / "report.json").write_text(json.dumps(CLEAN_REPORT))
        docker = FakeDocker({"first": {"scan": 1}, "second": {"report": CLEAN_REPORT}})
        code, output = self.run_scan(docker)
        self.assertEqual(code, 1)
        self.assertRegex(output, r"first\s+linux/amd64\s+error")

    def test_images_are_removed_unless_kept(self):
        answers = {"first": {"report": CLEAN_REPORT}, "second": {"report": CLEAN_REPORT}}
        docker = FakeDocker(answers)
        self.run_scan(docker)
        self.assertEqual(len(docker.removed), 4)
        kept = FakeDocker(answers)
        self.run_scan(kept, keep=True)
        self.assertEqual(kept.removed, [])

    def test_no_scan_leaves_a_volume_behind(self):
        docker = FakeDocker({"first": {"scan": 1}, "second": {"report": CLEAN_REPORT}})
        self.run_scan(docker)
        self.assertEqual(docker.volumes, {})

    def test_the_report_stays_on_disk_to_read_afterwards(self):
        docker = FakeDocker(
            {"first": {"report": CLEAN_REPORT}, "second": {"report": CLEAN_REPORT}}
        )
        self.run_scan(docker)
        report = self.work_dir / "first-arm64" / "report.json"
        self.assertEqual(json.loads(report.read_text()), CLEAN_REPORT)


class DockerCommandTests(unittest.TestCase):
    """What reaches the Docker VM: what the machine pays for (#1963)."""

    def setUp(self):
        self.commands = []

        def record(command, **kwargs):
            self.commands.append(command)
            return SimpleNamespace(returncode=0, stdout="", stderr="")

        patcher = patch.object(image_scan.subprocess, "run", record)
        patcher.start()
        self.addCleanup(patcher.stop)
        self.docker = image_scan.Docker("restmail-image-scan-", "rest-mail-loop=test")
        self.gate = image_scan.Gate("v0.70.0", "CRITICAL,HIGH", True, ".trivyignore", ("linux/arm64",))

    def test_nothing_is_bind_mounted_into_trivy(self):
        self.docker.scan(self.gate, "the-volume", "report.json")
        self.docker.read_report("the-volume", "report.json")
        for command in self.commands:
            for index, word in enumerate(command):
                if word == "--volume":
                    self.assertNotIn("/", command[index + 1].split(":", 1)[0], command)

    def test_every_volume_and_container_carries_the_label(self):
        self.docker.prepare("the-volume", b"ignored")
        self.docker.scan(self.gate, "the-volume", "report.json")
        self.docker.read_report("the-volume", "report.json")
        made = [c for c in self.commands if c[1] == "run" or c[1:3] == ["volume", "create"]]
        self.assertGreaterEqual(len(made), 4)
        for command in made:
            self.assertIn("rest-mail-loop=test", command, command)

    def test_the_trivy_cache_stays_in_one_volume_between_scans(self):
        self.docker.scan(self.gate, "one", "report.json")
        self.docker.scan(self.gate, "two", "report.json")
        caches = {
            word for command in self.commands for word in command
            if word.endswith(":/root/.cache/trivy")
        }
        self.assertEqual(caches, {"restmail-image-scan-trivy-cache:/root/.cache/trivy"})


class ChoicesTests(unittest.TestCase):
    gate = image_scan.Gate("v0.70.0", "CRITICAL,HIGH", True, ".trivyignore", ("linux/amd64", "linux/arm64"))

    def test_an_ordinary_run_scans_the_machines_own_platform(self):
        self.assertEqual(image_scan.choose_platforms(None, False, self.gate, "arm64"), ("linux/arm64",))

    def test_all_platforms_scans_what_the_gate_scans(self):
        self.assertEqual(image_scan.choose_platforms(None, True, self.gate, "arm64"), self.gate.platforms)

    def test_a_named_platform_is_kept(self):
        self.assertEqual(image_scan.choose_platforms(["linux/amd64"], False, self.gate, "arm64"), ("linux/amd64",))

    def test_a_machine_the_gate_does_not_scan_is_not_a_silent_pass(self):
        self.assertEqual(image_scan.choose_platforms(None, False, self.gate, "riscv64"), ("linux/riscv64",))

    def test_the_label_comes_from_the_run(self):
        with patch.dict(image_scan.os.environ, {"RESTMAIL_LABEL": "rest-mail-loop=prfix"}):
            self.assertEqual(image_scan.default_label(), "rest-mail-loop=prfix")
        with patch.dict(image_scan.os.environ):
            image_scan.os.environ.pop("RESTMAIL_LABEL", None)
            self.assertEqual(image_scan.default_label(), "rest-mail.run=local")


class HygieneTests(unittest.TestCase):
    def test_the_hygiene_script_is_there_and_takes_both_steps(self):
        script = (ROOT / image_scan.HYGIENE_SCRIPT).read_text()
        for step in ("cap-build-cache", "drop-guest-cache"):
            self.assertIn(step, script)

    def test_the_cache_is_capped_and_the_guest_cache_dropped_even_after_a_failure(self):
        calls = []
        with tempfile.TemporaryDirectory() as root:
            with patch.object(image_scan.subprocess, "run", lambda c, **k: calls.append(c)):
                with self.assertRaises(RuntimeError):
                    with image_scan.tidied_afterwards(Path(root)):
                        raise RuntimeError("the scan broke")
        self.assertEqual([c[-1] for c in calls], ["cap-build-cache", "drop-guest-cache"])


class JsFilterImageTests(unittest.TestCase):
    def test_the_runtime_image_drops_the_package_managers(self):
        # A runtime needs node, not the tools that install packages. npm alone
        # brought 13 findings into the image (#1947).
        text = JS_FILTER_DOCKERFILE.read_text(encoding="utf-8")
        prod = text.split(" AS prod", 1)[1]
        for path in (
            "/usr/local/lib/node_modules",
            "/usr/local/bin/npm",
            "/usr/local/bin/npx",
            "/usr/local/bin/corepack",
        ):
            with self.subTest(path):
                self.assertIn(path, prod)


if __name__ == "__main__":
    unittest.main()
