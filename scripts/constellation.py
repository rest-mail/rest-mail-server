#!/usr/bin/env python3
"""List the REST Mail family and its open issues without modifying any repo."""

import argparse
import json
from pathlib import Path
import subprocess
import sys

MANIFEST = Path(__file__).resolve().parents[1] / "constellation.json"


class InventoryError(Exception):
    pass


def gh_json(*args):
    try:
        result = subprocess.run(
            ["gh", *args], check=True, capture_output=True, text=True, timeout=60
        )
        payload = json.loads(result.stdout)
    except (OSError, subprocess.SubprocessError, ValueError) as error:
        raise InventoryError("GitHub inventory failed: %s" % error) from error
    if isinstance(payload, dict) and payload.get("errors"):
        raise InventoryError("GitHub returned errors: %s" % payload["errors"])
    return payload


def graphql(query, **variables):
    args = ["api", "graphql", "-f", "query=" + query]
    for name, value in variables.items():
        if value is not None:
            args.extend(["-f", name + "=" + value])
    return gh_json(*args)["data"]


def load_manifest(path=MANIFEST):
    with open(path, encoding="utf-8") as source:
        manifest = json.load(source)
    if manifest.get("schemaVersion") != 1:
        raise InventoryError("unsupported constellation manifest version")
    organizations = manifest["membership"]["githubOrganizations"]
    if not organizations or len(set(organizations)) != len(organizations):
        raise InventoryError("membership needs unique GitHub organizations")
    seen = set()
    for member in manifest["repositories"]:
        name = member["repository"]
        if name in seen or name.split("/")[0] not in organizations:
            raise InventoryError("duplicate or out-of-scope core repository: " + name)
        seen.add(name)
    for related in manifest["relatedRepositories"]:
        if not related.get("scope") or not isinstance(related.get("issues"), list):
            raise InventoryError("related repositories need an explicit scope and issue list")
        if any(type(number) is not int or number < 1 for number in related["issues"]):
            raise InventoryError("related issue numbers must be positive integers")
    return manifest


def live_repositories(manifest):
    known = {r["repository"]: r for r in manifest["repositories"]}
    found = []
    query = """query($organization: String!, $cursor: String) {
      organization(login: $organization) {
        repositories(first: 100, after: $cursor) {
          nodes { nameWithOwner url isArchived }
          pageInfo { hasNextPage endCursor }
        }
      }
    }"""
    for organization in manifest["membership"]["githubOrganizations"]:
        cursor = None
        while True:
            result = graphql(query, organization=organization, cursor=cursor)
            page = result["organization"]["repositories"]
            for repo in page["nodes"]:
                found.append({
                    "repository": repo["nameWithOwner"],
                    "role": known.get(repo["nameWithOwner"], {}).get("role", "unclassified"),
                    "url": repo["url"],
                    "archived": repo["isArchived"],
                })
            if not page["pageInfo"]["hasNextPage"]:
                break
            next_cursor = page["pageInfo"]["endCursor"]
            if not next_cursor or next_cursor == cursor:
                raise InventoryError("GitHub repository pagination did not advance")
            cursor = next_cursor
    return sorted(found, key=lambda r: r["repository"])


def repository_issues(repository):
    owner, name = repository.split("/", 1)
    query = """query($owner: String!, $name: String!, $cursor: String) {
      repository(owner: $owner, name: $name) {
        issues(first: 100, states: OPEN, after: $cursor) {
          nodes { number title url }
          pageInfo { hasNextPage endCursor }
        }
      }
    }"""
    cursor, issues = None, []
    while True:
        page = graphql(query, owner=owner, name=name, cursor=cursor)["repository"]["issues"]
        issues.extend(page["nodes"])
        if not page["pageInfo"]["hasNextPage"]:
            return sorted(issues, key=lambda issue: issue["number"])
        next_cursor = page["pageInfo"]["endCursor"]
        if not next_cursor or next_cursor == cursor:
            raise InventoryError("GitHub issue pagination did not advance: " + repository)
        cursor = next_cursor


def inventory(manifest, include_related=False):
    repositories = live_repositories(manifest)
    output = []
    for repo in repositories:
        output.append({**repo, "scope": "core", "issues": repository_issues(repo["repository"])})
    if include_related:
        for related in manifest["relatedRepositories"]:
            # A related project never contributes its entire backlog. Other
            # forges stay documented until specific issue IDs are registered.
            allowed = set(related["issues"])
            if not allowed:
                continue
            if related["host"] != "github.com":
                raise InventoryError("issue inventory is not implemented for " + related["host"])
            issues = [i for i in repository_issues(related["repository"]) if i["number"] in allowed]
            output.append({
                **related,
                "scopeDescription": related["scope"],
                "scope": "related",
                "issues": issues,
            })
    return output


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["repos", "issues"])
    parser.add_argument("--manifest", type=Path, default=MANIFEST)
    parser.add_argument("--live", action="store_true", help="discover current organization membership")
    parser.add_argument("--include-related", action="store_true", help="include only registered related issues")
    parser.add_argument("--json", action="store_true", help="write machine-readable inventory")
    args = parser.parse_args(argv)
    try:
        manifest = load_manifest(args.manifest)
        if args.command == "repos":
            members = live_repositories(manifest) if args.live else manifest["repositories"]
            output = [{**r, "scope": "core"} for r in members]
            if args.include_related:
                output.extend(
                    {**r, "scopeDescription": r["scope"], "scope": "related"}
                    for r in manifest["relatedRepositories"]
                )
        else:
            # Issue work always discovers live membership, including new org
            # repos that have not received a descriptive manifest entry yet.
            output = inventory(manifest, args.include_related)
        if args.json:
            print(json.dumps(output, indent=2))
        elif args.command == "repos":
            for repo in output:
                print("%s [%s]: %s" % (repo["repository"], repo["scope"], repo["role"]))
        else:
            count = sum(len(r["issues"]) for r in output)
            print("%d open issues across %d repositories" % (count, len(output)))
            for repo in output:
                for issue in repo["issues"]:
                    print("%s#%d [%s]: %s" % (repo["repository"], issue["number"], repo["scope"], issue["title"]))
        return 0
    except (InventoryError, KeyError, ValueError, TypeError, OSError) as error:
        print("constellation: " + str(error), file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
