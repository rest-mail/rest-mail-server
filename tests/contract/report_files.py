"""Start a contract invocation without publishing an earlier run's reports."""

import argparse
from pathlib import Path

PHASES = (
    "examples",
    "coverage",
    "authorization",
    "ownership",
    "ownership-domains",
    "ownership-mailboxes",
    "fuzz",
    "browser-sessions",
)
SUFFIXES = (".xml", ".html", ".md", ".har", ".json")
RUN_FILES = (
    "openapi.json",
    "vertrag-run.yml",
    "vertrag-examples.yml",
    "vertrag-probes.yml",
    "vertrag-authorization.yml",
    "vertrag-ownership.yml",
    "vertrag-ownership-domains.yml",
    "vertrag-ownership-mailboxes.yml",
)


def reset_reports(directory, phases=None, reset_run=False):
    phases = PHASES if phases is None else tuple(phases)
    if set(phases) - set(PHASES):
        raise ValueError("unknown contract phase")
    directory = Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    (directory / "summary.md").unlink(missing_ok=True)
    for phase in phases:
        for suffix in SUFFIXES:
            (directory / (phase + suffix)).unlink(missing_ok=True)
    if reset_run:
        for name in RUN_FILES:
            (directory / name).unlink(missing_ok=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--reset-run", action="store_true")
    parser.add_argument("directory")
    parser.add_argument("phases", nargs="*")
    args = parser.parse_args()
    reset_reports(args.directory, args.phases or None, args.reset_run)
