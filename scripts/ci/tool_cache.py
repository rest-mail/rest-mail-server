#!/usr/bin/env python3
"""Name immutable archive caches and bridge pre-cache base-tip oasdiff installers."""

import hashlib
import importlib.util
import os
from pathlib import Path
import platform
import re
import subprocess
import sys


def load_installer(filename):
    spec = importlib.util.spec_from_file_location("pinned_installer", filename)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def cache_key(tool, scripts):
    scripts = Path(scripts)
    if tool == "golangci-lint":
        source = (scripts / "ci/install-golangci-lint").read_text()
        version, target, checksum = (
            re.search(rf"(?m)^{name}=(\S+)$", source).group(1)
            for name in ("VERSION", "PLATFORM", "SHA256")
        )
        if (platform.system(), platform.machine()) != ("Linux", "x86_64"):
            raise ValueError("the pinned linter requires linux-amd64")
    elif tool == "vertrag":
        module = load_installer(scripts / "vertrag_pin.py")
        version, checksum = module.pinned((scripts / "vertrag-pin.yml").read_text())
        system, machine = module.this_machine()
        if (system, machine) != ("linux", "x86_64"):
            raise ValueError("only linux-x86_64 has a repository-pinned Vertrag checksum")
        target = f"{system}-{machine}"
    elif tool == "oasdiff":
        module = load_installer(scripts / "oasdiff_pin.py")
        system = platform.system().lower()
        machine = {"x86_64": "amd64", "aarch64": "arm64"}.get(
            platform.machine().lower(), platform.machine().lower()
        )
        target = "darwin_all" if system == "darwin" else f"{system}_{machine}"
        version, checksum = module.VERSION, module.CHECKSUMS[target]
    else:
        raise ValueError(f"unknown pinned tool: {tool}")
    return f"pinned-tool-v1-{tool}-{version}-{target}-{checksum}"


def install_base_oasdiff(installer, directory):
    cache = os.environ.get("OASDIFF_DOWNLOAD_CACHE")
    if not cache:
        return subprocess.call([sys.executable, installer, "install", directory])
    module = load_installer(installer)
    original_fetch = module.fetch

    def verified_fetch(url):
        name = url.rsplit("/", 1)[-1]
        prefix = f"oasdiff_{module.VERSION}_"
        target = name.removeprefix(prefix).removesuffix(".tar.gz")
        want = module.CHECKSUMS[target]
        archive_path = Path(cache) / name
        try:
            archive = archive_path.read_bytes()
        except OSError:
            archive = b""
        if hashlib.sha256(archive).hexdigest() != want:
            archive = original_fetch(url)
        if hashlib.sha256(archive).hexdigest() != want:
            raise ValueError(f"{name} does not match the base-tip pinned checksum")
        archive_path.parent.mkdir(parents=True, exist_ok=True)
        archive_path.write_bytes(archive)
        return archive

    module.fetch = verified_fetch
    module.install(directory)
    return 0


if __name__ == "__main__":
    if len(sys.argv) == 4 and sys.argv[1] == "key":
        print(cache_key(sys.argv[2], sys.argv[3]))
    elif len(sys.argv) == 4 and sys.argv[1] == "install-base-oasdiff":
        sys.exit(install_base_oasdiff(sys.argv[2], sys.argv[3]))
    else:
        sys.exit("usage: tool_cache.py key TOOL SCRIPTS | install-base-oasdiff INSTALLER DIRECTORY")
