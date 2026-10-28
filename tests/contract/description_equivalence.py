#!/usr/bin/env python3
"""Prove a served description and its saved bytes compile identically.

This is an input/compilation gate, not evidence that API transactions passed.
The normal examples and coverage phases still supply the live API verdicts.
"""

import argparse
import hashlib
import json
import os
import subprocess
import sys
import tempfile
import urllib.request
from pathlib import Path

import yaml


def description_url(config, endpoint=""):
    return (endpoint or config["endpoint"]).rstrip("/") + "/api/docs/openapi.json"


def verify(config, saved, report, binary, mode="saved", endpoint=""):
    report = Path(report)
    report.unlink(missing_ok=True)
    if mode not in ("saved", "live"):
        raise ValueError("CONTRACT_DESCRIPTION_MODE must be saved or live")
    saved = Path(saved).resolve()
    url = description_url(config, endpoint)
    # API credentials intentionally stay out of this public-document request.
    with urllib.request.urlopen(url, timeout=30) as response:
        live_bytes = response.read(32 * 1024 * 1024 + 1)
    if len(live_bytes) > 32 * 1024 * 1024:
        raise ValueError("generated description exceeds 32 MiB")
    body = saved.read_bytes()
    if live_bytes != body:
        raise ValueError("the live description changed after saving; start a fresh run")
    spec = json.loads(body)
    if (
        not isinstance(spec, dict)
        or not str(spec.get("openapi", "")).startswith("3.")
        or not isinstance(spec.get("paths"), dict)
    ):
        raise ValueError("expected a generated OpenAPI 3 description")
    with tempfile.TemporaryDirectory(prefix="contract-inputs-") as directory:
        results = []
        for index, source in enumerate((str(saved), url)):
            output = Path(directory) / f"compiled-{index}.json"
            with output.open("wb") as stream:
                subprocess.run(
                    [binary, "compile", source], stdout=stream, check=True, timeout=180
                )
            results.append(json.loads(output.read_bytes()))
        if results[0] != results[1]:
            raise ValueError(
                "live and saved descriptions compile to different transactions or annotations"
            )
        transactions = results[0].get("transactions")
        if not isinstance(transactions, list) or not transactions:
            raise ValueError("the generated description compiled to no transactions")
        digest = hashlib.sha256(
            json.dumps(results[0], sort_keys=True, separators=(",", ":")).encode()
        ).hexdigest()
    evidence = {
        "status": "passed",
        "scope": "exact input bytes and complete compiled transactions/annotations; API verdicts are separate",
        "description_sha256": hashlib.sha256(body).hexdigest(),
        "compiled_sha256": digest,
        "transactions": len(transactions),
        "mode": mode,
    }
    temporary = report.with_suffix(".tmp")
    temporary.write_text(json.dumps(evidence, indent=2) + "\n")
    temporary.replace(report)
    return url if mode == "live" else str(saved)


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("config")
    parser.add_argument("saved")
    parser.add_argument("report")
    parser.add_argument("binary")
    parser.add_argument("--mode", choices=("saved", "live"), default="saved")
    args = parser.parse_args(argv)
    report = Path(args.report)
    report.unlink(missing_ok=True)
    with open(args.config) as stream:
        config = yaml.safe_load(stream)
    print(
        verify(
            config,
            args.saved,
            report,
            args.binary,
            args.mode,
            os.environ.get("CONTRACT_ENDPOINT", ""),
        )
    )


if __name__ == "__main__":
    main(sys.argv[1:])
