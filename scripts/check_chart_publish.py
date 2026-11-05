#!/usr/bin/env python3
"""Run the immutable chart-version gate locally or in CI (#1942)."""

import argparse
import os
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]


def check_version(registry, version):
    result = subprocess.run(
        ["helm", "show", "chart", registry, "--version", version],
        capture_output=True, text=True, timeout=60,
    )
    if result.returncode == 0:
        raise ValueError(
            f"chart {version} is already published — bump version in helm/restmail/Chart.yaml"
        )
    reference = registry.removeprefix("oci://") + ":" + version
    if result.stderr.strip() != f"Error: {reference}: not found":
        raise ValueError(
            f"could not establish whether chart {version} is published: "
            + (result.stderr.strip() or result.stdout.strip() or f"helm exited {result.returncode}")
        )
    print(f"{version} is unpublished")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--chart", default=str(ROOT / "helm/restmail"))
    parser.add_argument("--registry", default=os.environ.get("REGISTRY", "oci://ghcr.io/rest-mail/charts") + "/restmail")
    parser.add_argument("--version", default=os.environ.get("VERSION"))
    args = parser.parse_args(argv)
    try:
        version = args.version
        if not version:
            chart = subprocess.run(
                ["helm", "show", "chart", args.chart],
                capture_output=True, text=True, check=True, timeout=60,
            )
            versions = re.findall(r"^version: (\S+)$", chart.stdout, re.MULTILINE)
            if len(versions) != 1:
                raise ValueError(f"no unambiguous version in {args.chart}/Chart.yaml")
            version = versions[0]
        check_version(args.registry, version)
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        print(f"::error::{error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
