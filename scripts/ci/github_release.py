#!/usr/bin/env python3
"""Fetch a release asset through GitHub's API when the download frontend fails."""

import json
import os
from pathlib import Path
import re
import sys
import urllib.parse
import urllib.request


def release_asset(url):
    match = re.fullmatch(
        r"https://github\.com/([^/]+)/([^/]+)/releases/download/([^/?#]+)/([^/?#]+)",
        url,
    )
    return match.groups() if match else None


def request_bytes(url, accept):
    request = urllib.request.Request(
        url, headers={"User-Agent": "rest-mail-server pinned-tools", "Accept": accept}
    )
    token = os.environ.get("GITHUB_TOKEN")
    if token:
        request.add_unredirected_header("Authorization", "Bearer " + token)
    with urllib.request.urlopen(request, timeout=60) as response:
        return response.read()


def fetch_asset(url):
    coordinates = release_asset(url)
    if not coordinates:
        raise ValueError("not a GitHub release download URL")
    owner, repository, tag, name = coordinates
    base = f"https://api.github.com/repos/{owner}/{repository}/releases"
    release = json.loads(request_bytes(f"{base}/tags/{tag}", "application/vnd.github+json"))
    assets = [asset for asset in release["assets"] if asset["name"] == urllib.parse.unquote(name)]
    if len(assets) != 1 or type(assets[0]["id"]) is not int or assets[0]["id"] <= 0:
        raise ValueError(f"release {tag} has no unique asset named {name}")
    return request_bytes(f"{base}/assets/{assets[0]['id']}", "application/octet-stream")


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit("usage: github_release.py RELEASE_URL ARCHIVE")
    Path(sys.argv[2]).write_bytes(fetch_asset(sys.argv[1]))
