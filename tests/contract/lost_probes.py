"""Count the generated requests a before hook ended in the coverage process.

Vertrag 0.42.0 sends nothing for a probe whose body a hook changed
(runner.ErrChangedByHook) or that a hook skipped, and then drops it: it is not in
the report or the skip count. So this watches from inside the hook worker. It
writes one line, the operationId, per probe ended, and gate.py holds the counts
to lost-probes-baseline.txt.

The coverage process sends a transaction's documented request twice before any
of its probes: once in the examples phase, which always runs, and once to see
that it works. A hook may rewrite those, because only a generated body is
compared. So the first two requests of each transaction name are not counted.
Register this after every other hook file, so that it sees the body they leave.
"""

import os

DOCUMENTED_SENDS = 2


def register_lost_probe_count(environ=os.environ):
    path = environ.get("CONTRACT_LOST_PROBES")
    if environ.get("CONTRACT_COVERAGE") != "1" or not path:
        return
    import vertrag_hooks as hooks

    sends = {}
    drawn = {}

    @hooks.before_each
    def remember_the_drawn_body(transaction):
        drawn["body"] = transaction.get("request", {}).get("body") or ""

    @hooks.before("*")
    def count_a_probe_a_hook_ended(transaction):
        name = transaction.get("name", "")
        sends[name] = sends.get(name, 0) + 1
        if sends[name] <= DOCUMENTED_SENDS:
            return
        body = transaction.get("request", {}).get("body") or ""
        if transaction.get("skip") or body != drawn["body"]:
            with open(path, "a") as report:
                report.write(transaction.get("operationId", name) + "\n")
