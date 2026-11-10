#!/usr/bin/env python3
"""Provide and probe a real C compiler for Go race jobs without requiring root."""

import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import platform
import shlex
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.error
import urllib.request


VERSION = "0.17.0"
PINS = {
    "x86_64": {
        "url": "https://ziglang.org/download/0.17.0/zig-x86_64-linux-0.17.0.tar.xz",
        "sha256": "1cbe9df9f27e6b78d14ccbca43b6703a404ef79ef1c463de901d7f088d4e2026",
    },
    "aarch64": {
        "url": "https://ziglang.org/download/0.17.0/zig-aarch64-linux-0.17.0.tar.xz",
        "sha256": "9e8d11661d4ae3bd57702a3832781e23ad151dde5798e16a5ccd503f65234ff8",
    },
}


def fetch(url):
    for attempt in range(3):
        try:
            with urllib.request.urlopen(url, timeout=120) as response:
                return response.read()
        except (OSError, urllib.error.URLError) as error:
            if isinstance(error, urllib.error.HTTPError) and error.code not in (408, 429, 500, 502, 503, 504):
                raise
            if attempt == 2:
                raise
            print(f"cgo: tool download failed ({error}); retrying", file=sys.stderr)
            time.sleep(attempt + 1)


def download_zig(directory, architecture, environment):
    pin = PINS[architecture]
    payload = b""
    cached = None
    if environment.get("CGO_DOWNLOAD_CACHE"):
        cached = Path(environment["CGO_DOWNLOAD_CACHE"]) / pin["url"].rsplit("/", 1)[1]
        if cached.is_file():
            payload = cached.read_bytes()
    if hashlib.sha256(payload).hexdigest() != pin["sha256"]:
        payload = fetch(pin["url"])
    else:
        print("cgo: checksum-verified archive cache hit", file=sys.stderr)
    actual = hashlib.sha256(payload).hexdigest()
    if actual != pin["sha256"]:
        raise ValueError(f"cgo: Zig {VERSION} checksum {actual}, expected {pin['sha256']}")
    if cached:
        cached.parent.mkdir(parents=True, exist_ok=True)
        cached.write_bytes(payload)
    directory = Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    with tarfile.open(fileobj=io.BytesIO(payload), mode="r:xz") as archive:
        archive.extractall(directory, filter="data")
    executable = directory / f"zig-{architecture}-linux-{VERSION}" / "zig"
    version = subprocess.run([str(executable), "version"], check=True, capture_output=True, text=True, env=environment)
    if version.stdout.strip() != VERSION:
        raise ValueError(f"cgo: expected Zig {VERSION}, found {version.stdout.strip()}")
    return executable


def probe(compiler, environment):
    with tempfile.TemporaryDirectory(prefix="restmail-cgo-probe-") as temporary:
        source = Path(temporary) / "probe.c"
        binary = Path(temporary) / "probe"
        source.write_text('#include <stdio.h>\nint main(void) { return puts("cgo compiler ready") < 0; }\n')
        subprocess.run([*compiler, str(source), "-o", str(binary)], check=True, env=environment)
        result = subprocess.run([str(binary)], check=True, capture_output=True, text=True, env=environment)
        if result.stdout != "cgo compiler ready\n":
            raise ValueError("cgo: the compiler probe did not execute successfully")


def configure(directory, environment=None):
    environment = dict(os.environ if environment is None else environment)
    compiler = []
    if environment.get("CC"):
        compiler = shlex.split(environment["CC"])
        if not compiler or not shutil.which(compiler[0], path=environment.get("PATH")):
            raise ValueError("cgo: explicit CC is unavailable; fix that compiler rather than replacing it")
    else:
        for name in ("cc", "gcc", "clang"):
            available = shutil.which(name, path=environment.get("PATH"))
            if available:
                compiler = [available]
                break
    if not compiler:
        if platform.system() != "Linux":
            raise ValueError("cgo: install the host C compiler before running the local race gate")
        architecture = {"x86_64": "x86_64", "aarch64": "aarch64", "arm64": "aarch64"}[platform.machine()]
        directory = Path(directory)
        executable = download_zig(directory, architecture, environment)
        directory.mkdir(parents=True, exist_ok=True)
        wrapper = directory / "cc"
        wrapper.write_text(f'#!/bin/sh\nexec {shlex.quote(str(executable))} cc -target {architecture}-linux-gnu "$@"\n')
        wrapper.chmod(0o755)
        compiler = [str(wrapper)]
    probe(compiler, environment)
    return {"CC": shlex.join(compiler), "CGO_ENABLED": "1"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    parser.add_argument("--print-env", action="store_true")
    arguments = parser.parse_args()
    environment = configure(arguments.directory)
    if arguments.print_env:
        print(json.dumps(environment))
    elif os.environ.get("GITHUB_ENV"):
        with Path(os.environ["GITHUB_ENV"]).open("a") as output:
            for key, value in environment.items():
                output.write(f"{key}={value}\n")
    else:
        for key, value in environment.items():
            print(f"export {key}={shlex.quote(value)}")


if __name__ == "__main__":
    main()
