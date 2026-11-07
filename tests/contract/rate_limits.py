"""Raised contract throttles, pacing policy, and the default-limits guard."""

import json
import os
import sys
import time


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


def pace(seconds):
    if os.environ.get("RESTMAIL_CONTRACT_LIMITS_RAISED") != "true":
        time.sleep(seconds)


def docker_flags():
    return " ".join(
        f"-e {name}={'true' if name.endswith('_ENABLED') else '10000'}"
        for name in DEFAULTS
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
    else:
        raise ValueError("expected --docker-flags or --assert-defaults")


if __name__ == "__main__":
    main(sys.argv[1:])
