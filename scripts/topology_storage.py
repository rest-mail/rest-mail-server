#!/usr/bin/env python3
"""Reject memory-backed writable storage in a labelled topology.

Docker's implicit /dev/shm IPC mount is not an application scratch mount.
Explicit tmpfs mounts, including those hidden behind a named local volume,
are forbidden. Go state on read-only services needs writable disk storage;
short-lived build tools may also use their writable, disk-backed image layer.
"""

import argparse
import json
import subprocess
import sys

from topology_plan import validate


GO_PATHS = ("GOPATH", "GOMODCACHE", "GOCACHE")


def beneath(path, destination):
    return path == destination or path.startswith(destination.rstrip("/") + "/")


def violations(containers, volumes):
    errors = []
    for container in containers:
        name = container["Name"].lstrip("/")
        mounts = container.get("Mounts", [])
        temporary = container.get("HostConfig", {}).get("Tmpfs") or {}
        for destination in temporary:
            errors.append(f"{name}: tmpfs mount at {destination}")
        for mount in mounts:
            options = volumes.get(mount.get("Name"), {}).get("Options") or {}
            if mount["Type"] == "tmpfs" or options.get("type") == "tmpfs":
                errors.append(f"{name}: memory-backed mount at {mount['Destination']}")
        environment = dict(value.split("=", 1) for value in container.get("Config", {}).get("Env", []) if "=" in value)
        for variable in GO_PATHS:
            path = environment.get(variable)
            if not path:
                continue
            matching = [mount for mount in mounts if beneath(path, mount["Destination"])]
            matching.extend({"Type": "tmpfs", "Destination": destination} for destination in temporary if beneath(path, destination))
            mount = max(matching, key=lambda item: len(item["Destination"]), default=None)
            options = (volumes.get(mount.get("Name"), {}).get("Options") or {}) if mount else {}
            invalid = container.get("HostConfig", {}).get("ReadonlyRootfs", False) if not mount else (
                mount["Type"] not in ("volume", "bind") or options.get("type") == "tmpfs" or not mount.get("RW", True)
            )
            if invalid:
                errors.append(f"{name}: {variable}={path} is not on writable disk storage")
    return errors


def docker(*arguments):
    return subprocess.check_output(["docker", *arguments], text=True)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("topology", nargs="?")
    parser.add_argument("--label")
    arguments = parser.parse_args(argv)
    if bool(arguments.topology) == bool(arguments.label):
        parser.error("supply a topology name or --label, not both")
    if arguments.topology:
        validate(arguments.topology)
    label = arguments.label or f"rest-mail-loop={arguments.topology}"
    if not label.startswith(("rest-mail-loop=", "rest-mail.run=")) or not label.split("=", 1)[1]:
        parser.error("storage checks need a nonempty rest-mail run label")
    identifiers = docker("ps", "-aq", "--filter", f"label={label}").split()
    if not identifiers:
        parser.error("the topology has no containers; storage was not checked")
    containers = json.loads(docker("inspect", *identifiers))
    names = sorted({mount["Name"] for container in containers for mount in container.get("Mounts", []) if mount["Type"] == "volume"})
    volumes = {volume["Name"]: volume for volume in json.loads(docker("volume", "inspect", *names))} if names else {}
    errors = violations(containers, volumes)
    for container in containers:
        if not container.get("State", {}).get("Running"):
            continue
        paths = [value.split("=", 1)[1] for value in container.get("Config", {}).get("Env", [])
                 if value.split("=", 1)[0] in GO_PATHS and value.split("=", 1)[1:]]
        if paths:
            filesystems = docker("exec", container["Id"], "stat", "-f", "-c", "%T", *paths).splitlines()
            for path, filesystem in zip(paths, filesystems):
                if filesystem.strip() == "tmpfs":
                    errors.append(f"{container['Name'].lstrip('/')}: Go state {path} is on tmpfs")
    if errors:
        print("\n".join(errors), file=sys.stderr)
        return 1
    print(f"{len(containers)} topology containers: no tmpfs; Go state uses disk storage")
    return 0


if __name__ == "__main__":
    sys.exit(main())
