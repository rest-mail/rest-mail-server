"""The contract phases' raised rate limits, and the switch the hooks read (#1979).

The E2E topology recreates its API with raised-limits.env for the contract
step and with its defaults again right after it, so the live proofs, the
Limits suite and the teardown see the real limits. The contract step sets
CONTRACT_RAISED_LIMITS=1, and while it is set the hooks do not pace requests to
stay under the defaults. Anywhere else, against default limits, they still do.

    python3 -B tests/contract/rate_limits.py check raised|default CONTAINER

reads the container's environment and fails when it is not what the name says,
or when the switch disagrees with it.
"""

import json
import os
import subprocess
import sys
import time
from pathlib import Path

SWITCH = "CONTRACT_RAISED_LIMITS"
RAISED_ENV = Path(__file__).resolve().with_name("raised-limits.env")


def raised():
    """Whether this process runs against the API started with raised-limits.env."""
    return os.environ.get(SWITCH) == "1"


def pace(seconds):
    """Wait for the default limit's next token, unless the limits are raised."""
    if not raised():
        time.sleep(seconds)


def settings(path=RAISED_ENV):
    """The KEY=VALUE pairs of an env file, without its comments."""
    pairs = {}
    for line in Path(path).read_text().splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        key, sep, value = line.partition("=")
        if not sep:
            raise ValueError(f"{path}: not KEY=VALUE: {line!r}")
        pairs[key] = value
    return pairs


def problems(expect, environment, switch=None, wanted=None):
    """What is wrong with a container environment (a list of KEY=VALUE strings)
    for the given expectation, "raised" or "default"; empty when nothing is."""
    wanted = settings() if wanted is None else wanted
    switch = os.environ.get(SWITCH) if switch is None else switch
    given = dict(entry.partition("=")[::2] for entry in environment)
    found = []
    if expect == "raised":
        if switch != "1":
            found.append(f"the API has raised limits expected, but {SWITCH} is not 1")
        for key, value in wanted.items():
            if given.get(key) != value:
                found.append(f"{key}: want {value!r}, the API has {given.get(key)!r}")
    elif expect == "default":
        if switch:
            found.append(f"{SWITCH} is set ({switch!r}) where the API must run on its defaults")
        for key in wanted:
            if key in given:
                found.append(f"{key}={given[key]} reached an API that must run on its defaults")
    else:
        raise ValueError(f"expect raised or default, not {expect!r}")
    return found


def container_environment(container):
    out = subprocess.run(
        ["docker", "inspect", "-f", "{{json .Config.Env}}", container],
        check=True, capture_output=True, text=True,
    ).stdout
    return json.loads(out) or []


def main(argv):
    if len(argv) != 3 or argv[0] != "check" or argv[1] not in ("raised", "default"):
        print(__doc__.strip(), file=sys.stderr)
        return 2
    found = problems(argv[1], container_environment(argv[2]))
    for problem in found:
        print(f"rate limits: {problem}", file=sys.stderr)
    if found:
        return 1
    print(f"rate limits: {argv[2]} runs with the {argv[1]} limits, and {SWITCH} agrees")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
