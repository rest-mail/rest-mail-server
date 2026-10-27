#!/usr/bin/env python3
"""The Vertrag release REST Mail's contract run uses, pinned with its checksum (#628).

The pin is VERTRAG_VERSION and VERTRAG_SHA256 in .github/workflows/e2e-suite.yml: the
version CI installs and the sha256 of its linux-x86_64 archive, from that release's own
checksums.txt.

  vertrag_pin.py version          print the pinned version
  vertrag_pin.py bump             pin the latest release, version and checksum together
  vertrag_pin.py install DIR      install the pinned version and a verified cache receipt
  vertrag_pin.py ensure DIR       print the absolute verified cached tool path, installing
                                  the pinned release only when the cache is absent or stale

bump is what the scheduled bump workflow runs, and `chore deps:vertrag` runs it locally;
install is how a person's run uses the same binary CI does.
"""

import contextlib
import hashlib
import io
import json
import os
import platform
import re
import subprocess
import sys
import tarfile
import tempfile
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
WORKFLOW = os.path.join(ROOT, ".github", "workflows", "e2e-suite.yml")
REPO = "antimatter-studios/vertrag"

VERSION_LINE = re.compile(r"^(\s*VERTRAG_VERSION:\s*)(\S+)\s*$", re.MULTILINE)
SHA_LINE = re.compile(r"^(\s*VERTRAG_SHA256:\s*)([0-9a-f]{64})\s*$", re.MULTILINE)


def pinned(workflow):
    """The version and checksum the workflow pins."""
    version, sha = VERSION_LINE.search(workflow), SHA_LINE.search(workflow)
    if not version or not sha:
        raise ValueError(
            "e2e-suite.yml has no VERTRAG_VERSION and VERTRAG_SHA256 to pin"
        )
    return version.group(2), sha.group(2)


def with_pin(workflow, version, sha):
    """The workflow pinning version and sha instead."""
    pinned(workflow)
    workflow = VERSION_LINE.sub(lambda m: m.group(1) + version, workflow, count=1)
    return SHA_LINE.sub(lambda m: m.group(1) + sha, workflow, count=1)


def asset(version, system="linux", machine="x86_64"):
    """A release archive's name."""
    return f"vertrag-{version}-{system}-{machine}.tar.gz"


def checksum_for(checksums, name):
    """name's sha256 in a release's checksums.txt."""
    for line in checksums.splitlines():
        parts = line.split()
        if len(parts) == 2 and parts[1] == name:
            return parts[0]
    raise ValueError(f"checksums.txt has no entry for {name}")


def fetch(url):
    request = urllib.request.Request(
        url, headers={"User-Agent": "rest-mail-server vertrag_pin"}
    )
    token = os.environ.get("GITHUB_TOKEN")
    if token and url.startswith("https://api.github.com/"):
        request.add_header("Authorization", "Bearer " + token)
    with urllib.request.urlopen(request, timeout=60) as response:
        return response.read()


def latest_version():
    release = json.loads(fetch(f"https://api.github.com/repos/{REPO}/releases/latest"))
    return release["tag_name"].lstrip("v")


def release_checksums(version):
    url = f"https://github.com/{REPO}/releases/download/v{version}/checksums.txt"
    return fetch(url).decode()


def bump():
    with open(WORKFLOW) as f:
        workflow = f.read()
    current, _ = pinned(workflow)
    latest = latest_version()
    if latest == current:
        print(f"Vertrag {current} is the latest release; nothing to bump")
        return 0
    sha = checksum_for(release_checksums(latest), asset(latest))
    with open(WORKFLOW, "w") as f:
        f.write(with_pin(workflow, latest, sha))
    print(f"Vertrag {current} -> {latest} ({sha})")
    return 0


def this_machine():
    system = platform.system().lower()
    machine = {"aarch64": "arm64", "amd64": "x86_64"}.get(
        platform.machine().lower(), platform.machine().lower()
    )
    return system, machine


def atomic_write(path, data, mode):
    """Publish only complete bytes; an interrupted write preserves the old target."""
    directory = os.path.dirname(path)
    fd, temporary = tempfile.mkstemp(
        prefix="." + os.path.basename(path) + "-", dir=directory
    )
    with os.fdopen(fd, "wb") as stream:
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())
    os.chmod(temporary, mode)
    os.replace(temporary, path)


def install(directory):
    with open(WORKFLOW) as f:
        version, linux_sha = pinned(f.read())
    system, machine = this_machine()
    name = asset(version, system, machine)
    want = checksum_for(release_checksums(version), name)
    if (system, machine) == ("linux", "x86_64") and want != linux_sha:
        raise ValueError("the release checksum disagrees with the immutable CI pin")
    archive = fetch(f"https://github.com/{REPO}/releases/download/v{version}/{name}")
    got = hashlib.sha256(archive).hexdigest()
    if got != want:
        raise ValueError(f"{name} has sha256 {got}, its release says {want}")
    with tarfile.open(fileobj=io.BytesIO(archive)) as tar:
        member = tar.getmember("vertrag")
        if not member.isfile():
            raise ValueError("the archive's vertrag member must be a regular file")
        binary = tar.extractfile(member).read()
    directory = os.path.realpath(directory)
    os.makedirs(directory, exist_ok=True)
    target = os.path.join(directory, "vertrag")
    receipt = {
        "version": version,
        "linux_archive_sha256": linux_sha,
        "system": system,
        "machine": machine,
        "archive_sha256": want,
        "binary_sha256": hashlib.sha256(binary).hexdigest(),
    }
    atomic_write(target, binary, 0o755)
    atomic_write(
        os.path.join(directory, "vertrag-pin.json"),
        (json.dumps(receipt, indent=2) + "\n").encode(),
        0o644,
    )
    print(f"installed Vertrag {version} ({name}, checksum verified) as {target}")
    return 0


def cached(directory, version, linux_sha, system, machine):
    """A cache hit requires the current pin, platform, executable and exact bytes."""
    try:
        with open(os.path.join(directory, "vertrag-pin.json")) as stream:
            receipt = json.load(stream)
        expected = {
            "version": version,
            "linux_archive_sha256": linux_sha,
            "system": system,
            "machine": machine,
        }
        if any(receipt.get(key) != value for key, value in expected.items()):
            return False
        if (system, machine) == ("linux", "x86_64") and receipt.get(
            "archive_sha256"
        ) != linux_sha:
            return False
        target = os.path.join(directory, "vertrag")
        if not os.path.isfile(target) or not os.access(target, os.X_OK):
            return False
        with open(target, "rb") as stream:
            return hashlib.file_digest(stream, "sha256").hexdigest() == receipt.get(
                "binary_sha256"
            )
    except (OSError, ValueError, TypeError, AttributeError):
        return False


def ensure(directory):
    """Select only the project cache, never an unrelated executable from PATH."""
    directory = os.path.realpath(directory)
    with open(WORKFLOW) as stream:
        version, linux_sha = pinned(stream.read())
    system, machine = this_machine()
    if not cached(directory, version, linux_sha, system, machine):
        # stdout is a shell-safe path value; installation messages are diagnostics.
        with contextlib.redirect_stdout(sys.stderr):
            install(directory)
    target = os.path.join(directory, "vertrag")
    try:
        version_check = subprocess.run(
            [target, "version"], capture_output=True, timeout=10, check=False
        )
    except (OSError, subprocess.TimeoutExpired) as error:
        raise ValueError("pinned Vertrag cannot execute its version command") from error
    if version_check.returncode != 0:
        raise ValueError("pinned Vertrag cannot execute its version command")
    return target


def main(argv):
    if len(argv) == 2 and argv[1] == "version":
        with open(WORKFLOW) as f:
            print(pinned(f.read())[0])
        return 0
    if len(argv) == 2 and argv[1] == "bump":
        return bump()
    if len(argv) == 3 and argv[1] == "install":
        return install(argv[2])
    if len(argv) == 3 and argv[1] == "ensure":
        print(ensure(argv[2]))
        return 0
    print(__doc__.strip(), file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv))
