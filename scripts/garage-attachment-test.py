#!/usr/bin/env python3
"""Run attachment integration checks against a disposable, real Garage node."""
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


def main():
    name = "restmail-garage-test-" + secrets.token_hex(5)
    token = secrets.token_hex(32)
    access = "GK" + secrets.token_hex(12)
    secret = secrets.token_hex(32)
    with tempfile.TemporaryDirectory(prefix="restmail-garage-") as directory:
        config = Path(directory) / "garage.toml"
        config.write_text('metadata_dir = "/data/meta"\ndata_dir = "/data/objects"\n'
                          'db_engine = "lmdb"\nreplication_factor = 1\n'
                          'rpc_bind_addr = "[::]:3901"\nrpc_public_addr = "127.0.0.1:3901"\n'
                          'rpc_secret = "' + secrets.token_hex(32) + '"\n'
                          '[s3_api]\ns3_region = "garage"\napi_bind_addr = "[::]:3900"\n'
                          '[admin]\napi_bind_addr = "[::]:3903"\nadmin_token = "' + token + '"\n')
        config.chmod(0o600)
        try:
            subprocess.run(["docker", "run", "--detach", "--name", name,
                            "-p", "127.0.0.1:3900:3900", "-p", "127.0.0.1:3903:3903",
                            "-v", str(config) + ":/etc/garage.toml:ro", "--entrypoint", "/garage", "dxflrs/garage:v2.4.1", "server"], check=True)

            def api(method, path, body=None):
                request = urllib.request.Request("http://127.0.0.1:3903/v2/" + path,
                    data=json.dumps(body).encode() if body is not None else None, method=method,
                    headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"})
                with urllib.request.urlopen(request, timeout=5) as response:
                    raw = response.read()
                    return json.loads(raw) if raw else {}

            deadline = time.monotonic() + 60
            while True:
                try:
                    nodes = api("GET", "GetClusterStatus")["nodes"]
                    if nodes:
                        break
                except (OSError, ValueError):
                    if time.monotonic() >= deadline:
                        raise RuntimeError("Garage admin API did not become ready")
                time.sleep(1)
            api("POST", "UpdateClusterLayout", {"roles": [{"id": nodes[0]["id"], "zone": "test", "capacity": 1_000_000_000, "tags": ["ci"]}]})
            version = api("GET", "GetClusterLayout")["version"] + 1
            api("POST", "ApplyClusterLayout", {"version": version})
            bucket = api("POST", "CreateBucket", {"globalAlias": "attachments-test"})
            api("POST", "CreateBucket", {"globalAlias": "attachments-denied"})
            api("POST", "ImportKey", {"accessKeyId": access, "secretAccessKey": secret, "name": "attachments-test"})
            api("POST", "AllowBucketKey", {"bucketId": bucket["id"], "accessKeyId": access,
                "permissions": {"read": True, "write": True, "owner": False}})
            env = dict(os.environ, RESTMAIL_TEST_GARAGE_ENDPOINT="http://127.0.0.1:3900",
                       RESTMAIL_TEST_GARAGE_ACCESS_KEY=access, RESTMAIL_TEST_GARAGE_SECRET_KEY=secret,
                       RESTMAIL_REQUIRE_GARAGE="1", GOMAXPROCS="1")
            subprocess.run(["go", "test", "-p", "1", "./internal/attachmentstore", "-run", "^TestS3Garage$", "-count=1", "-v"], env=env, check=True, timeout=240)
        except Exception:
            subprocess.run(["docker", "logs", "--tail", "60", name], check=False)
            raise
        finally:
            subprocess.run(["docker", "rm", "-f", "-v", name], check=False)


if __name__ == "__main__":
    main()
