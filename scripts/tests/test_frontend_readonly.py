"""Exercise the real frontend launch shells with a Docker stub."""

import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parents[2]


class FrontendReadonlyTests(unittest.TestCase):
    def test_dev_and_prod_launches(self):
        for app in ("webmail", "admin"):
            for mode in ("dev", "prod"):
                with self.subTest(app=app, mode=mode), tempfile.TemporaryDirectory() as temp:
                    directory = Path(temp)
                    docker = directory / "docker"
                    docker.write_text('''#!/usr/bin/env python3
import json, os, sys
with open(os.environ["CAPTURE"], "a") as output:
    output.write(json.dumps(sys.argv[1:]) + "\\n")
''')
                    docker.chmod(0o755)
                    source = (ROOT / "tasks" / (app + ".yml")).read_text()
                    up = source.split("\n  up:\n", 1)[1]
                    up = re.split(r"\n  [a-z][\w-]*:\n", up, maxsplit=1)[0]
                    # The final literal block contains the launch for both
                    # modes; the preceding Task deps step runs separately.
                    script = textwrap.dedent(up.split("      - |\n")[-1])
                    values = {app.upper() + "_MODE": mode}
                    script = re.sub(r"{{\.(\w+)}}", lambda match: values.get(
                        match[1], "fixture-" + match[1].lower()), script)
                    self.assertNotIn("{{", script)
                    capture = directory / "docker.jsonl"
                    env = dict(os.environ, PATH=temp + os.pathsep + os.environ["PATH"],
                               CAPTURE=str(capture), OUTPUT="/dev/null")
                    result = subprocess.run(["sh", "-eu"], input=script, cwd=temp,
                                            env=env, text=True, capture_output=True, timeout=10)
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    calls = [json.loads(line) for line in capture.read_text().splitlines()]
                    self.assertEqual(len(calls), 1)
                    args = calls[0]
                    self.assertEqual(args[:2], ["run", "-d"])
                    self.assertIn("no-new-privileges:true", args)
                    self.assertEqual(args[args.index("--cap-drop") + 1], "ALL")
                    self.assertEqual(args[args.index("--memory") + 1], "1g")
                    self.assertEqual(args[args.index("--pids-limit") + 1], "512")
                    self.assertEqual(args[args.index("--cpus") + 1], "1.5")
                    if mode == "dev":
                        self.assertIn("--read-only", args)
                        self.assertEqual(args[args.index("--tmpfs") + 1],
                                         "/tmp:rw,noexec,nosuid,nodev,size=128m,mode=1777")
                        mounts = [args[i + 1] for i, arg in enumerate(args) if arg == "-v"]
                        self.assertEqual(mounts, [f"{temp}/{app}:/app",
                                                 f"fixture-{app}_modules_volume:/app/node_modules"])
                        self.assertIn(f"docker-proxy.{app}.websocket=true", args)
                        self.assertIn(f"VITE_BASE_PATH=fixture-{app}_base_path", args)
                    else:
                        self.assertNotIn("--read-only", args)
                        self.assertNotIn("--tmpfs", args)
                        self.assertNotIn("-v", args)
                        self.assertIn(f"NGINX_BASE_PATH=fixture-{app}_base_path", args)
                    port = f"fixture-{app}_{mode}_port"
                    self.assertIn(f"docker-proxy.{app}.port={port}", args)


if __name__ == "__main__":
    unittest.main()
