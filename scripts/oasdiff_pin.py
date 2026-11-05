#!/usr/bin/env python3
"""Install the OpenAPI 3.1 comparator used by contract-change CI (#653)."""

import hashlib
import http.client
import io
import platform
import sys
import tarfile
import time
import urllib.error
import urllib.request
from pathlib import Path

VERSION = "1.34.0"
CHECKSUMS = {
    "darwin_all": "34f5713d9d4761d96f9ece0abccc27ba1514ea3640f024f95cf3db63da2d7952",
    "linux_amd64": "7c88e09fd500b231af2819eb21f6fdfc7bcdfe2741d2c1937c59e7b72c8a575a",
    "linux_arm64": "76118aef6887042f5147db537ee6670d3003f185e6aa128f7a04171cecc8baef",
}


# GitHub's release downloads have answered 504 for longer than a quick retry lasts:
# they failed three merge group jobs on 2026-11-05 (#1942).
RETRY_DELAYS = (5, 15, 30, 60)


def transient(error):
    """A request worth repeating: a 5xx or 429, or one that never got an answer."""
    if isinstance(error, urllib.error.HTTPError):
        return error.code >= 500 or error.code == 429
    return True


def fetch(url):
    for delay in (*RETRY_DELAYS, None):
        try:
            with urllib.request.urlopen(url, timeout=60) as response:
                return response.read()
        except (OSError, http.client.HTTPException) as error:
            if delay is None or not transient(error):
                raise
            print(f"oasdiff_pin: {url} failed ({error}); retrying in {delay}s", file=sys.stderr)
            time.sleep(delay)


def install(directory):
    system = platform.system().lower()
    machine = {"x86_64": "amd64", "aarch64": "arm64"}.get(
        platform.machine().lower(), platform.machine().lower()
    )
    target = "darwin_all" if system == "darwin" else system + "_" + machine
    want = CHECKSUMS[target]
    name = f"oasdiff_{VERSION}_{target}.tar.gz"
    url = f"https://github.com/oasdiff/oasdiff/releases/download/v{VERSION}/{name}"
    archive = fetch(url)
    if hashlib.sha256(archive).hexdigest() != want:
        raise ValueError(f"{name} does not match the pinned release checksum")
    with tarfile.open(fileobj=io.BytesIO(archive)) as tar:
        member = tar.getmember("oasdiff")
        if not member.isfile():
            raise ValueError("oasdiff is not a regular file in the release archive")
        binary = tar.extractfile(member).read()
    directory = Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    path = directory / "oasdiff"
    path.write_bytes(binary)
    path.chmod(0o755)
    print(f"installed oasdiff {VERSION} ({target}, pinned checksum verified) as {path}")


if __name__ == "__main__":
    if len(sys.argv) != 3 or sys.argv[1] != "install":
        sys.exit("usage: oasdiff_pin.py install DIRECTORY")
    install(sys.argv[2])
