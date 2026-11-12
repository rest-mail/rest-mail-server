#!/usr/bin/env python3
"""Resolve signed distro packages and unpack oathtool without root or dpkg writes."""

import os
from pathlib import Path
import platform
import pwd
import re
import shutil
import subprocess
import sys
import tempfile


def packages(plan):
    selected = []
    for line in plan.splitlines():
        if not line.startswith("Inst "):
            continue
        match = re.match(r"^Inst (\S+)(?: \[[^]]+\])? \((\S+)", line)
        if not match:
            raise ValueError(f"unrecognized package plan: {line}")
        selected.append(f"{match[1]}={match[2]}")
    if not any(item.split("=", 1)[0].split(":", 1)[0] == "oathtool" for item in selected):
        raise ValueError("package plan does not provide oathtool")
    return selected


def install(directory, environment=None):
    environment = dict(os.environ if environment is None else environment)
    existing = shutil.which("oathtool", path=environment.get("PATH"))
    if existing:
        subprocess.run([existing, "--version"], check=True, env=environment)
        return
    directory = Path(directory).resolve()
    with tempfile.TemporaryDirectory(prefix="restmail-oathtool-") as temporary:
        state = Path(temporary)
        for name in ("lists/partial", "cache/archives/partial", "debs", "config-parts"):
            (state / name).mkdir(parents=True)
        config = state / "apt.conf"
        config.write_text(
            f'Dir::Etc::parts "{state}/config-parts";\n'
            f'Dir::State::lists "{state}/lists";\n'
            f'Dir::Cache "{state}/cache";\n'
            'Debug::NoLocking "true";\n'
            f'APT::Sandbox::User "{pwd.getpwuid(os.getuid()).pw_name}";\n'
            'Acquire::AllowInsecureRepositories "false";\n'
            'APT::Get::AllowUnauthenticated "false";\n'
        )
        environment.update(APT_CONFIG=str(config), LC_ALL="C")
        subprocess.run(["apt-get", "update"], check=True, env=environment)
        plan = subprocess.run(
            ["apt-get", "--simulate", "install", "--no-install-recommends", "--no-upgrade", "oathtool"],
            check=True, stdout=subprocess.PIPE, text=True, env=environment,
        )
        selected = packages(plan.stdout)
        subprocess.run(["apt-get", "download", *selected], check=True, cwd=state / "debs", env=environment)
        archives = sorted((state / "debs").glob("*.deb"))
        if not archives:
            raise ValueError("apt downloaded no oathtool packages")
        for archive in archives:
            subprocess.run(["dpkg-deb", "--extract", str(archive), str(directory)], check=True, env=environment)

    architecture = {"x86_64": "x86_64", "aarch64": "aarch64", "arm64": "aarch64"}[platform.machine()]
    libraries = [directory / name for name in (
        f"usr/lib/{architecture}-linux-gnu", f"lib/{architecture}-linux-gnu", "usr/lib", "lib",
    ) if (directory / name).is_dir()]
    library_path = os.pathsep.join(map(str, libraries))
    if environment.get("LD_LIBRARY_PATH"):
        library_path += os.pathsep + environment["LD_LIBRARY_PATH"]
    environment["LD_LIBRARY_PATH"] = library_path
    subprocess.run([str(directory / "usr/bin/oathtool"), "--version"], check=True, env=environment)
    for variable, content in (
        ("GITHUB_PATH", str(directory / "usr/bin")),
        ("GITHUB_ENV", f"LD_LIBRARY_PATH={library_path}"),
    ):
        if environment.get(variable):
            with Path(environment[variable]).open("a") as output:
                output.write(content + "\n")


if __name__ == "__main__":
    if len(sys.argv) != 2 or platform.system() != "Linux":
        sys.exit("usage on Linux: install_oathtool.py DIRECTORY")
    install(sys.argv[1])
