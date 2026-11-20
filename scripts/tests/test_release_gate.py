"""The release promotion gate (#2104, AC06): scripts/release_gate.py.

Receipts are written by cluster_e2e.receipt, the function the installed-cluster
check uses, so a change to the receipt format that the gate cannot read fails
here. Each refusal class the contract names (missing, stale, failed, wrong
digest) has its own case, and one complete fresh matching run is authorized.
"""

import importlib.util
import io
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]


def load(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / f"scripts/{name}.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


cluster_e2e = load("cluster_e2e")
release_gate = load("release_gate")

COMMIT = "0123456789abcdef0123456789abcdef01234567"
OTHER_COMMIT = "fedcba9876543210fedcba9876543210fedcba98"
NOW = "2026-11-19T12:00:00Z"
FRESH = "2026-11-19T05:00:00Z"
OLD = "2026-11-17T05:00:00Z"
DIGESTS = {name: "sha256:" + format(i, "x") * 64 for i, name in enumerate(release_gate.RELEASE_IMAGES, start=1)}
WRONG = "sha256:" + "f" * 64


def digest_token(digests):
    return ",".join(sorted(f"{name}@{digest}" for name, digest in digests.items()) + ["postgres@sha256:" + "e" * 64])


def run_lines(run="r2", commit=COMMIT, digests=None, at=FRESH, results=None, steps=None):
    """One run's receipts, written by cluster_e2e.receipt."""
    token = digest_token(DIGESTS if digests is None else digests)
    results = results or {}
    return [cluster_e2e.receipt(commit, "api:1.2.3", step, results.get(step, "pass"), "ok",
                                digests=token, run=run, at=at)
            for step in (cluster_e2e.STEPS if steps is None else steps)]


class GateTests(unittest.TestCase):
    def gate(self, lines, images=None, extra=()):
        images = DIGESTS if images is None else images
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "receipts.txt"
            if lines is not None:
                path.write_text("".join(line + "\n" for line in lines))
            argv = ["--receipts", str(path), "--commit", COMMIT, "--now", NOW, *extra]
            for name, digest in images.items():
                argv += ["--image", f"{name}={digest}"]
            out, err = io.StringIO(), io.StringIO()
            with redirect_stdout(out), redirect_stderr(err):
                code = release_gate.main(argv)
        return code, out.getvalue(), err.getvalue()

    def assertRefused(self, result, *fragments):
        code, out, err = result
        self.assertEqual(code, 1, err)
        self.assertEqual(out, "")
        for fragment in fragments:
            self.assertIn(fragment, err)

    def test_a_complete_fresh_matching_run_authorizes(self):
        code, out, err = self.gate(run_lines())
        self.assertEqual((code, err), (0, ""))
        self.assertTrue(out.startswith(f"release-gate authorized commit={COMMIT} run=r2 images=api@{DIGESTS['api']}"))

    def test_only_the_newest_run_counts(self):
        older_failed = run_lines(run="r1", results={"health": "fail"})
        self.assertEqual(self.gate(older_failed + run_lines(run="r2"))[0], 0)
        self.assertRefused(self.gate(run_lines(run="r2") + older_failed), "failed: run r1 step health result=fail")

    # missing
    def test_no_receipts_file_is_refused(self):
        self.assertRefused(self.gate(None), "missing: no cluster-e2e receipts")

    def test_an_empty_receipts_file_is_refused(self):
        self.assertRefused(self.gate([]), "missing: no cluster-e2e receipts")

    def test_a_run_missing_a_step_is_refused(self):
        partial = [step for step in cluster_e2e.STEPS if step != "mail-in"]
        self.assertRefused(self.gate(run_lines(steps=partial)), "missing: run r2 has no receipt for step mail-in")

    def test_a_receipt_written_before_runs_were_recorded_is_refused(self):
        legacy = f"cluster-e2e receipt commit={COMMIT} images=api:1.2.3 step=health result=pass detail=200"
        self.assertRefused(self.gate(run_lines() + [legacy]), "missing: the newest receipt names no run")

    # failed
    def test_a_failed_step_is_refused(self):
        self.assertRefused(self.gate(run_lines(results={"dkim-receive": "fail"})),
                           "failed: run r2 step dkim-receive result=fail")

    def test_a_skipped_step_is_refused(self):
        self.assertRefused(self.gate(run_lines(results={"restmail-peer": "skipped"})),
                           "failed: run r2 step restmail-peer result=skipped")

    # stale
    def test_a_run_on_another_commit_is_refused(self):
        self.assertRefused(self.gate(run_lines(commit=OTHER_COMMIT)),
                           f"stale: run r2 tested commit {OTHER_COMMIT}, not {COMMIT}")

    def test_a_run_older_than_the_window_is_refused(self):
        self.assertRefused(self.gate(run_lines(at=OLD)), "stale: run r2 began 2026-11-17T05:00:00Z")
        self.assertEqual(self.gate(run_lines(at=OLD), extra=("--max-age", "60"))[0], 0)

    def test_a_receipt_without_a_time_is_refused(self):
        self.assertRefused(self.gate(run_lines(at="-")), "stale: run r2 has a receipt with no time it finished")

    # wrong digest
    def test_a_different_digest_is_refused(self):
        ran = dict(DIGESTS, **{"imap-gateway": WRONG})
        self.assertRefused(self.gate(run_lines(digests=ran)),
                           f"wrong digest: run r2 ran imap-gateway@{WRONG}, the release names {DIGESTS['imap-gateway']}")

    def test_an_image_the_run_did_not_record_is_refused(self):
        ran = {name: digest for name, digest in DIGESTS.items() if name != "webmail"}
        self.assertRefused(self.gate(run_lines(digests=ran)), "wrong digest: run r2 recorded no digest for webmail")

    def test_an_unknown_digest_is_refused(self):
        self.assertRefused(self.gate(run_lines(digests=dict(DIGESTS, admin="unknown"))),
                           "wrong digest: run r2 ran admin@unknown")

    def test_two_digests_for_one_image_are_refused(self):
        lines = [line.replace("digests=", f"digests=api@{WRONG},") for line in run_lines()]
        self.assertRefused(self.gate(lines), "wrong digest: run r2 ran api@")

    def test_digests_that_change_within_a_run_are_refused(self):
        lines = run_lines()
        lines[-1] = run_lines(digests=dict(DIGESTS, api=WRONG))[-1]
        self.assertRefused(self.gate(lines), "recorded different image digests on different steps")

    def test_digests_from_the_ci_builds_are_refused_for_a_published_release(self):
        # cluster-e2e.yml loads images it builds into kind; their digests are
        # not the published release's, so its receipts never authorize one.
        built = {name: "sha256:" + "c" * 64 for name in DIGESTS}
        code, _, err = self.gate(run_lines(digests=built))
        self.assertEqual(code, 1)
        self.assertEqual(err.count("wrong digest:"), len(release_gate.RELEASE_IMAGES))

    # arguments
    def test_every_released_image_needs_a_digest(self):
        with redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
            release_gate.parse_args(["--receipts", "r", "--commit", COMMIT, "--image", f"api={DIGESTS['api']}"])

    def test_a_short_commit_or_bad_digest_is_an_error(self):
        images = [arg for name, digest in DIGESTS.items() for arg in ("--image", f"{name}={digest}")]
        for argv in (["--commit", COMMIT[:12], *images],
                     ["--commit", COMMIT, *images, "--image", "api=sha256:abc"],
                     ["--commit", COMMIT, *images, "--image", f"postgres={WRONG}"]):
            with self.subTest(argv=argv[1]), redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                release_gate.parse_args(["--receipts", "r", *argv])

    def test_the_released_images_are_the_ones_docker_yml_builds(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/docker.yml").read_text())
        built = [entry["image"] for entry in workflow["jobs"]["build-and-push"]["strategy"]["matrix"]["include"]]
        self.assertEqual(sorted(built), sorted(release_gate.RELEASE_IMAGES))

    def test_the_gate_checks_every_step_cluster_e2e_runs(self):
        self.assertEqual(release_gate.load_steps(), cluster_e2e.STEPS)


if __name__ == "__main__":
    unittest.main()
