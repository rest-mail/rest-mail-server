"""The newest-main compatibility check against real git histories (#1774).

Each fixture is a scripted repository whose API description is generated from
its files, like REST Mail's. A branch is merged the way the merge queue does it,
by squashing it onto main's tip, and the check compares main's tip with that
result. The pull request's own check, measured from where it branched, is run on
the same branch to show what the two disagree about.

OASDIFF names the pinned comparator (the merge-queue job installs it). Without
it a small stand-in reports added and removed paths with the ids and levels the
real one uses, so the git and label logic still runs everywhere.
"""

import json
import os
import stat
import subprocess
import sys
import tempfile
import textwrap
import unittest
from contextlib import redirect_stdout
from io import StringIO
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))

import contract_compat  # noqa: E402
import contract_diff  # noqa: E402

# Routes live one per file under routes/; the generator turns them into paths.
GENERATOR_BY_FILE = textwrap.dedent(
    """
    import json, pathlib, sys
    routes = sorted(json.loads(p.read_text())["path"] for p in pathlib.Path("routes").glob("*.json"))
    OUT = sys.argv[1]
    """
)
# The same API with every route in one routes.json.
GENERATOR_BY_LIST = textwrap.dedent(
    """
    import json, pathlib, sys
    routes = sorted(json.loads(pathlib.Path("routes.json").read_text()))
    OUT = sys.argv[1]
    """
)
EMIT = textwrap.dedent(
    """
    ok = {"200": {"description": "OK", "content": {"application/json": {"schema": {"type": "object", "properties": {"id": {"type": "integer"}}}}}}}
    doc = {"openapi": "3.1.0", "info": {"title": "fixture", "version": "1"},
           "paths": {r: {"get": {"operationId": "get" + r.strip("/").title(), "responses": ok}} for r in routes}}
    pathlib.Path(OUT).write_text(json.dumps(doc, indent=2, sort_keys=True))
    """
)

STAND_IN = textwrap.dedent(
    """
    import json, sys
    base, revision = (set(json.load(open(p))["paths"]) for p in sys.argv[2:4])
    changes = [{"id": "api-path-removed-without-deprecation", "level": 3, "path": p,
                "operation": "GET", "text": "api path removed without deprecation"}
               for p in sorted(base - revision)]
    changes += [{"id": "endpoint-added", "level": 1, "path": p, "operation": "GET",
                 "text": "endpoint added"} for p in sorted(revision - base)]
    print(json.dumps(changes))
    """
)


def git(repo, *args):
    return subprocess.run(
        ["git", *args], cwd=repo, check=True, capture_output=True, text=True
    ).stdout.strip()


class NewestMainCompatibility(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        self.repo = self.root / "repo"
        self.out = self.root / "out"
        self.out.mkdir()
        self.oasdiff = os.environ.get("OASDIFF")
        if not self.oasdiff:
            stand_in = self.root / "oasdiff"
            stand_in.write_text(f"#!{sys.executable}\n" + STAND_IN)
            stand_in.chmod(stand_in.stat().st_mode | stat.S_IXUSR)
            self.oasdiff = str(stand_in)

        self.repo.mkdir()
        git(self.repo, "init", "-q", "-b", "main")
        git(self.repo, "config", "user.email", "fixture@example.invalid")
        git(self.repo, "config", "user.name", "Fixture")
        git(self.repo, "config", "commit.gpgsign", "false")
        self.write("gen.py", GENERATOR_BY_FILE + EMIT)
        self.write("routes/widgets.json", json.dumps({"path": "/widgets"}))
        self.commit("start")
        self.start = git(self.repo, "rev-parse", "HEAD")

    def tearDown(self):
        self._tmp.cleanup()

    def write(self, name, text):
        path = self.repo / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)

    def commit(self, message):
        git(self.repo, "add", "-A")
        git(self.repo, "commit", "-q", "-m", message)
        return git(self.repo, "rev-parse", "HEAD")

    def branch(self, name, change):
        git(self.repo, "switch", "-q", "-c", name, self.start)
        change()
        head = self.commit(f"{name} work")
        git(self.repo, "switch", "-q", "main")
        return head

    def describe(self, revision, name):
        git(self.repo, "switch", "-q", "--detach", revision)
        output = self.out / f"{name}.json"
        subprocess.run(
            [sys.executable, "-I", "gen.py", str(output)], cwd=self.repo, check=True
        )
        git(self.repo, "switch", "-q", "main")
        return output

    def queue(self, branch, number):
        """Squash the branch onto main's tip, as the merge queue does."""
        git(self.repo, "switch", "-q", "-c", f"gh-readonly-queue/main/pr-{number}", "main")
        git(self.repo, "merge", "-q", "--squash", branch)
        head = self.commit(f"{branch} (#{number})")
        git(self.repo, "switch", "-q", "main")
        return head

    def own_check(self, branch):
        base = git(self.repo, "merge-base", "main", branch)
        with redirect_stdout(StringIO()):
            return contract_diff.main(
                [
                    str(self.describe(base, "merge-base")),
                    str(self.describe(branch, "head")),
                    "--oasdiff",
                    self.oasdiff,
                    "--output",
                    str(self.out / "own"),
                ]
            )

    def newest_main_check(self, merged, number, labels=()):
        tip = git(self.repo, "rev-parse", "main")
        argv = [
            str(self.describe(tip, "tip")),
            str(self.describe(merged, "merged")),
            "--oasdiff",
            self.oasdiff,
            "--base-sha",
            tip,
            "--head-sha",
            merged,
            "--head-ref",
            f"refs/heads/gh-readonly-queue/main/pr-{number}-{merged}",
            "--checkout",
            str(self.repo),
            "--output",
            str(self.out / "compat"),
        ]
        for label in labels:
            argv += ["--label", label]
        with redirect_stdout(StringIO()) as printed:
            code = contract_compat.main(argv)
        result = json.loads((self.out / "compat" / "compatibility.json").read_text())
        return code, result, printed.getvalue()

    def advance_main(self):
        """Main gains a route after both branches started."""
        self.write("routes/gadgets.json", json.dumps({"path": "/gadgets"}))
        self.commit("add gadgets")

    def test_branch_fine_at_its_merge_base_breaks_newest_main(self):
        # A refactor that moves every route into routes.json, from a main that
        # had only /widgets. It merges cleanly but drops main's newer /gadgets.
        def refactor():
            self.write("gen.py", GENERATOR_BY_LIST + EMIT)
            self.write("routes.json", json.dumps(["/widgets"]))
            (self.repo / "routes/widgets.json").unlink()

        stale = self.branch("refactor", refactor)
        self.advance_main()
        merged = self.queue("refactor", 41)

        self.assertEqual(self.own_check(stale), 0, "its own diff changes nothing")
        code, result, printed = self.newest_main_check(merged, 41)
        self.assertEqual(code, 1)
        self.assertEqual(result["missing_labels"], ["contract-change"])
        self.assertEqual(result["pull_requests"], [41])
        self.assertEqual(
            [(c["path"], c["classification"]) for c in result["changes"]],
            [("/gadgets", "breaking")],
        )
        self.assertIn("# API compatibility with newest main", printed)
        self.assertNotIn("# API contract changes", printed)

        # A deliberate label on the group accepts it, as in the contract diff.
        code, result, _ = self.newest_main_check(merged, 41, ["contract-change"])
        self.assertEqual((code, result["missing_labels"]), (0, []))

    def test_branch_fine_with_both(self):
        def add_route():
            self.write("routes/sprockets.json", json.dumps({"path": "/sprockets"}))

        fresh = self.branch("sprockets", add_route)
        self.advance_main()
        merged = self.queue("sprockets", 42)

        self.assertEqual(self.own_check(fresh), 0)
        code, result, _ = self.newest_main_check(merged, 42)
        self.assertEqual((code, result["missing_labels"]), (0, []))
        self.assertEqual(
            [(c["path"], c["classification"]) for c in result["changes"]],
            [("/sprockets", "additive")],
        )

    def test_group_lists_every_squashed_entry_and_its_ref(self):
        git(self.repo, "switch", "-q", "-c", "queue", "main")
        self.write("a.txt", "a")
        self.commit("first entry (#7)")
        self.write("b.txt", "b")
        head = self.commit("second entry (#9)")
        self.assertEqual(
            contract_compat.group_pull_requests(
                self.start,
                head,
                f"refs/heads/gh-readonly-queue/main/pr-9-{head}",
                cwd=self.repo,
            ),
            [7, 9],
        )
        self.assertEqual(
            contract_compat.group_pull_requests(
                self.start, head, "refs/heads/gh-readonly-queue/main/pr-11-abc123", cwd=self.repo
            ),
            [7, 9, 11],
        )


class Workflow(unittest.TestCase):
    """The job compares main's tip with the merge group's result, once per attempt."""

    def test_runs_on_merge_group_and_compares_its_base_and_head(self):
        text = (ROOT / ".github/workflows/contract-compat.yml").read_text()
        self.assertIn("name: API compatibility with newest main", text)
        on = text.split("\non:\n", 1)[1].split("\n\n", 1)[0]
        self.assertEqual(on.strip().splitlines()[-1].strip(), "merge_group:")
        self.assertNotIn("pull_request", on)
        self.assertNotIn("push", on)
        self.assertIn("ref: ${{ github.event.merge_group.base_sha }}", text)
        self.assertIn("ref: ${{ github.event.merge_group.head_sha }}", text)
        self.assertIn("scripts/contract_compat.py", text)


if __name__ == "__main__":
    unittest.main()
