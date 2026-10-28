#!/usr/bin/env python3
"""Write a vertrag configuration for one phase of the contract run (#627).

The configuration is the one given, with its spec and hook paths made absolute so
it works from .cache/contract/, its reports written as JUnit, HTML and Markdown
beside each other (<base>.xml, <base>.html, <base>.md), the endpoint replaced by
CONTRACT_ENDPOINT when that is set, and, for a narrowed run, without the surface
gate, which is about the whole API.

Usage: reports.py <config.yml> <out.yml> <report base> [--narrowed]
"""

import os
import sys
from pathlib import Path
from urllib.parse import urlsplit

import yaml


def spec_location(value, directory):
    """HTTP descriptions stay URLs; only local descriptions become paths."""
    if urlsplit(value).scheme in ("http", "https"):
        return value
    return str((Path(directory) / value).resolve())


def configure(config, source_dir, base, narrowed=False, endpoint="", spec=""):
    if config.get("spec"):
        config["spec"] = spec_location(config["spec"], source_dir)
    if config.get("hookfiles"):
        config["hookfiles"] = str((Path(source_dir) / config["hookfiles"]).resolve())
    if spec:
        config["spec"] = spec_location(spec, Path.cwd())
    if endpoint:
        config["endpoint"] = endpoint
    if narrowed:
        config.pop("surface", None)
    base = str(Path(base).resolve())
    # The HAR is the run's traffic, which the secret scan reads (#673).
    config["reporter"] = ["cli", "junit", "html", "markdown", "har", "json"]
    config["output"] = [
        "",
        base + ".xml",
        base + ".html",
        base + ".md",
        base + ".har",
        base + ".json",
    ]
    return config


def main(argv):
    args = [a for a in argv if a != "--narrowed"]
    if len(args) != 3:
        sys.exit("usage: reports.py <config.yml> <out.yml> <report base> [--narrowed]")
    source, out, base = args
    with open(source) as stream:
        config = yaml.safe_load(stream)
    configure(
        config,
        Path(source).resolve().parent,
        base,
        "--narrowed" in argv,
        os.environ.get("CONTRACT_ENDPOINT", ""),
        os.environ.get("CONTRACT_RUN_SPEC", os.environ.get("CONTRACT_SPEC", "")),
    )
    with open(out, "w") as stream:
        yaml.safe_dump(config, stream, sort_keys=False, allow_unicode=True)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
