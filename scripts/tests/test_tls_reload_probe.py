"""The real-listener reload probe keeps its protocol and publication checks strict."""

import importlib.util
import json
import socket
import ssl
import sys
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("tls_reload_probe", SCRIPTS / "tls_reload_probe.py")
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)


class TLSReloadProbeTests(unittest.TestCase):
    def test_dialogue_requires_protocol_completion_and_emits_command(self):
        client, server = socket.socketpair()
        with client, server:
            client.settimeout(1)
            server.sendall(b"250-feature\r\n250 complete\r\n")
            probe.dialogue(client, "EHLO reload.test", "250 ")
            self.assertEqual(server.recv(1024), b"EHLO reload.test\r\n")

    def test_premature_eof_is_not_a_successful_response(self):
        client, server = socket.socketpair()
        with client:
            server.close()
            with self.assertRaisesRegex(ValueError, "connection ended"):
                probe.dialogue(client, "", "250 ")

    def test_publish_preserves_original_permissions_and_owner(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "tls.key"
            path.write_bytes(b"old key")
            path.chmod(0o400)
            original = path.stat()
            probe.publish(path, b"renewed key", original)
            self.assertEqual(path.read_bytes(), b"renewed key")
            self.assertEqual(path.stat().st_mode & 0o777, 0o400)
            self.assertEqual(path.stat().st_uid, original.st_uid)

    def test_https_error_is_not_retained_traffic_success(self):
        client, server = socket.socketpair()
        with client, server:
            server.sendall(b"HTTP/1.1 500 Broken\r\nContent-Length: 0\r\n\r\n")
            with self.assertRaisesRegex(ValueError, "HTTPS listener answered 500"):
                probe.ping(client, "https")

    def run_topology(self, fresh_counts):
        topology = SimpleNamespace(validate=lambda name: None, names_for=lambda name: {
            "instance_project": "restmail-proof", "network": "network", "certs_volume": "certs",
        })
        container = {
            "Config": {"Labels": {"rest-mail-loop": "proof"}, "Env": [
                "GATEWAY_HOSTNAME=mail.test", "TLS_CERT_PATH=/certs/public.crt", "TLS_KEY_PATH=/certs/public.key",
                "INTERNAL_MTLS_SERVER_CERT=/certs/internal.crt", "INTERNAL_MTLS_SERVER_KEY=/certs/internal.key",
            ]},
            "State": {"Running": True, "StartedAt": "original"},
            "NetworkSettings": {"Networks": {"network": {"IPAddress": "192.0.2.1"}}},
            "Mounts": [{"Destination": "/certs", "Name": "certs"}],
        }
        probe_ran = False

        def run(command):
            if command[:2] == ["docker", "inspect"]:
                return SimpleNamespace(stdout=json.dumps([container]))
            if command[:2] == ["docker", "run"]:
                return SimpleNamespace(stdout="CA fixture")
            if command[:2] == ["openssl", "req"]:
                Path(command[command.index("-keyout") + 1]).write_text("key fixture")
                return SimpleNamespace()
            if command[:2] == ["openssl", "x509"]:
                Path(command[-1]).write_text(ssl.DER_cert_to_PEM_cert(b"cert fixture"))
                return SimpleNamespace()
            self.assertEqual(command[:2], ["docker", "logs"])
            service = command[-1].removeprefix("restmail-proof-")
            marker = "tls-front: " if service == "smtp-gateway-https" else "tls: "
            marker += "certificate reload failed\n"
            fresh = fresh_counts.get(service, 2) if probe_ran else 0
            return SimpleNamespace(stdout=marker * 2, stderr=marker * fresh)

        def run_helper(*args, **kwargs):
            nonlocal probe_ran
            probe_ran = True
            return SimpleNamespace(returncode=0)

        with patch.dict(sys.modules, {"topology_plan": topology}), patch.object(probe, "run", run), \
                patch.object(probe.subprocess, "run", run_helper):
            return probe.host("proof")

    def test_old_container_failures_do_not_count(self):
        with self.assertRaisesRegex(ValueError, "api: expected two new reload failures, observed 0"):
            self.run_topology({"api": 0})

    def test_only_one_new_front_failure_is_insufficient(self):
        with self.assertRaisesRegex(ValueError, "HTTPS front: expected two new reload failures, observed 1"):
            self.run_topology({"smtp-gateway-https": 1})

    def test_new_failures_on_every_container_pass(self):
        self.assertEqual(self.run_topology({}), 0)


if __name__ == "__main__":
    unittest.main()
