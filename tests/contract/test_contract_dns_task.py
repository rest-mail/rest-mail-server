import importlib.util
import os
from pathlib import Path
import re
import shlex
import subprocess
import tempfile
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[2]


class ContractDNSTask(unittest.TestCase):
    def setUp(self):
        task = yaml.safe_load((ROOT / "chores.yml").read_text())["tasks"]["contract:dns:register"]
        values = {
            "TOPOLOGY_RUN": "c1-dns",
            "RESTMAIL_PROJECT": "restmail-c1-dns",
            "ROOT_DIR": str(ROOT),
            "FRAG_VOL": "restmail-c1-dns-tb_dnsmasq_fragments",
            "DNSMASQ_CONTAINER": "restmail-c1-dns-tb-dnsmasq",
        }
        self.command = re.sub(
            r'{{\.(\w+)(?: \| default "([^"]+)")?}}',
            lambda match: values.get(match[1], match[2] or ""),
            task["cmds"][0],
        )
        self.assertNotIn("{{", self.command)

    def test_helper_name_and_labels_satisfy_the_private_topology_guard(self):
        run_line = next(line for line in self.command.replace("\\\n", "").splitlines()
                        if line.strip().startswith("docker run "))
        arguments = shlex.split(run_line)[1:]
        self.assertEqual(arguments[arguments.index("--name") + 1], "restmail-c1-dns-contract-dns")
        self.assertIn("rest-mail-loop=c1-dns", arguments)
        specification = importlib.util.spec_from_file_location("dns_topology_guard", ROOT / "scripts/topology_docker.py")
        guard = importlib.util.module_from_spec(specification)
        specification.loader.exec_module(guard)
        guard.rewrite(arguments, "rest-mail-loop=c1-dns", "c1-dns", 42)

    def test_a_failed_fragment_copy_fails_the_task_before_reloading_dns(self):
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            docker = directory / "docker"
            docker.write_text('#!/bin/sh\nprintf "%s\\n" "$1" >> "$DNS_TEST_LOG"\n[ "$1" != run ] || exit 23\n')
            docker.chmod(0o755)
            environment = dict(os.environ, PATH=str(directory) + os.pathsep + os.environ["PATH"],
                               DNS_TEST_LOG=str(directory / "calls"), RESTMAIL_LABEL="rest-mail-loop=c1-dns")
            result = subprocess.run(["/bin/sh", "-c", self.command], env=environment,
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 23, result.stdout + result.stderr)
            self.assertEqual((directory / "calls").read_text().splitlines(), ["run"])
