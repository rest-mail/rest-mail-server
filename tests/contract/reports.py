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
from pathlib import Path
import sys

import yaml


def configure(config, source_dir, base, narrowed=False, endpoint=""):
    for key in ("spec", "hookfiles"):
        if config.get(key):
            config[key] = str((Path(source_dir) / config[key]).resolve())
    if endpoint:
        config["endpoint"] = endpoint
    if narrowed:
        config.pop("surface", None)
    base = str(Path(base).resolve())
    config["reporter"] = ["cli", "junit", "html", "markdown"]
    config["output"] = ["", base + ".xml", base + ".html", base + ".md"]
    return config


def main(argv):
    args = [a for a in argv if a != "--narrowed"]
    if len(args) != 3:
        sys.exit("usage: reports.py <config.yml> <out.yml> <report base> [--narrowed]")
    source, out, base = args
    with open(source) as stream:
        config = yaml.safe_load(stream)
    configure(config, Path(source).resolve().parent, base, "--narrowed" in argv, os.environ.get("CONTRACT_ENDPOINT", ""))
    with open(out, "w") as stream:
        yaml.safe_dump(config, stream, sort_keys=False, allow_unicode=True)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
