"""Reset/seed orchestration fails closed before the next contract exchange."""

import json
import subprocess
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest.mock import patch

import reset_seed


class ResetSeedTest(unittest.TestCase):
    def setUp(self):
        self.requests = []
        self.responses = [
            (200, {"data": {"status": "reset_complete"}}),
            (200, {"data": {"status": "seed_complete"}}),
        ]
        case = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                case.requests.append((self.path, self.headers.get("Authorization")))
                status, body = case.responses.pop(0)
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(
                    body if isinstance(body, bytes) else json.dumps(body).encode()
                )

            def log_message(self, *args):
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(thread.join)
        self.addCleanup(server.shutdown)
        self.origin = "http://127.0.0.1:" + str(server.server_port)
        self.runner = self.enterContext(patch.object(reset_seed.subprocess, "run"))
        self.runner.return_value = subprocess.CompletedProcess(
            [], 0, "fixtures prepared\n", ""
        )

    def prepare(self, **kwargs):
        return reset_seed.reset_and_seed(
            self.origin, "fixture-jwt", api_container="isolated-api", **kwargs
        )

    def test_reset_then_seed_then_existing_fixtures(self):
        def run(command, **kwargs):
            self.assertEqual(len(self.requests), 2)
            self.assertEqual(
                command,
                [
                    "docker",
                    "exec",
                    "--",
                    "isolated-api",
                    "go",
                    "run",
                    "./tests/contract/fixtures",
                    "--test-domain=restmail.test",
                ],
            )
            self.assertTrue(kwargs["check"])
            self.assertEqual(kwargs["timeout"], 60)
            return subprocess.CompletedProcess(command, 0, "fixtures prepared\n", "")

        self.runner.side_effect = run
        with patch.object(reset_seed.time, "perf_counter", side_effect=[0, 1, 3, 6]):
            result = self.prepare()
        self.assertEqual(
            self.requests,
            [
                ("/api/v1/admin/test/reset", "Bearer fixture-jwt"),
                ("/api/v1/admin/test/seed", "Bearer fixture-jwt"),
            ],
        )
        self.assertEqual(
            result,
            {
                "reset_seconds": 1,
                "seed_seconds": 2,
                "fixtures_seconds": 3,
                "elapsed_seconds": 6,
                "fixture_output": {"queue": "fixtures prepared\n"},
            },
        )

    def test_existing_optional_fixture_modes(self):
        self.prepare(
            fixtures=(
                "queue",
                "link-account",
                "domain-tenants",
                "mfa-admin",
                "api-keys",
            )
        )
        commands = [call.args[0] for call in self.runner.call_args_list]
        self.assertEqual(
            [command[8:] for command in commands],
            [
                [],
                ["--link-account-only"],
                ["--domain-tenants"],
                ["--mfa-admin"],
                ["--api-keys"],
            ],
        )

    def test_seed_only_does_not_add_unrelated_fixtures(self):
        result = reset_seed.reset_and_seed(self.origin, "fixture-jwt", fixtures=())
        self.assertEqual(len(self.requests), 2)
        self.assertEqual(result["fixture_output"], {})
        self.runner.assert_not_called()

    def test_fixture_commands_still_require_the_api_container(self):
        with self.assertRaises(ValueError):
            reset_seed.reset_and_seed(self.origin, "fixture-jwt")
        self.assertEqual(self.requests, [])

    def test_reset_http_failures_abort_without_seeding_or_fixtures(self):
        for status in (401, 403, 429, 500):
            with self.subTest(status=status):
                self.requests.clear()
                self.responses = [(status, {"error": "private server detail"})]
                with self.assertRaisesRegex(
                    RuntimeError, "resetTestData.*HTTP " + str(status)
                ) as raised:
                    self.prepare()
                self.assertNotIn("private server detail", str(raised.exception))
                self.assertEqual(len(self.requests), 1)
                self.runner.assert_not_called()

    def test_seed_failure_aborts_without_running_fixtures(self):
        self.responses[1] = (500, {})
        with self.assertRaisesRegex(RuntimeError, "seedTestData.*HTTP 500"):
            self.prepare()
        self.runner.assert_not_called()

    def test_seed_wrong_success_envelope_aborts_without_fixtures(self):
        self.responses[1] = (200, {"data": {"status": "reset_complete"}})
        with self.assertRaisesRegex(RuntimeError, "seedTestData.*seed_complete"):
            self.prepare()
        self.runner.assert_not_called()

    def test_invalid_json_aborts_without_fixtures(self):
        self.responses = [(200, b"not JSON")]
        with self.assertRaisesRegex(RuntimeError, "resetTestData.*JSONDecodeError"):
            self.prepare()
        self.runner.assert_not_called()

    def test_connection_failure_is_not_a_skip(self):
        with (
            patch.object(
                reset_seed.urllib.request, "urlopen", side_effect=TimeoutError
            ),
            self.assertRaisesRegex(RuntimeError, "resetTestData.*TimeoutError"),
        ):
            self.prepare()
        self.runner.assert_not_called()

    def test_wrong_success_envelope_is_not_success(self):
        for body in ({}, {"data": {}}, {"data": {"status": "seed_complete"}}, []):
            with self.subTest(body=body):
                self.responses = [(200, body)]
                with self.assertRaisesRegex(
                    RuntimeError, "resetTestData.*reset_complete"
                ):
                    self.prepare()
                self.runner.assert_not_called()

    def test_fixture_failure_aborts_remaining_modes(self):
        for failure in (
            subprocess.CalledProcessError(7, []),
            subprocess.TimeoutExpired([], 60),
            FileNotFoundError(),
        ):
            with self.subTest(failure=type(failure).__name__):
                self.reset_responses()
                self.runner.reset_mock()
                self.runner.side_effect = failure
                with self.assertRaisesRegex(
                    RuntimeError, "contract fixture queue failed"
                ):
                    self.prepare(fixtures=("queue", "mfa-admin"))
                self.assertEqual(self.runner.call_count, 1)

    def reset_responses(self):
        self.responses = [
            (200, {"data": {"status": "reset_complete"}}),
            (200, {"data": {"status": "seed_complete"}}),
        ]

    def test_invalid_fixture_selection_fails_before_reset(self):
        for fixtures in (("unknown",), ("queue", "queue")):
            with self.subTest(fixtures=fixtures), self.assertRaises(ValueError):
                self.prepare(fixtures=fixtures)
        self.assertEqual(self.requests, [])
        self.runner.assert_not_called()

    def test_invalid_target_fails_before_reset(self):
        for origin, token, container in (
            (self.origin + "/api", "token", "api"),
            (self.origin, "", "api"),
            (self.origin, "token", ""),
        ):
            with (
                self.subTest(origin=origin, token=token, container=container),
                self.assertRaises(ValueError),
            ):
                reset_seed.reset_and_seed(origin, token, api_container=container)
        self.assertEqual(self.requests, [])


if __name__ == "__main__":
    unittest.main()
