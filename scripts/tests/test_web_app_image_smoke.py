"""Run CI's web app image step against a stand-in for Docker's published port (#1960).

A published port answers before the server in the container listens. docker-proxy
accepts the connection and resets it, and curl does not count that as a refused
connection, so --retry-connrefused gives up on the first reset. Nothing guarded
the first request, so the step passed only while nginx won that race.
"""

import http.server
import os
from pathlib import Path
import socket
import socketserver
import stat
import struct
import subprocess
import tempfile
import textwrap
import threading
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[2]
STEP = "Build and serve with the chart's security settings"
READY_AFTER_POLLS = 3
INDEX = b'<!doctype html><html><head><base href="/"><script src="/assets/app.js"></script></head></html>'


def web_app_step():
    workflow = yaml.safe_load((ROOT / ".github/workflows/ci.yml").read_text())
    for job in workflow["jobs"].values():
        for step in job.get("steps", []):
            if step.get("name") == STEP:
                return step["run"]
    raise AssertionError(f"ci.yml has no step named {STEP!r}")


class PublishedPort(socketserver.ThreadingMixIn, http.server.HTTPServer):
    """Resets every connection until the container's server is up, as docker-proxy does."""

    daemon_threads = True

    def __init__(self, polls, reset_paths=()):
        super().__init__(("127.0.0.1", 0), Handler)
        self.polls = polls
        self.reset_paths = reset_paths
        self.up = False

    def get_request(self):
        connection, address = super().get_request()
        self.up = self.polls.exists() and len(self.polls.read_text()) >= READY_AFTER_POLLS
        if not self.up:
            reset(connection)
            raise OSError("reset before the server listened")
        return connection, address

    def handle_error(self, request, client_address):
        """A reset is what this server is for, so it logs nothing."""


def reset(connection):
    connection.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER, struct.pack("ii", 1, 0))
    connection.close()


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path in self.server.reset_paths:
            reset(self.connection)
            self.close_connection = True
            return
        script = self.path.endswith(".js")
        body = b"export {};\n" if script else INDEX
        self.send_response(200)
        self.send_header("Content-Type", "text/javascript" if script else "text/html")
        self.send_header("Content-Security-Policy", "default-src 'self'; connect-src 'self'")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        """The test reads the step's exit status, not a request log."""


class WebAppImageStep(unittest.TestCase):
    def setUp(self):
        scratch = tempfile.TemporaryDirectory()
        self.addCleanup(scratch.cleanup)
        self.scratch = Path(scratch.name)
        self.bin = self.scratch / "bin"
        self.bin.mkdir()
        self.polls = self.scratch / "polls"

    def run_step(self, comes_up=True, reset_paths=()):
        server = PublishedPort(self.polls, reset_paths)
        self.addCleanup(server.server_close)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.shutdown)
        self.stub("docker", f"""
            case "$1" in
            build) ;;
            run) echo stand-in-container ;;
            port) echo 127.0.0.1:{server.server_address[1]} ;;
            exec) {"printf x >> " + str(self.polls) + '; [ "$(wc -c < ' + str(self.polls) + f')" -ge {READY_AFTER_POLLS} ]' if comes_up else "exit 1"} ;;
            logs|rm) ;;
            *) echo "unexpected docker $*" >&2; exit 2 ;;
            esac
        """)
        self.stub("sleep", "exit 0")
        env = dict(os.environ, APP="webmail", RUNNER_TEMP=str(self.scratch),
                   PATH=f"{self.bin}{os.pathsep}{os.environ['PATH']}")
        return subprocess.run(["bash", "-c", web_app_step()], cwd=self.scratch, env=env,
                              capture_output=True, text=True, timeout=600)

    def stub(self, name, body):
        path = self.bin / name
        path.write_text("#!/bin/sh\n" + textwrap.dedent(body))
        path.chmod(path.stat().st_mode | stat.S_IXUSR)

    def test_a_reset_before_nginx_listens_is_not_a_failure(self):
        result = self.run_step()
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_nginx_that_never_comes_up_fails_the_step(self):
        self.assertNotEqual(self.run_step(comes_up=False).returncode, 0)

    def test_a_reset_once_nginx_listens_still_fails_the_step(self):
        for path in ("/", "/login", "/assets/app.js"):
            with self.subTest(path=path):
                self.polls.unlink(missing_ok=True)
                result = self.run_step(reset_paths=(path,))
                self.assertNotEqual(result.returncode, 0, "a reset from a running nginx passed")

    def test_no_request_retries_so_a_fault_cannot_hide(self):
        self.assertNotIn("--retry", web_app_step())


if __name__ == "__main__":
    unittest.main()
