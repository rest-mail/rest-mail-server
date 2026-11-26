#!/usr/bin/env python3
"""Collect and check local or registry-built release-image evidence.

See docs/release-evidence.md for the policy, digest boundary and fresh-start recipe.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import subprocess
import sys
import xml.etree.ElementTree as ElementTree
from datetime import datetime, timezone
from pathlib import Path
from types import SimpleNamespace

import cluster_e2e
import image_scan
import tls_reload_probe
from release_gate import COMMIT, DIGEST, RELEASE_IMAGES

ROOT = Path(__file__).resolve().parents[1]
POLICY = "restmail-fixable-high-critical-v1"
BASELINE_OPERATIONS = {"apiRoot", "describeServer", "getHealth", "getPublicStatus", "openapiDocument",
                       "openapiSpec", "recoveryEmailLink", "swaggerUI"}
TLS_LISTENERS = {
    "API internal mTLS": "api", "IMAP implicit TLS": "mail.release.test", "POP3 implicit TLS": "mail.release.test",
    "SMTP submission TLS": "mail.release.test", "SMTP STARTTLS": "mail.release.test", "API HTTPS front": "mail.release.test",
}


def sha256(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def dispositions(text):
    allowed, rationale = set(), []
    for line in text.splitlines():
        line = line.strip()
        if line.startswith("#"):
            rationale.append(line)
        elif line:
            reason = " ".join(rationale)
            if not re.fullmatch(r"CVE-\d{4}-\d+|GHSA-[\w-]+", line) or not re.search(r"https://\S+/issues/\d+", reason):
                raise ValueError(f"ignore entry has no documented issue disposition: {line}")
            if not re.search(r"not reachable|cannot be fixed|not applicable", reason, re.I):
                raise ValueError(f"ignore entry has no applicability/fix disposition: {line}")
            allowed.add(line)
            rationale = []
        else:
            rationale = []
    return allowed


def evaluate(report, ignore_text):
    allowed = dispositions(ignore_text)
    if report.get("SchemaVersion") != 2 or not report.get("Results") or report.get("ArtifactType") != "container_image":
        raise ValueError("scan covers no recognized container-image results")
    blocked, ignored, observed = [], [], []
    for result in report["Results"]:
        for vulnerability in result.get("Vulnerabilities") or []:
            identity = vulnerability["VulnerabilityID"]
            observed.append(identity)
            if identity in allowed:
                ignored.append(identity)
            elif vulnerability.get("Severity") in ("HIGH", "CRITICAL") and vulnerability.get("FixedVersion"):
                blocked.append(identity)
        for secret in result.get("Secrets") or []:
            identity = "secret:" + secret["RuleID"]
            observed.append(identity)
            if secret.get("Severity") in ("HIGH", "CRITICAL"):
                blocked.append(identity)
    return {"policy": POLICY, "ignore_sha256": hashlib.sha256(ignore_text.encode()).hexdigest(),
            "observed": sorted(observed), "ignored": sorted(ignored), "blocked": sorted(blocked),
            "verdict": "fail" if blocked else "pass"}


def conformance_counts(path):
    declared = cluster_e2e.junit_counts(path)
    cases = list(ElementTree.parse(path).getroot().iter("testcase"))
    observed = (len(cases), sum(len(case.findall("failure")) + len(case.findall("error")) for case in cases),
                sum(len(case.findall("skipped")) for case in cases))
    if observed != declared:
        raise ValueError("fresh-start JUnit counts do not match recorded exchanges")
    return observed


def baseline_operations(spec, junit):
    import yaml

    description = json.loads(spec.read_text())
    base = yaml.safe_load((ROOT / "vertrag.yml").read_text())
    operations = cluster_e2e.contract_operations(description, cluster_e2e.internal_operations(base))
    if not BASELINE_OPERATIONS.issubset(operations):
        raise ValueError("fresh-start description omits required baseline operations")
    observed_paths = {case.get("name", "").partition(" > ")[0] for case in ElementTree.parse(junit).iter("testcase")
                      if case.get("classname") == "vertrag.GET"}
    expected_paths = {path for path, methods in description["paths"].items()
                      if methods.get("get", {}).get("operationId") in operations}
    if not expected_paths.issubset(observed_paths):
        raise ValueError("fresh-start baseline has operations without recorded exchanges")
    return operations


def check(directory, commit, expected):
    reasons = []
    try:
        image_scan.check_trivy_pin(image_scan.read_gate((ROOT / image_scan.WORKFLOW).read_text()))
        builds = json.loads((directory / "builds.json").read_text())
        if builds["commit"] != commit or builds["images"] != expected:
            reasons.append("build commit or image digests differ from the release")
        if set(expected) != set(RELEASE_IMAGES) or not all(DIGEST.fullmatch(value) for value in expected.values()):
            reasons.append("release must name exactly all seven image digests")
        for name, digest in expected.items():
            evidence = json.loads((directory / f"{name}.json").read_text())
            if evidence["image_digest"] != digest or evidence["commit"] != commit:
                reasons.append(f"{name}: scan image digest or commit differs from built image")
            scan_path = directory / evidence["scan"]
            sbom_path = directory / evidence["sbom"]
            if sha256(scan_path) != evidence["scan_sha256"] or sha256(sbom_path) != evidence["sbom_sha256"]:
                reasons.append(f"{name}: scan or SBOM bytes differ from the recorded evidence")
            scan = json.loads(scan_path.read_text())
            if scan["Metadata"]["ImageID"] != digest:
                reasons.append(f"{name}: scanner inspected another image digest")
            sbom = json.loads(sbom_path.read_text())
            properties = {entry["name"]: entry["value"] for entry in sbom["metadata"]["component"].get("properties", [])}
            if sbom.get("bomFormat") != "CycloneDX" or not sbom.get("components") or properties.get("aquasecurity:trivy:ImageID") != digest:
                reasons.append(f"{name}: SBOM is empty or names another image digest")
            evaluation = evaluate(scan, (ROOT / ".trivyignore").read_text())
            if evidence.get("evaluation") != evaluation:
                reasons.append(f"{name}: missing or incorrect policy evaluation")
            if evaluation["verdict"] != "pass":
                reasons.append(f"{name}: policy refused {','.join(evaluation['blocked'])}")
            if evidence.get("scanner") != image_scan.TRIVY_IMAGE:
                reasons.append(f"{name}: scanner does not match the pinned tool")
        fresh = json.loads((directory / "fresh-start.json").read_text())
        if fresh["commit"] != commit or fresh["images"] != expected:
            reasons.append("fresh-start commit or running image digests differ from the release")
        junit = directory / "contract/examples.xml"
        spec = directory / "contract/openapi.json"
        counts = conformance_counts(junit)
        if fresh["junit_sha256"] != sha256(junit) or fresh["spec_sha256"] != sha256(spec):
            reasons.append("fresh-start report bytes differ from the recorded evidence")
        if fresh["exit_code"] != 0 or counts[0] <= 0 or counts[1:] != (0, 0) or list(counts) != fresh["counts"]:
            reasons.append("fresh-start evidence is failed, empty, skipped or inconsistent")
        operations = baseline_operations(spec, junit)
        if fresh["operations"] != operations:
            reasons.append("fresh-start evidence does not cover the declared baseline operations")
        if counts[0] < len(operations):
            reasons.append("fresh-start baseline has operations without recorded exchanges")
    except (OSError, ValueError, KeyError, TypeError, AttributeError, ElementTree.ParseError,
            image_scan.WorkflowError, cluster_e2e.StepFailed) as error:
        reasons.append(f"missing or malformed evidence: {error}")
    return reasons


def run(command, **kwargs):
    return subprocess.run(command, check=True, text=True, capture_output=True, **kwargs)


def local_images(builds):
    """The name each built image has in this Docker daemon.

    The evidence binds every image to its configuration digest, and the classic
    image store uses that digest as the local image ID. The containerd image store
    (the default from Docker 29) names a pulled image by the registry digest it was
    pulled as, and has no image under the configuration digest. collect records the
    local name beside each configuration so that containers start from, and are
    compared with, the exact image the daemon holds."""
    images = builds["images"]
    local = builds.get("local_images", images)
    registry = builds.get("registry_images", {})
    if set(local) != set(images) or any(local[name] not in (images[name], registry.get(name)) for name in images):
        raise ValueError("a local image name is neither its configuration nor its registry digest")
    return local


def image_ids(containers):
    actual = {}
    for name, container in containers.items():
        inspected = json.loads(run(["docker", "inspect", container]).stdout)[0]
        if not inspected["State"]["Running"]:
            raise ValueError(f"{name}: fresh-start container is not running")
        actual[name] = inspected["Image"]
    return actual


def registry_digests(directory):
    digests = {path.stem: path.read_text().strip() for path in directory.glob("*.digest")}
    if set(digests) != set(RELEASE_IMAGES) or not all(DIGEST.fullmatch(value) for value in digests.values()):
        raise ValueError("build artifacts must name exactly all seven registry digests")
    return digests


def platform_manifest(index, platform):
    system, architecture = platform.split("/")
    manifests = [entry["digest"] for entry in index["manifests"]
                 if (entry.get("platform", {}).get("os"), entry.get("platform", {}).get("architecture")) == (system, architecture)]
    if len(manifests) != 1 or not DIGEST.fullmatch(manifests[0]):
        raise ValueError(f"image index does not name one {platform} manifest")
    return manifests[0]


def registry_config(directory, name, index_digest, platform):
    index = directory / f"{name}.index.json"
    manifest = directory / f"{name}.manifest.json"
    if "sha256:" + sha256(index) != index_digest:
        raise ValueError(f"{name}: index bytes differ from the build digest")
    if "sha256:" + sha256(manifest) != platform_manifest(json.loads(index.read_bytes()), platform):
        raise ValueError(f"{name}: manifest bytes differ from the build digest chain")
    config = json.loads(manifest.read_bytes())["config"]["digest"]
    if not DIGEST.fullmatch(config):
        raise ValueError(f"{name}: invalid platform configuration digest")
    return config


def inspect_registry(directory, repository, name, digest, platform):
    index = directory / f"{name}.index.json"
    manifest = directory / f"{name}.manifest.json"
    def fetch(path, expected):
        reference = f"{repository}@{expected}"
        raw = subprocess.run(["docker", "buildx", "imagetools", "inspect", "--raw", reference], check=True, capture_output=True)
        path.write_bytes(raw.stdout)
    fetch(index, digest)
    fetch(manifest, platform_manifest(json.loads(index.read_bytes()), platform))
    config = registry_config(directory, name, digest, platform)
    reference = f"{repository}@{digest}"
    run(["docker", "pull", "--platform", platform, reference])
    local = run(["docker", "image", "inspect", "--format", "{{.Id}}", reference]).stdout.strip()
    # The classic store names the image by its configuration, the containerd store
    # by the index digest it was pulled as. Both are checked links of one chain.
    if local not in (config, digest):
        raise ValueError(f"{name}: Docker pulled a different configuration than the build manifest names")
    return config, local


def check_tls_receipt(receipt, commit, images):
    if receipt["exit_code"] != 0 or receipt["commit"] != commit or receipt["images"] != images:
        raise ValueError("failed TLS reload or a different commit or running image digest")
    observations = receipt["observations"]
    expected = {(listener, phase) for listener in TLS_LISTENERS for phase in tls_reload_probe.PHASES}
    if len(observations) != len(expected) or {(entry["listener"], entry["phase"]) for entry in observations} != expected:
        raise ValueError("TLS reload did not observe every listener and phase")
    fingerprints = {}
    for entry in observations:
        listener = entry["listener"]
        leaf, original = entry["leaf_sha256"], entry["original_leaf_sha256"]
        if (entry["hostname"] != TLS_LISTENERS[listener] or leaf == original
                or not all(re.fullmatch(r"[0-9a-f]{64}", fingerprint) for fingerprint in (leaf, original))
                or fingerprints.setdefault(listener, (leaf, original)) != (leaf, original)):
            raise ValueError(f"TLS reload observed inconsistent name or certificate fingerprints for {listener}")


def collect(args):
    if run(["git", "status", "--porcelain"], cwd=ROOT).stdout.strip():
        raise ValueError("release images require a clean committed checkout")
    args.platform = args.platform or "linux/" + image_scan.daemon_arch()
    workflow = (ROOT / image_scan.WORKFLOW).read_text()
    gate = image_scan.read_gate(workflow)
    image_scan.check_trivy_pin(gate)
    directory = args.directory
    directory.mkdir(parents=True, exist_ok=True)
    commit = image_scan.head_commit(ROOT)
    docker = image_scan.Docker(args.tag_prefix, args.label)
    builds = {"commit": commit, "platform": args.platform, "images": {}, "local_images": {}}
    registry = registry_digests(args.registry_digests) if args.registry_digests else None
    if registry is not None:
        builds["registry_images"] = registry
    write_json(directory / "builds.json", builds)
    failed = False
    for image in image_scan.read_matrix(workflow):
        volume = f"{args.tag_prefix.replace('/', '-')}{image.name}-scan"
        try:
            if registry is not None:
                digest, local = inspect_registry(directory, f"{args.registry_prefix}/{image.name}", image.name, registry[image.name], args.platform)
            else:
                built = docker.build(ROOT, image, args.platform, commit)
                (directory / f"{image.name}.build.log").write_text(built.stdout + built.stderr)
                if built.returncode:
                    raise ValueError(f"build exited {built.returncode}")
                digest = local = run(["docker", "image", "inspect", "--format", "{{.Id}}", docker.tag(image, args.platform)]).stdout.strip()
            builds["images"][image.name] = digest
            builds["local_images"][image.name] = local
            write_json(directory / "builds.json", builds)
            prepared = docker.prepare(volume, b"")
            if prepared.returncode or docker.save(image, args.platform, volume, reference=local if registry is not None else None).returncode:
                raise ValueError("could not save the exact image for scanning")
            cache = f"{args.tag_prefix.replace('/', '-')}trivy-cache"
            docker._create_volume(cache)
            for kind, filename in (("json", f"{image.name}.scan.json"), ("cyclonedx", f"{image.name}.sbom.json")):
                scanned = docker._run(["docker", "run", "--rm", "--label", args.label,
                                       "-v", f"{volume}:/scan", "-v", f"{cache}:/root/.cache/trivy",
                                       image_scan.TRIVY_IMAGE, "image", "--input", "/scan/image.tar",
                                       "--ignorefile", "/scan/ignore", "--format", kind,
                                       "--output", f"/scan/{filename}", "--exit-code", "0"])
                (directory / f"{filename}.log").write_text(scanned.stdout + scanned.stderr)
                if scanned.returncode:
                    raise ValueError(f"{kind} scanner exited {scanned.returncode}")
                report = docker.read_report(volume, filename)
                if report is None:
                    raise ValueError(f"{kind} scanner wrote no report")
                (directory / filename).write_text(report)
            scan_path, sbom_path = directory / f"{image.name}.scan.json", directory / f"{image.name}.sbom.json"
            evaluation = evaluate(json.loads(scan_path.read_text()), (ROOT / ".trivyignore").read_text())
            write_json(directory / f"{image.name}.json", {
                "commit": commit, "image_digest": digest, "scanner": image_scan.TRIVY_IMAGE,
                "at": datetime.now(timezone.utc).isoformat(),
                "scan": scan_path.name, "scan_sha256": sha256(scan_path),
                "sbom": sbom_path.name, "sbom_sha256": sha256(sbom_path), "evaluation": evaluation})
            print(f"release-evidence collected image={image.name} digest={digest} policy={evaluation['verdict']} findings={len(evaluation['observed'])}", flush=True)
            if evaluation["blocked"]:
                print(f"release-evidence policy refused {image.name}: {','.join(evaluation['blocked'])}", file=sys.stderr, flush=True)
            failed |= evaluation["verdict"] != "pass"
        except (OSError, ValueError, KeyError, TypeError, subprocess.CalledProcessError) as error:
            print(f"release-evidence collection failed image={image.name}: {error}", file=sys.stderr, flush=True)
            failed = True
        finally:
            docker.remove_volume(volume)
    return int(failed)


def fresh_start(args):
    builds = json.loads((args.directory / "builds.json").read_text())
    invalidate_fresh_start(args.directory, builds["commit"])
    containers = dict(item.split("=", 1) for item in args.container)
    if len(containers) != len(args.container):
        raise ValueError("duplicate fresh-start container argument")
    before = image_ids(containers)
    if set(before) != set(RELEASE_IMAGES) or before != local_images(builds):
        raise ValueError("fresh-start containers must run exactly the seven built image digests")
    api = json.loads(run(["docker", "inspect", containers["api"]]).stdout)[0]
    ports = api["NetworkSettings"]["Ports"].get("8080/tcp") or []
    if args.endpoint not in {f"http://127.0.0.1:{port['HostPort']}" for port in ports if port["HostIp"] == "127.0.0.1"}:
        raise ValueError("fresh-start endpoint must be the built API container's loopback port")
    arguments = SimpleNamespace(report_dir=str(args.directory), ingress_url=args.endpoint,
                                ingress_ca="", timeout=120, vertrag="")
    stages = cluster_e2e.Stages(arguments, None)
    detail = stages.contract_examples()
    if image_ids(containers) != before:
        raise ValueError("running images changed during fresh-start baseline")
    spec = args.directory / "contract/openapi.json"
    junit = args.directory / "contract/examples.xml"
    counts = conformance_counts(junit)
    if counts[2]:
        raise ValueError("fresh-start baseline skipped required exchanges")
    write_json(args.directory / "fresh-start.json", {
        "commit": builds["commit"], "images": builds["images"], "at": datetime.now(timezone.utc).isoformat(),
        "exit_code": 0, "counts": list(counts), "junit_sha256": sha256(junit), "spec_sha256": sha256(spec),
        "operations": baseline_operations(spec, junit)})
    print(f"release-evidence fresh-start {detail}")
    return 0


def invalidate_fresh_start(directory, commit):
    write_json(directory / "fresh-start.json", {"commit": commit, "images": {}, "exit_code": 1,
                                               "counts": [0, 0, 0], "junit_sha256": "", "spec_sha256": "", "operations": []})


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    commands = parser.add_subparsers(dest="command", required=True)
    collecting = commands.add_parser("collect")
    collecting.add_argument("--directory", required=True, type=Path)
    collecting.add_argument("--platform", choices=("linux/amd64", "linux/arm64"))
    collecting.add_argument("--tag-prefix", default="restmail-release-evidence-")
    collecting.add_argument("--label", default="rest-mail.run=release-evidence")
    collecting.add_argument("--registry-digests", type=Path)
    collecting.add_argument("--registry-prefix")
    fresh = commands.add_parser("fresh-start")
    fresh.add_argument("--directory", required=True, type=Path)
    fresh.add_argument("--endpoint", required=True)
    fresh.add_argument("--container", action="append", required=True, metavar="IMAGE=CONTAINER")
    checking = commands.add_parser("check")
    checking.add_argument("--directory", required=True, type=Path)
    checking.add_argument("--commit", required=True)
    checking.add_argument("--image", action="append", required=True, metavar="IMAGE=sha256:HEX")
    args = parser.parse_args(argv)
    try:
        if args.command == "collect":
            if bool(args.registry_prefix) != bool(args.registry_digests):
                raise ValueError("registry collection requires both --registry-prefix and --registry-digests")
            return collect(args)
        if args.command == "fresh-start":
            return fresh_start(args)
        if not COMMIT.fullmatch(args.commit):
            raise ValueError("release commit must be a full SHA")
        expected = dict(item.split("=", 1) for item in args.image)
        if len(expected) != len(args.image):
            raise ValueError("duplicate release image argument")
        reasons = check(args.directory, args.commit, expected)
        for reason in reasons:
            print(f"release-evidence refused: {reason}", file=sys.stderr)
        if not reasons:
            print(f"release-evidence authorized commit={args.commit} images={len(expected)}")
        return int(bool(reasons))
    except (OSError, ValueError, subprocess.CalledProcessError, cluster_e2e.StepFailed) as error:
        print(f"release-evidence refused: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
