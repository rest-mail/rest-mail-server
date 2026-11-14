"""Raised contract throttles, pacing policy, and the default-limits guard."""

import json
import os
import subprocess
import sys
import time
from pathlib import Path


DEFAULTS = {
    "AUTH_RATE_LIMIT_ENABLED": "true",
    "AUTH_RATE_LIMIT_RPS": "1",
    "AUTH_RATE_LIMIT_BURST": "15",
    "PIPELINE_TEST_RATE_LIMIT_ENABLED": "true",
    "PIPELINE_TEST_RATE_LIMIT_RPS": "2",
    "PIPELINE_TEST_RATE_LIMIT_BURST": "10",
    "TLSRPT_RATE_LIMIT_ENABLED": "true",
    "TLSRPT_RATE_LIMIT_RPS": "1",
    "TLSRPT_RATE_LIMIT_BURST": "10",
}


SWITCH = "RESTMAIL_CONTRACT_LIMITS_RAISED"
RAISED_ENV = Path(__file__).resolve().with_name("raised-limits.env")


def raised():
    return os.environ.get(SWITCH) == "true"


def settings(path=RAISED_ENV):
    pairs = {}
    for line in Path(path).read_text().splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        key, separator, value = line.partition("=")
        if not separator:
            raise ValueError(f"{path}: not KEY=VALUE: {line!r}")
        pairs[key] = value
    return pairs


def problems(expect, environment, switch=None, wanted=None):
    wanted = settings() if wanted is None else wanted
    switch = os.environ.get(SWITCH) if switch is None else switch
    given = dict(entry.split("=", 1) for entry in environment if "=" in entry)
    found = []
    if expect == "raised":
        if switch != "true":
            found.append(f"{SWITCH} must be true for raised API limits")
        for key, value in wanted.items():
            if given.get(key) != value:
                found.append(f"{key} does not match the raised setting")
    elif expect == "default":
        if switch not in (None, "", "false"):
            found.append(f"{SWITCH} reached a default-limit phase")
        for key, value in DEFAULTS.items():
            if given.get(key, value) != value:
                found.append(f"Limits suite requires the default {key}")
    else:
        raise ValueError(f"expect raised or default, not {expect!r}")
    return found


def container_environment(container):
    result = subprocess.run(
        ["docker", "inspect", "-f", "{{json .Config.Env}}", container],
        check=True, capture_output=True, text=True,
    )
    return json.loads(result.stdout) or []


def pace(seconds):
    if not raised():
        time.sleep(seconds)


def docker_flags():
    return " ".join(
        f"-e {name}={value}" for name, value in settings().items()
    )


def assert_defaults(containers):
    environment = dict(
        entry.split("=", 1) for entry in containers[0]["Config"]["Env"] if "=" in entry
    )
    for name, default in DEFAULTS.items():
        if environment.get(name, default) != default:
            raise ValueError(f"Limits suite requires the default {name}")


def main(arguments):
    if arguments == ["--docker-flags"]:
        print(docker_flags())
    elif arguments == ["--assert-defaults"]:
        assert_defaults(json.load(sys.stdin))
        print("Limits suite API uses the default rate limits")
    elif len(arguments) == 3 and arguments[0] == "check":
        found = problems(arguments[1], container_environment(arguments[2]))
        for problem in found:
            print(f"rate limits: {problem}", file=sys.stderr)
        return 1 if found else 0
    else:
        raise ValueError("expected --docker-flags, --assert-defaults, or check raised|default CONTAINER")


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
