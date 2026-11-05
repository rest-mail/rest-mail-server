import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location(
    "check_api_shard_work", Path(__file__).resolve().parents[1] / "check_api_shard_work.py"
)
CHECK = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CHECK)

TOP = """Duration: 609.43s, Total samples = 251.77s (41.31%)
      flat  flat%   sum%        cum   cum%
         0     0%     0%     26.25s 10.43%  golang.org/x/crypto/bcrypt.bcrypt
"""


class ShardSelectionTests(unittest.TestCase):
    def test_selection_matches_ci_sorted_round_robin_including_examples_and_fuzz(self):
        listing = "TestD\nTestB\nFuzzA\nok package\nExampleA\nTestA\nTestC\n"
        shards = [CHECK.shard_names(listing, index, 4) for index in range(1, 5)]
        self.assertEqual(shards, [["ExampleA", "TestC"], ["FuzzA", "TestD"], ["TestA"], ["TestB"]])
        self.assertEqual(len(set(sum(shards, []))), 6)

    def test_invalid_or_empty_shards_fail(self):
        for shard, count in [(0, 4), (5, 4), (1, 0), (4, 4)]:
            with self.subTest(shard=shard, count=count), self.assertRaises(ValueError):
                CHECK.shard_names("TestA", shard, count)


class StaticFindingTests(unittest.TestCase):
    def tree(self, files):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        root = Path(directory.name)
        for name in ("internal/api", "cmd", "tools", "tests"):
            (root / name).mkdir(parents=True)
        for name, text in files.items():
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(text)
        return root

    def test_a_fixture_at_the_minimum_cost_is_clean(self):
        root = self.tree({"internal/api/a_test.go": 'bcrypt.GenerateFromPassword([]byte("x"), bcrypt.MinCost)\n'})
        self.assertEqual(CHECK.static_findings(root), [])

    def test_a_fixture_at_any_other_cost_is_named(self):
        for cost in ("10", "bcrypt.DefaultCost"):
            with self.subTest(cost=cost):
                root = self.tree({"internal/api/handlers/a_test.go": f'bcrypt.GenerateFromPassword([]byte("x"), {cost})\n'})
                self.assertEqual(
                    CHECK.static_findings(root),
                    ["internal/api/handlers/a_test.go:1: bcrypt at a cost other than bcrypt.MinCost"],
                )

    def test_lowering_the_cost_outside_a_test_file_is_named(self):
        root = self.tree({"cmd/api/main.go": "auth.UseMinimumHashCostInTests()\n"})
        self.assertEqual(CHECK.static_findings(root), ["cmd/api/main.go: UseMinimumHashCostInTests outside a test file"])

    def test_the_seam_and_its_probe_are_not_findings(self):
        root = self.tree({
            "internal/auth/hash_cost.go": "func UseMinimumHashCostInTests() {}\n",
            "internal/auth/testdata/hashcost/main.go": "auth.UseMinimumHashCostInTests()\n",
            "internal/api/main_test.go": "auth.UseMinimumHashCostInTests()\n",
        })
        self.assertEqual(CHECK.static_findings(root), [])

    def test_the_checked_in_tree_is_clean(self):
        self.assertEqual(CHECK.static_findings(CHECK.ROOT), [])


class WorkTestRunTests(unittest.TestCase):
    def run_with(self, returncode, stdout):
        result = subprocess.CompletedProcess([], returncode, stdout)
        with patch.object(CHECK.subprocess, "run", return_value=result), patch("builtins.print"):
            return CHECK.run_work_tests(Path("."))

    def test_a_package_that_ran_nothing_fails(self):
        # go test exits 0 when -run matches nothing: the check must not read that as proof.
        failures = self.run_with(0, "testing: warning: no tests to run\nPASS\n")
        self.assertEqual(len(failures), len(CHECK.WORK_PACKAGES))

    def test_a_failing_test_fails(self):
        self.assertEqual(len(self.run_with(1, "--- FAIL: TestShardWork_FixturesHashAtTheMinimumCost (0.01s)\n")), len(CHECK.WORK_PACKAGES))

    def test_every_named_test_passing_passes(self):
        names = {name for wanted in CHECK.WORK_PACKAGES.values() for name in wanted}
        output = "".join(f"--- PASS: {name} (0.01s)\n" for name in names)
        self.assertEqual(self.run_with(0, output), [])


class ShardRunTests(unittest.TestCase):
    def test_bcrypt_share_reads_the_profile(self):
        self.assertAlmostEqual(CHECK.bcrypt_share(TOP), 26.25 / 251.77)

    def test_a_profile_without_bcrypt_has_no_share(self):
        self.assertEqual(CHECK.bcrypt_share("Duration: 1s, Total samples = 1s (100%)\n"), 0.0)

    def test_a_report_with_no_total_is_an_error(self):
        with self.assertRaises(ValueError):
            CHECK.bcrypt_share("no profile here")

    def run_shard(self, exit_code, top):
        results = [
            subprocess.CompletedProcess([], 0, "TestA\n"),
            subprocess.CompletedProcess([], exit_code),
            subprocess.CompletedProcess([], 0, top),
        ]
        with patch.object(CHECK.subprocess, "run", side_effect=results) as run, patch("builtins.print"):
            result = CHECK.run_shard(Path("."), 1, 1, "testdb-exec", 0.05)
        command = run.call_args_list[1].args[0]
        for flag in ["-race", "-count=1", "-timeout=20m", "-exec", "testdb-exec"]:
            self.assertIn(flag, command)
        self.assertTrue(any(flag.startswith("-coverprofile=") for flag in command))
        return result

    def test_a_shard_with_little_bcrypt_passes_whatever_it_took(self):
        self.assertEqual(self.run_shard(0, "Total samples = 100s (1%)\n1s 1% golang.org/x/crypto/bcrypt.bcrypt\n"), 0)

    def test_a_shard_spending_its_cpu_on_bcrypt_fails(self):
        self.assertEqual(self.run_shard(0, TOP), 1)

    def test_a_failed_shard_is_not_hidden_by_little_bcrypt(self):
        self.assertEqual(self.run_shard(1, "Total samples = 100s (1%)\n"), 1)


if __name__ == "__main__":
    unittest.main()
