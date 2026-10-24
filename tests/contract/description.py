#!/usr/bin/env python3
"""Save the generated description served by the contract run's endpoint."""
import json
import os
from pathlib import Path
import sys
import urllib.request

import yaml


def fetch(config, destination, endpoint=""):
    destination = Path(destination)
    # A failed fetch must not leave a previous run's description available.
    destination.unlink(missing_ok=True)
    url = (endpoint or config["endpoint"]).rstrip("/") + "/api/docs/openapi.json"
    with urllib.request.urlopen(url, timeout=30) as response:
        body = response.read()
    document = json.loads(body)
    if not isinstance(document, dict) or not str(document.get("openapi", "")).startswith("3.") \
            or not isinstance(document.get("paths"), dict):
        raise ValueError("the contract endpoint did not serve an OpenAPI 3 description")
    temporary = destination.with_suffix(".tmp")
    temporary.write_bytes(body)
    temporary.replace(destination)


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit("usage: description.py <vertrag.yml> <openapi.json>")
    with open(sys.argv[1]) as source:
        fetch(yaml.safe_load(source), sys.argv[2], os.environ.get("CONTRACT_ENDPOINT", ""))
