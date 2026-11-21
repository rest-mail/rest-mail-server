"""The real-listener reload probe keeps its protocol and publication checks strict."""

import importlib.util
import socket
import tempfile
import unittest
from pathlib import Path

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


if __name__ == "__main__":
    unittest.main()
