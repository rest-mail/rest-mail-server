"""Opt-in reset/seed primitive for hooks on one disposable restmail.test API.

No hook is registered here. Calls must be serialized with exchanges: this resets
database fixtures, not API rate limits, event streams, or delivery workers.
The existing reset retains administrators but replaces ordinary account IDs;
ordinary sessions and account IDs must not be cached across calls.
"""

import argparse
import json
import os
import statistics
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

FIXTURE_FLAGS = {
    "queue": (),
    "link-account": ("--link-account-only",),
    "domain-tenants": ("--domain-tenants",),
    "mfa-admin": ("--mfa-admin",),
    "api-keys": ("--api-keys",),
}


def reset_and_seed(origin, token, *, api_container, fixtures=("queue",), timeout=60):
    """Reset, seed, then run the existing Go fixture command in the API container.

    Pass the surviving fixture administrator's JWT and the same API container
    that serves origin. The default recreates the retry/bounce queue, quarantine
    and suppression fixtures; select existing optional fixture modes as needed.
    Outputs (including domain-tenant IDs) and wall-clock timings are returned.
    Any failed step raises RuntimeError; no exchange should run after that.
    Reset already seeds internally; the explicit, idempotent seed call also
    exercises seedTestData, and is included in the measured cost.
    """
    started = time.perf_counter()
    target = urllib.parse.urlsplit(origin)
    if (
        target.scheme not in ("http", "https")
        or not target.netloc
        or target.path not in ("", "/")
        or target.query
        or target.fragment
        or target.username is not None
        or not token
        or not api_container
        or timeout <= 0
    ):
        raise ValueError(
            "reset/seed requires an API origin, admin token, container and positive timeout"
        )
    fixtures = tuple(fixtures)
    if (
        not fixtures
        or len(set(fixtures)) != len(fixtures)
        or any(mode not in FIXTURE_FLAGS for mode in fixtures)
    ):
        raise ValueError("select distinct existing contract fixture modes")
    with (
        Path(__file__).resolve().parent / "fixtureplan" / "defaults.json"
    ).open() as source:
        plan = json.load(source)
    if plan["domain"] != "restmail.test":
        raise ValueError(
            "reset/seed requires the disposable restmail.test fixture plan"
        )

    def send(operation, path, expected):
        request = urllib.request.Request(
            origin.rstrip("/") + path,
            method="POST",
            data=b"",
            headers={"Authorization": "Bearer " + token},
        )
        try:
            with urllib.request.urlopen(request, timeout=timeout) as response:
                if response.status != 200:
                    raise RuntimeError(f"{operation} failed: HTTP {response.status}")
                body = json.load(response)
        except urllib.error.HTTPError as error:
            error.close()
            raise RuntimeError(f"{operation} failed: HTTP {error.code}") from None
        except (OSError, ValueError) as error:
            raise RuntimeError(f"{operation} failed: {type(error).__name__}") from None
        if (
            not isinstance(body, dict)
            or not isinstance(body.get("data"), dict)
            or body["data"].get("status") != expected
        ):
            raise RuntimeError(f"{operation} did not return {expected}")

    send("resetTestData", "/api/v1/admin/test/reset", "reset_complete")
    reset_at = time.perf_counter()
    send("seedTestData", "/api/v1/admin/test/seed", "seed_complete")
    seed_at = time.perf_counter()
    output = {}
    for mode in fixtures:
        command = [
            "docker",
            "exec",
            "--",
            api_container,
            "go",
            "run",
            "./tests/contract/fixtures",
            "--test-domain=" + plan["domain"],
            *FIXTURE_FLAGS[mode],
        ]
        try:
            result = subprocess.run(
                command, check=True, capture_output=True, text=True, timeout=timeout
            )
        except (OSError, subprocess.SubprocessError) as error:
            detail = (
                f"exit {error.returncode}"
                if isinstance(error, subprocess.CalledProcessError)
                else type(error).__name__
            )
            raise RuntimeError(f"contract fixture {mode} failed: {detail}") from None
        output[mode] = result.stdout
    finished = time.perf_counter()
    return {
        "reset_seconds": reset_at - started,
        "seed_seconds": seed_at - reset_at,
        "fixtures_seconds": finished - seed_at,
        "elapsed_seconds": finished - started,
        "fixture_output": output,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--origin", required=True)
    parser.add_argument("--api-container", required=True)
    parser.add_argument(
        "--fixtures", nargs="+", choices=tuple(FIXTURE_FLAGS), default=["queue"]
    )
    parser.add_argument("--runs", type=int, default=5)
    args = parser.parse_args()
    if args.runs < 1:
        parser.error("--runs must be positive")
    token = os.environ.get("CONTRACT_RESET_TOKEN", "")
    if not token:
        parser.error("set CONTRACT_RESET_TOKEN to the fixture administrator's JWT")
    samples = [
        reset_and_seed(
            args.origin, token, api_container=args.api_container, fixtures=args.fixtures
        )
        for _ in range(args.runs)
    ]
    elapsed = [sample["elapsed_seconds"] for sample in samples]
    print(
        json.dumps(
            {
                "samples": [
                    {
                        key: value
                        for key, value in sample.items()
                        if key != "fixture_output"
                    }
                    for sample in samples
                ],
                "median_seconds": statistics.median(elapsed),
                "worst_seconds": max(elapsed),
                "exchanges": 600,
                "median_job_seconds": statistics.median(elapsed) * 600,
                "worst_job_seconds": max(elapsed) * 600,
            },
            indent=2,
        )
    )


if __name__ == "__main__":
    main()
