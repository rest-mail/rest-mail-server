#!/usr/bin/env python3
"""Build every published image and scan it as the Docker Build & Push job does.

    chore image:scan                             every image, this machine's platform
    chore image:scan -- --all-platforms          every platform the gate scans
    chore image:scan -- --image js-filter        one image
    chore image:scan -- --platform linux/arm64   one platform

The images come from the matrix in .github/workflows/docker.yml, and the Trivy
version, severities and ignore file from its gate steps, so this cannot drift
from what fails on main (#1947). One image's failure does not stop the others.

Exit status 0 means no image has a finding the gate would fail on. Anything else
fails: a finding, a build that broke, or a scan that did not produce a report.

Nothing is bind mounted into Trivy: on Docker Desktop every file a container reads
that way holds a handle on the Mac (#1963). The image goes in over a pipe to a
labelled volume, and the report comes out over stdout.
"""

import argparse
import contextlib
import json
import os
import re
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = Path(".github/workflows/docker.yml")
GATE_STEP_PREFIX = "Scan image for fixable CRITICAL/HIGH"

# The same release docker.yml installs. Pinned by digest like the base images; a
# test fails if the tag and the workflow's version part.
TRIVY_IMAGE = (
    "aquasec/trivy:0.70.0"
    "@sha256:be1190afcb28352bfddc4ddeb71470835d16462af68d310f9f4bca710961a41e"
)

# Trivy also exits 1 when it fails outright, so the local scan asks for another
# status on findings. A broken scan must not read as "vulnerable" or as "clean".
FINDINGS_EXIT = 64

# Trims the build cache and frees the Docker VM's (#1963).
HYGIENE_SCRIPT = Path("tasks/docker-hygiene.sh")


class WorkflowError(Exception):
    """docker.yml no longer has the shape this script reads."""


@dataclass(frozen=True)
class Image:
    name: str
    dockerfile: str
    context: str


@dataclass(frozen=True)
class Gate:
    trivy_version: str
    severity: str
    ignore_unfixed: bool
    ignore_file: str
    platforms: tuple


@dataclass
class Result:
    image: str
    platform: str
    verdict: str  # pass, findings or error
    findings: list = field(default_factory=list)
    detail: str = ""


def _strip_value(raw):
    value = raw.split(" #", 1)[0].strip()
    return value.strip("\"'")


def read_matrix(text):
    """The images in the workflow's matrix, in its order."""
    images = []
    current = None
    for match in re.finditer(
        r"(?m)^\s+(- )?(image|dockerfile|context):\s*(\S+)\s*$", text
    ):
        dash, key, value = match.groups()
        if dash and key == "image":
            current = {"image": _strip_value(value)}
            images.append(current)
        elif current is not None:
            current[key] = _strip_value(value)
    if not images:
        raise WorkflowError("no images in the matrix")
    for entry in images:
        if not {"image", "dockerfile", "context"} <= entry.keys():
            raise WorkflowError(f"incomplete matrix entry: {entry}")
    return [Image(e["image"], e["dockerfile"], e["context"]) for e in images]


def _steps(text):
    """Each workflow step as its own block of text, keyed by step name."""
    found = {}
    parts = re.split(r"(?m)^\s+- name: ", text)
    for part in parts[1:]:
        name, _, body = part.partition("\n")
        found[name.strip()] = body
    return found


def _step_value(body, key):
    match = re.search(rf"(?m)^\s+{re.escape(key)}:\s*(.+?)\s*$", body)
    return _strip_value(match.group(1)) if match else None


def read_gate(text):
    """What the workflow's gate steps ask of Trivy."""
    steps = _steps(text)
    gates = {n: b for n, b in steps.items() if n.startswith(GATE_STEP_PREFIX)}
    if not gates:
        raise WorkflowError(f"no step named {GATE_STEP_PREFIX!r}")

    versions = {
        _step_value(body, "version")
        for body in steps.values()
        if "aquasecurity/trivy-action@" in body
    } - {None}
    if len(versions) != 1:
        raise WorkflowError(f"want one Trivy version, found {sorted(versions)}")

    platforms = []
    settings = set()
    for body in gates.values():
        platforms.append(_step_value(body, "TRIVY_PLATFORM"))
        settings.add(
            (
                _step_value(body, "severity"),
                _step_value(body, "ignore-unfixed"),
                _step_value(body, "trivyignores"),
                _step_value(body, "exit-code"),
            )
        )
    if len(settings) != 1:
        raise WorkflowError(f"the gate steps differ: {sorted(settings)}")
    severity, ignore_unfixed, ignore_file, exit_code = settings.pop()
    if None in (severity, ignore_unfixed, ignore_file) or None in platforms:
        raise WorkflowError("a gate step lacks a setting this script copies")
    if exit_code != "1":
        raise WorkflowError(f"the gate no longer fails on findings: {exit_code}")
    return Gate(
        trivy_version=versions.pop(),
        severity=severity,
        ignore_unfixed=ignore_unfixed == "true",
        ignore_file=ignore_file,
        platforms=tuple(platforms),
    )


def check_trivy_pin(gate, trivy_image=TRIVY_IMAGE):
    tag = trivy_image.split("@", 1)[0].rsplit(":", 1)[1]
    if f"v{tag}" != gate.trivy_version:
        raise WorkflowError(
            f"docker.yml uses Trivy {gate.trivy_version}, this script {tag}"
        )


def trivy_args(gate, report_name):
    """The scan the gate step runs, on a saved image rather than a registry's."""
    args = [
        "image",
        "--input",
        "/scan/image.tar",
        "--severity",
        gate.severity,
        "--ignorefile",
        "/scan/ignore",
        "--exit-code",
        str(FINDINGS_EXIT),
        "--format",
        "json",
        "--output",
        f"/scan/{report_name}",
        "--quiet",
    ]
    if gate.ignore_unfixed:
        args.append("--ignore-unfixed")
    return args


def read_findings(report):
    """One row per finding. A secret's matched text is never copied out."""
    rows = []
    for result in report.get("Results") or []:
        target = result.get("Target", "")
        for vuln in result.get("Vulnerabilities") or []:
            rows.append(
                {
                    "severity": vuln.get("Severity", ""),
                    "id": vuln.get("VulnerabilityID", ""),
                    "package": vuln.get("PkgName", ""),
                    "installed": vuln.get("InstalledVersion", ""),
                    "fixed": vuln.get("FixedVersion", ""),
                    "where": vuln.get("PkgPath") or target,
                }
            )
        for secret in result.get("Secrets") or []:
            rows.append(
                {
                    "severity": secret.get("Severity", ""),
                    "id": f"secret:{secret.get('RuleID', '')}",
                    "package": "",
                    "installed": "",
                    "fixed": "",
                    "where": target,
                }
            )
    return sorted(rows, key=lambda r: (r["severity"], r["package"], r["id"]))


def judge(exit_code, report):
    """The verdict on one scan, from Trivy's status and its report together."""
    if exit_code not in (0, FINDINGS_EXIT):
        return "error", [], f"trivy exited {exit_code}"
    if report is None:
        return "error", [], "trivy wrote no readable report"
    if not report.get("Results"):
        # Trivy found nothing it could read: not the same as nothing to fix.
        return "error", [], "the report covers nothing in the image"
    findings = read_findings(report)
    if exit_code == FINDINGS_EXIT or findings:
        return "findings", findings, ""
    return "pass", [], ""


class Docker:
    """The commands that touch Docker, apart so that the tests can replace them."""

    def __init__(self, tag_prefix, label, verbose=False):
        self.tag_prefix = tag_prefix
        self.label = label
        self.verbose = verbose

    def tag(self, image, platform):
        return f"{self.tag_prefix}{image.name}:{platform.split('/')[-1]}"

    def _run(self, command, **kwargs):
        return subprocess.run(command, text=True, capture_output=True, **kwargs)

    def build(self, root, image, platform, commit):
        command = [
            "docker", "build",
            "--platform", platform,
            "--build-arg", "VERSION=image-scan",
            "--build-arg", f"COMMIT={commit}",
            "--label", self.label,
            "--tag", self.tag(image, platform),
            "--file", str(root / image.dockerfile),
            str(root / image.context),
        ]  # fmt: skip
        return self._run(command)

    def _in_volume(self, volume, *command, **kwargs):
        """A throwaway container of the Trivy image, with the scan's volume at /scan."""
        return [
            "docker", "run", "--rm", "--label", self.label,
            "--volume", f"{volume}:/scan", "--entrypoint", command[0],
            *kwargs.get("flags", []), TRIVY_IMAGE, *command[1:],
        ]  # fmt: skip

    def _create_volume(self, name):
        return self._run(["docker", "volume", "create", "--label", self.label, name])

    def prepare(self, volume, ignore):
        """A new volume for one scan, holding the ignore file."""
        created = self._create_volume(volume)
        if created.returncode != 0:
            return created
        return subprocess.run(
            self._in_volume(volume, "sh", "-c", "cat > /scan/ignore", flags=["--interactive"]),
            input=ignore,
            capture_output=True,
        )

    def save(self, image, platform, volume):
        """docker save, piped into the scan's volume."""
        with subprocess.Popen(
            ["docker", "save", self.tag(image, platform)],
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
        ) as saving:
            stored = subprocess.run(
                self._in_volume(volume, "sh", "-c", "cat > /scan/image.tar", flags=["--interactive"]),
                stdin=saving.stdout,
                text=True,
                capture_output=True,
            )
            saving.stdout.close()
        if saving.returncode != 0:
            return subprocess.CompletedProcess(saving.args, saving.returncode, "", "docker save failed")
        return stored

    def scan(self, gate, volume, report_name):
        cache = f"{self.tag_prefix}trivy-cache"
        self._create_volume(cache)
        command = [
            "docker", "run", "--rm",
            "--name", f"{volume}-trivy",
            "--label", self.label,
            "--volume", f"{volume}:/scan",
            "--volume", f"{cache}:/root/.cache/trivy",
            TRIVY_IMAGE,
            *trivy_args(gate, report_name),
        ]  # fmt: skip
        return self._run(command)

    def read_report(self, volume, report_name):
        """The report's text, or None when Trivy wrote none."""
        done = self._run(self._in_volume(volume, "cat", f"/scan/{report_name}"))
        return done.stdout if done.returncode == 0 else None

    def remove_volume(self, volume):
        return self._run(["docker", "volume", "rm", "--force", volume])

    def remove(self, image, platform):
        return self._run(["docker", "image", "rm", self.tag(image, platform)])


def _tail(completed, lines=15):
    text = (completed.stderr or completed.stdout or "").strip()
    return "\n".join(text.splitlines()[-lines:])


def scan_one(docker, root, gate, image, platform, work_dir, commit, keep):
    arch = platform.split("/")[-1]
    report_dir = work_dir / f"{image.name}-{arch}"
    report_dir.mkdir(parents=True, exist_ok=True)
    report_path = report_dir / "report.json"
    report_path.unlink(missing_ok=True)

    built = docker.build(root, image, platform, commit)
    if built.returncode != 0:
        return Result(image.name, platform, "error", detail=f"build: {_tail(built)}")
    volume = f"{docker.tag_prefix}{image.name}-{arch}-{os.getpid()}"
    try:
        prepared = docker.prepare(volume, (ROOT / gate.ignore_file).read_bytes())
        if prepared.returncode != 0:
            return Result(image.name, platform, "error", detail="the scan's volume could not be made")
        saved = docker.save(image, platform, volume)
        if saved.returncode != 0:
            return Result(image.name, platform, "error", detail=f"save: {_tail(saved)}")
        scanned = docker.scan(gate, volume, report_path.name)
        text = docker.read_report(volume, report_path.name)
        report = None
        try:
            report = json.loads(text) if text is not None else None
        except ValueError:
            pass
        if report is not None:
            report_path.write_text(text, encoding="utf-8")
        verdict, findings, detail = judge(scanned.returncode, report)
        if verdict == "error" and scanned.returncode != FINDINGS_EXIT:
            detail = f"{detail}\n{_tail(scanned)}".strip()
        return Result(image.name, platform, verdict, findings, detail)
    finally:
        docker.remove_volume(volume)
        if not keep:
            docker.remove(image, platform)


def print_result(result, out):
    print(f"--- {result.image} {result.platform}: {result.verdict}", file=out)
    if result.detail:
        print(result.detail, file=out)
    for row in result.findings:
        print(
            f"{row['severity']:<9}{row['id']:<26}{row['package']:<24}"
            f"{row['installed']:<22}{row['fixed']:<28}{row['where']}",
            file=out,
        )
    out.flush()


def print_summary(results, out):
    print("\nImage scan summary", file=out)
    for result in results:
        counts = {}
        for row in result.findings:
            counts[row["severity"]] = counts.get(row["severity"], 0) + 1
        shown = ", ".join(f"{k} {v}" for k, v in sorted(counts.items()))
        suffix = f" ({shown})" if shown else ""
        print(f"  {result.image:<14}{result.platform:<13}{result.verdict}{suffix}", file=out)
    out.flush()


def run(docker, root, images, gate, platforms, work_dir, commit, keep, out=sys.stdout):
    results = []
    for image in images:
        for platform in platforms:
            result = scan_one(docker, root, gate, image, platform, work_dir, commit, keep)
            print_result(result, out)
            results.append(result)
    print_summary(results, out)
    return 0 if all(r.verdict == "pass" for r in results) else 1


def head_commit(root):
    done = subprocess.run(
        ["git", "-C", str(root), "rev-parse", "HEAD"],
        text=True,
        capture_output=True,
    )
    return done.stdout.strip() if done.returncode == 0 else "unknown"


def default_label():
    """The run's own label when it set one, so `chore clean` finds what a killed scan left."""
    return os.environ.get("RESTMAIL_LABEL") or "rest-mail.run=local"


def daemon_arch():
    done = subprocess.run(
        ["docker", "version", "--format", "{{.Server.Arch}}"], text=True, capture_output=True
    )
    return done.stdout.strip()


def choose_platforms(named, all_platforms, gate, arch):
    """One scan grows the build cache by gigabytes, so only a release needs both."""
    if named:
        return tuple(named)
    if all_platforms:
        return gate.platforms
    return (f"linux/{arch}",)


@contextlib.contextmanager
def tidied_afterwards(root):
    """Cap the build cache and free the VM's cache however the scan ended."""
    try:
        yield
    finally:
        for step in ("cap-build-cache", "drop-guest-cache"):
            subprocess.run(["sh", str(root / HYGIENE_SCRIPT), step], cwd=root)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n", 1)[0])
    parser.add_argument("--image", action="append", help="only this image (repeatable)")
    parser.add_argument("--platform", action="append", help="only this platform (repeatable)")
    parser.add_argument(
        "--all-platforms", action="store_true", help="every platform the gate scans, not only this machine's"
    )
    parser.add_argument(
        "--root",
        type=Path,
        default=ROOT,
        help="the tree to build (default: this checkout). The workflow and the "
        "ignore file always come from this checkout, so another tree is judged "
        "by the same rules",
    )
    parser.add_argument(
        "--work-dir", type=Path, help="reports (default: <root>/.cache/image-scan)"
    )
    parser.add_argument("--tag-prefix", default="restmail-image-scan-", help="prefix of local image names")
    parser.add_argument("--label", default=default_label(), help="label on what this makes (default: RESTMAIL_LABEL)")
    parser.add_argument("--keep-images", action="store_true", help="leave the built images in place")
    args = parser.parse_args(argv)

    root = args.root.resolve()
    workflow = (ROOT / WORKFLOW).read_text(encoding="utf-8")
    try:
        images = read_matrix(workflow)
        gate = read_gate(workflow)
        check_trivy_pin(gate)
    except WorkflowError as error:
        print(f"image scan: {error}", file=sys.stderr)
        return 2

    if args.image:
        unknown = set(args.image) - {i.name for i in images}
        if unknown:
            print(f"image scan: not in the matrix: {sorted(unknown)}", file=sys.stderr)
            return 2
        images = [i for i in images if i.name in args.image]
    platforms = choose_platforms(args.platform, args.all_platforms, gate, daemon_arch())
    unknown = set(platforms) - set(gate.platforms)
    if unknown:
        print(f"image scan: the gate does not scan {sorted(unknown)}", file=sys.stderr)
        return 2

    work_dir = (args.work_dir or root / ".cache" / "image-scan").resolve()
    work_dir.mkdir(parents=True, exist_ok=True)
    docker = Docker(args.tag_prefix, args.label)
    with tidied_afterwards(ROOT):
        return run(docker, root, images, gate, platforms, work_dir, head_commit(root), args.keep_images)


if __name__ == "__main__":
    sys.exit(main())
