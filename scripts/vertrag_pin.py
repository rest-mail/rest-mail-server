#!/usr/bin/env python3
"""The Vertrag release REST Mail's contract run uses, pinned with its checksum (#628).

The pin is VERTRAG_VERSION and VERTRAG_SHA256 in .github/workflows/e2e-suite.yml: the
version CI installs and the sha256 of its linux-x86_64 archive, from that release's own
checksums.txt.

  vertrag_pin.py version          print the pinned version
  vertrag_pin.py bump             pin the latest release, version and checksum together
  vertrag_pin.py install DIR      install the pinned version for this machine into DIR,
                                  checked against the release's checksums.txt

bump is what the scheduled bump workflow runs, and `chore deps:vertrag` runs it locally;
install is how a person's run uses the same binary CI does.
"""

import hashlib
import io
import json
import os
import platform
import re
import sys
import tarfile
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
        raise ValueError("e2e-suite.yml has no VERTRAG_VERSION and VERTRAG_SHA256 to pin")
    return version.group(2), sha.group(2)


def with_pin(workflow, version, sha):
    """The workflow pinning version and sha instead."""
    pinned(workflow)
    workflow = VERSION_LINE.sub(lambda m: m.group(1) + version, workflow, count=1)
    return SHA_LINE.sub(lambda m: m.group(1) + sha, workflow, count=1)


def asset(version, system="linux", machine="x86_64"):
    """A release archive's name."""
    return "vertrag-%s-%s-%s.tar.gz" % (version, system, machine)


def checksum_for(checksums, name):
    """name's sha256 in a release's checksums.txt."""
    for line in checksums.splitlines():
        parts = line.split()
        if len(parts) == 2 and parts[1] == name:
            return parts[0]
    raise ValueError("checksums.txt has no entry for %s" % name)


def fetch(url):
    request = urllib.request.Request(url, headers={"User-Agent": "rest-mail-server vertrag_pin"})
    token = os.environ.get("GITHUB_TOKEN")
    if token and url.startswith("https://api.github.com/"):
        request.add_header("Authorization", "Bearer " + token)
    with urllib.request.urlopen(request, timeout=60) as response:
        return response.read()


def latest_version():
    release = json.loads(fetch("https://api.github.com/repos/%s/releases/latest" % REPO))
    return release["tag_name"].lstrip("v")


def release_checksums(version):
    url = "https://github.com/%s/releases/download/v%s/checksums.txt" % (REPO, version)
    return fetch(url).decode()


def bump():
    with open(WORKFLOW) as f:
        workflow = f.read()
    current, _ = pinned(workflow)
    latest = latest_version()
    if latest == current:
        print("Vertrag %s is the latest release; nothing to bump" % current)
        return 0
    sha = checksum_for(release_checksums(latest), asset(latest))
    with open(WORKFLOW, "w") as f:
        f.write(with_pin(workflow, latest, sha))
    print("Vertrag %s -> %s (%s)" % (current, latest, sha))
    return 0


def this_machine():
    system = platform.system().lower()
    machine = {"aarch64": "arm64", "amd64": "x86_64"}.get(platform.machine().lower(), platform.machine().lower())
    return system, machine


def install(directory):
    with open(WORKFLOW) as f:
        version, _ = pinned(f.read())
    system, machine = this_machine()
    name = asset(version, system, machine)
    want = checksum_for(release_checksums(version), name)
    archive = fetch("https://github.com/%s/releases/download/v%s/%s" % (REPO, version, name))
    got = hashlib.sha256(archive).hexdigest()
    if got != want:
        raise ValueError("%s has sha256 %s, its release says %s" % (name, got, want))
    with tarfile.open(fileobj=io.BytesIO(archive)) as tar:
        member = tar.getmember("vertrag")
        binary = tar.extractfile(member).read()
    os.makedirs(directory, exist_ok=True)
    target = os.path.join(directory, "vertrag")
    with open(target, "wb") as f:
        f.write(binary)
    os.chmod(target, 0o755)
    print("installed Vertrag %s (%s, checksum verified) as %s" % (version, name, target))
    return 0


def main(argv):
    if len(argv) == 2 and argv[1] == "version":
        with open(WORKFLOW) as f:
            print(pinned(f.read())[0])
        return 0
    if len(argv) == 2 and argv[1] == "bump":
        return bump()
    if len(argv) == 3 and argv[1] == "install":
        return install(argv[2])
    print(__doc__.strip(), file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv))
