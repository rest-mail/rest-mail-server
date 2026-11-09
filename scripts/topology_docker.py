#!/usr/bin/env python3
"""A `docker` for a private topology's tasks (#2062): every container, network and volume
it creates is named restmail-<run>-<purpose> and carries the run's label.

    PATH=<dir holding the shim>:$PATH chore ...      the shim is `exec python3 topology_docker.py "$@"`

scripts/topology_plan.py writes the shim into .cache/topology/<run>/bin/docker. It reads
RESTMAIL_LABEL (rest-mail-loop=<run>), TOPOLOGY_RUN (<run>) and TOPOLOGY_REAL_DOCKER.

What it changes, for `docker run`, `create`, `volume create` and `network create`:
- adds --label <RESTMAIL_LABEL>;
- names a container that has no --name `restmail-<run>-helper-<pid>`;
- reference-mailserver names its containers and volumes mailref-<CONFIG>-<service>, and the
  run's CONFIG already starts restmail-<run>-, so `mailref-restmail-` is read as `restmail-`
  in every argument of every command, which keeps one name for one thing;
- refuses a container, network or named volume whose name does not start restmail-<run>-,
  so nothing of the run can be named after the shared topology or left anonymous.
Every other command passes through with that one rewrite. A bind mount (a source that is a
path) is not a volume and is not checked.
"""

import os
import subprocess
import sys

# Options of `docker run` that take the next word as their value. Others are flags.
WITH_VALUE = {
    "-v", "--volume", "-e", "--env", "-l", "--label", "-p", "--publish", "-u", "--user", "-w", "--workdir",
    "-h", "--hostname", "-m", "--memory", "--name", "--network", "--net", "--ip", "--dns", "--entrypoint",
    "--cap-add", "--cap-drop", "--restart", "--env-file", "--mount", "--add-host", "--pid", "--platform",
    "--security-opt", "--tmpfs", "--health-cmd", "--health-interval", "--health-retries", "--health-timeout",
    "--health-start-period", "--log-driver", "--log-opt", "--link", "--device", "--ulimit", "--cpus",
    "--shm-size", "--group-add", "--label-file", "--network-alias", "--stop-signal", "--stop-timeout",
    "--user-ns", "--volumes-from", "--sysctl", "--ip6", "--mac-address", "--cidfile", "--pull", "--runtime",
}


class Refused(Exception):
    pass


def prefix(run):
    return f"restmail-{run}-"


def read_mailref(argument):
    return argument.replace("mailref-restmail-", "restmail-")


def check_name(kind, name, run):
    if not name.startswith(prefix(run)):
        raise Refused(f"{kind} {name!r} would not be named {prefix(run)}<purpose>")


def check_volume_source(source, run):
    if source and not source.startswith(("/", ".", "~")) and ":" not in source:
        check_name("volume", source, run)


def run_arguments(arguments, label, run, nonce):
    """The arguments after `run` or `create`, with the label, a name and the checks."""
    out, named, index = [], False, 0
    while index < len(arguments):
        word = arguments[index]
        if not word.startswith("-") or word == "-":
            break  # the image: everything after is the command
        option, equals, value = word.partition("=")
        takes_value = option in WITH_VALUE and not equals
        if option in ("-v", "--volume"):
            source = (value if equals else arguments[index + 1] if index + 1 < len(arguments) else "").split(":")[0]
            check_volume_source(source, run)
        if option == "--name":
            named = True
            name = value if equals else arguments[index + 1]
            check_name("container", name, run)
        if option == "--network" or option == "--net":
            network = value if equals else arguments[index + 1]
            if network not in ("none", "host", "bridge") and not network.startswith("container:"):
                check_name("network", network, run)
        out.append(word)
        if takes_value:
            index += 1
            out.append(arguments[index])
        index += 1
    if not named:
        out += ["--name", f"{prefix(run)}helper-{nonce}"]
    return ["--label", label, *out, *arguments[index:]]


def rewrite(arguments, label, run, nonce):
    """The arguments to give the real docker for the ones the shim was given."""
    arguments = [read_mailref(word) for word in arguments]
    if not arguments:
        return arguments
    first = arguments[0]
    if first in ("run", "create"):
        return [first, *run_arguments(arguments[1:], label, run, nonce)]
    if first in ("volume", "network") and arguments[1:2] == ["create"]:
        rest = arguments[2:]
        names = [word for word in rest if not word.startswith("-")]
        # The last word is the name; options with values are --label, --driver, --subnet and the like.
        flags_with_value = {"--label", "-l", "--driver", "-d", "--subnet", "--gateway", "--ip-range", "--opt", "-o", "--scope"}
        name = None
        skip = False
        for word in rest:
            if skip:
                skip = False
            elif word.partition("=")[0] in flags_with_value and "=" not in word:
                skip = True
            elif not word.startswith("-"):
                name = word
        if name is None:
            name = names[-1] if names else ""
        check_name(first, name, run)
        return [first, "create", "--label", label, *rest]
    return arguments


def main(argv):
    label, run, real = os.environ.get("RESTMAIL_LABEL", ""), os.environ.get("TOPOLOGY_RUN", ""), os.environ.get("TOPOLOGY_REAL_DOCKER", "")
    if not (label and run and real):
        print("topology docker: RESTMAIL_LABEL, TOPOLOGY_RUN and TOPOLOGY_REAL_DOCKER must be set", file=sys.stderr)
        return 2
    try:
        arguments = rewrite(argv, label, run, os.getpid())
    except Refused as error:
        print(f"topology docker: {error} (run {run})", file=sys.stderr)
        return 2
    return subprocess.call([real, *arguments])


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
