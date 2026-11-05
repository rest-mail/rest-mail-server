"""coverage_bases.py names the operations whose coverage probes change base."""

import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

import coverage_bases


def example(status, **request):
    return {"request": request, "response": {"status": status}}


class CoverageBase(unittest.TestCase):
    def test_the_first_name_among_the_lowest_success(self):
        examples = {"zeta": example(200), "alpha": example(201), "beta": example(200), "aardvark": example(404)}
        self.assertEqual(coverage_bases.coverage_base(examples)[0], "beta")

    def test_an_operation_without_a_success_has_no_base(self):
        self.assertIsNone(coverage_bases.coverage_base({"missing": example(404)}))

    def test_a_new_example_sorting_first_changes_the_base(self):
        # #1832: dkim_record_differs before found, naming a domain only a test builds.
        before = {"domainDNSCheck": {"found": example(200, path={"id": 1})}}
        after = {"domainDNSCheck": {"dkim_record_differs": example(200, path={"id": 6}), **before["domainDNSCheck"]}}
        changes = coverage_bases.changed_bases(before, after)
        self.assertEqual([(op, old[0], new[0]) for op, old, new in changes],
                         [("domainDNSCheck", "found", "dkim_record_differs")])

    def test_a_new_example_sorting_later_changes_nothing(self):
        before = {"op": {"found": example(200)}}
        after = {"op": {"found": example(200), "other": example(200), "another_refusal": example(403)}}
        self.assertEqual(coverage_bases.changed_bases(before, after), [])

    def test_a_changed_request_in_the_base_counts(self):
        before = {"op": {"found": example(200, path={"id": 1})}}
        after = {"op": {"found": example(200, path={"id": 2})}}
        self.assertEqual([op for op, _, _ in coverage_bases.changed_bases(before, after)], ["op"])

    def test_a_changed_response_in_the_base_does_not_count(self):
        before = {"op": {"found": {"request": {}, "response": {"status": 200, "body": {"a": 1}}}}}
        after = {"op": {"found": {"request": {}, "response": {"status": 200, "body": {"a": 2}}}}}
        self.assertEqual(coverage_bases.changed_bases(before, after), [])

    def test_a_new_operation_counts_and_a_removed_one_does_not(self):
        before = {"gone": {"found": example(200)}}
        after = {"added": {"found": example(200)}}
        self.assertEqual([op for op, _, _ in coverage_bases.changed_bases(before, after)], ["added"])


class CommandLine(unittest.TestCase):
    """The command against a real repository, as a person runs it."""

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.git("init", "--quiet", "--initial-branch=main")
        self.write({"op": {"found": example(200, path={"id": 1})}})
        self.git("add", "openapi-example.json")
        tree = self.git("write-tree")
        identity = ["-c", "user.email=test@example.test", "-c", "user.name=Test"]
        self.git("update-ref", "refs/heads/main", self.git(*identity, "commit-tree", tree, "-m", "base"))
        previous = os.getcwd()
        os.chdir(self.directory)
        self.addCleanup(os.chdir, previous)

    def git(self, *arguments):
        done = subprocess.run(["git", *arguments], cwd=self.directory, check=True, capture_output=True, text=True)
        return done.stdout.strip()

    def write(self, examples):
        (self.directory / "openapi-example.json").write_text(json.dumps(examples))

    def run_command(self, *arguments):
        with contextlib.redirect_stdout(io.StringIO()) as output, contextlib.redirect_stderr(io.StringIO()):
            status = coverage_bases.main(["--base", "main", *arguments])
        return status, output.getvalue()

    def test_an_unchanged_tree_passes(self):
        status, output = self.run_command()
        self.assertEqual(status, 0)
        self.assertIn("No operation's coverage base changed", output)

    def test_a_changed_base_fails_and_names_the_run(self):
        self.write({"op": {"alpha": example(200, path={"id": 6}), "found": example(200, path={"id": 1})}})
        status, output = self.run_command()
        self.assertEqual(status, 1)
        self.assertIn("op: found -> alpha", output)
        self.assertIn("chore contract:run -- --operation-id op", output)

    def test_flags_print_the_selection_and_pass(self):
        self.write({"op": {"alpha": example(200), "found": example(200)}})
        status, output = self.run_command("--flags")
        self.assertEqual((status, output), (0, "--operation-id op\n"))

    def test_flags_print_nothing_when_nothing_changed(self):
        self.assertEqual(self.run_command("--flags"), (0, ""))

    def test_an_unreadable_ref_is_not_a_pass(self):
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(coverage_bases.main(["--base", "no-such-ref"]), 2)

    def test_unreadable_examples_are_not_a_pass(self):
        (self.directory / "openapi-example.json").write_text("not json")
        self.assertEqual(self.run_command()[0], 2)


class TheCheckedInExamples(unittest.TestCase):
    def test_every_example_has_what_the_rule_reads(self):
        root = Path(__file__).resolve().parents[2]
        examples = json.loads((root / "openapi-example.json").read_text(encoding="utf-8"))
        bases = [coverage_bases.coverage_base(operation) for operation in examples.values()]
        self.assertGreater(sum(base is not None for base in bases), 100)


if __name__ == "__main__":
    unittest.main()
