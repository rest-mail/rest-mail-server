#!/usr/bin/env python3
"""Give a Garage node its layout, a bucket and a key to reach it, if it lacks them.

Safe to run again: what is already there is left alone. It uses only Garage's admin API.
"""
import argparse
import json
import sys
import time
import urllib.error
import urllib.request

CAPACITY = 100_000_000_000


class Garage:
    def __init__(self, admin, token):
        self.admin = admin.rstrip("/")
        self.token = token

    def call(self, method, path, body=None):
        request = urllib.request.Request(
            self.admin + "/v2/" + path,
            data=json.dumps(body).encode() if body is not None else None,
            method=method,
            headers={"Authorization": "Bearer " + self.token, "Content-Type": "application/json"},
        )
        with urllib.request.urlopen(request, timeout=5) as response:
            raw = response.read()
            return json.loads(raw) if raw else {}


def wait_for_nodes(garage, seconds=60):
    deadline = time.monotonic() + seconds
    while True:
        try:
            nodes = garage.call("GET", "GetClusterStatus")["nodes"]
            if nodes:
                return nodes
        except (OSError, ValueError, KeyError) as problem:
            if time.monotonic() >= deadline:
                raise SystemExit(f"Garage's admin API did not become ready: {problem}")
        if time.monotonic() >= deadline:
            raise SystemExit("Garage reports no nodes")
        time.sleep(1)


def ensure_layout(garage, nodes):
    layout = garage.call("GET", "GetClusterLayout")
    if layout.get("roles"):
        return
    garage.call("POST", "UpdateClusterLayout", {
        "roles": [{"id": nodes[0]["id"], "zone": "local", "capacity": CAPACITY, "tags": []}],
    })
    garage.call("POST", "ApplyClusterLayout", {"version": layout["version"] + 1})


def ensure_bucket(garage, alias):
    for bucket in garage.call("GET", "ListBuckets"):
        if alias in bucket.get("globalAliases", []):
            return bucket["id"]
    return garage.call("POST", "CreateBucket", {"globalAlias": alias})["id"]


def ensure_key(garage, access_key, secret_key, bucket_id):
    if not any(key.get("id") == access_key for key in garage.call("GET", "ListKeys")):
        garage.call("POST", "ImportKey", {"accessKeyId": access_key, "secretAccessKey": secret_key, "name": "restmail"})
    garage.call("POST", "AllowBucketKey", {
        "bucketId": bucket_id, "accessKeyId": access_key,
        "permissions": {"read": True, "write": True, "owner": False},
    })


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--admin", required=True)
    parser.add_argument("--admin-token", required=True)
    parser.add_argument("--bucket", required=True)
    parser.add_argument("--access-key", required=True)
    parser.add_argument("--secret-key", required=True)
    args = parser.parse_args(argv)
    garage = Garage(args.admin, args.admin_token)
    try:
        ensure_layout(garage, wait_for_nodes(garage))
        ensure_key(garage, args.access_key, args.secret_key, ensure_bucket(garage, args.bucket))
    except urllib.error.HTTPError as problem:
        # The answer may repeat what was sent, so only its status is printed.
        sys.exit(f"Garage's admin API refused {problem.url}: {problem.code}")
    print(f"garage ready: bucket {args.bucket}")


if __name__ == "__main__":
    main()
