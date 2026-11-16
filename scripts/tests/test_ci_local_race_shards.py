"""Local race concurrency is configurable without dropping selected tests."""

import importlib.util
import io
import os
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("ci_local", ROOT / "scripts/ci_local.py")
gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gate)


class RaceShardConfiguration(unittest.TestCase):
    def test_default_is_eight(self):
        self.assertEqual(gate.configured_race_shards({}), 8)

    def test_positive_overrides(self):
        for value in ("1", "2", "10", "016"):
            with self.subTest(value=value):
                self.assertEqual(
                    gate.configured_race_shards({"RESTMAIL_RACE_SHARDS": value}),
                    int(value),
                )

    def test_invalid_overrides_fail_closed(self):
        for value in ("", "0", "-1", "1.5", "1e2", "many", "+2", " 2 ", "2_0"):
            with self.subTest(value=value):
                with self.assertRaisesRegex(
                    gate.CouldNotRun, "RESTMAIL_RACE_SHARDS must be a positive integer"
                ):
                    gate.configured_race_shards({"RESTMAIL_RACE_SHARDS": value})

    def test_gate_reads_its_environment(self):
        with tempfile.TemporaryDirectory() as temporary:
            with patch.dict(os.environ, {"RESTMAIL_RACE_SHARDS": "2"}):
                local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)
            self.assertEqual(local.race_shards, 2)
            self.assertEqual(local.env["RESTMAIL_RACE_SHARDS"], "2")

    def test_both_api_packages_use_the_override_and_keep_every_test(self):
        with tempfile.TemporaryDirectory() as temporary:
            with patch.dict(os.environ, {"RESTMAIL_RACE_SHARDS": "2"}):
                local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)
            local.packages = [
                "mod/internal/api",
                "mod/internal/api/handlers",
            ]
            with (
                patch.object(local, "run", side_effect=["TestA\nTestB", ""] * 2),
                patch.object(
                    local,
                    "shards",
                    return_value=[
                        {"Action": "pass", "Test": "TestA"},
                        {"Action": "pass", "Test": "TestB"},
                    ],
                ) as shards,
                redirect_stdout(io.StringIO()) as output,
            ):
                local.go_tests()
            self.assertEqual(shards.call_count, 2)
            for call, directory in zip(
                shards.call_args_list, ("internal/api", "internal/api/handlers")
            ):
                self.assertEqual(call.args[0], ["TestA", "TestB"])
                self.assertEqual(call.args[3:], (2, "2h", directory))
            self.assertEqual(output.getvalue().count("across 2 race shards"), 2)

    def test_custom_shards_still_refuse_an_unexecuted_test(self):
        with tempfile.TemporaryDirectory() as temporary:
            with patch.dict(os.environ, {"RESTMAIL_RACE_SHARDS": "1"}):
                local = gate.Gate(ROOT, Path(temporary), Path(temporary), False)
            local.packages = ["mod/internal/api"]
            with (
                patch.object(local, "run", side_effect=["TestA\nTestB", ""]),
                patch.object(
                    local, "shards", return_value=[{"Action": "pass", "Test": "TestA"}]
                ),
                self.assertRaisesRegex(RuntimeError, "did not execute.*TestB"),
            ):
                local.go_tests()
