"""Run one of the contract fixture commands with a compile-sized bound.

In dev mode, `chore test:contract-*-fixture` runs

    docker exec <project>-api go run ./tests/contract/fixtures ...

inside the run's own container: a compile followed by one insert. CI's prod-mode
test image instead runs the prebuilt `restmail-fixtures` binary. Twenty seconds
was the compile budget on a quiet runner, and five call sites used it. On the ARC
pool, with the contract phases sharing a node, one run timed out at it before the
fixture ran at all:

    ERROR: POST /api/v1/admin/queue/{id}/bounce > Bounce a single queue entry > 200
      before hook: TimeoutExpired: Command
      '['chore', 'test:contract-queue-entry-fixture']' timed out after 20 seconds

and the exchange it serves was reported as an error rather than as a product
failure. Three minutes bounds a cold compile while still failing a container that
has stopped answering.
"""

import subprocess


TIMEOUT_SECONDS = 180


def run(command):
    """Run `chore <command>` and return the completed process, output captured."""
    return subprocess.run(
        ["chore", command],
        check=True,
        capture_output=True,
        timeout=TIMEOUT_SECONDS,
        text=True,
    )
