"""Put actual oversized bytes on the wire; never just forge Content-Length."""

import os

import vertrag_hooks as hooks


@hooks.before_each
def oversized_body(transaction):
    size = int(os.environ["CONTRACT_OVER_CAP_BYTES"])
    if size <= 0:
        raise ValueError("the live body size must be positive")
    transaction["request"]["body"] = " " * size
    transaction["request"]["headers"]["Content-Length"] = str(size)


@hooks.before_each_validation
def forget_the_sent_body(transaction):
    """The bytes are on the wire by now, so stop carrying them to the worker.

    Vertrag sends the whole transaction to this worker for each later hook, and
    the worker's read costs about 5 s of CPU for each 33 MB message. Vertrag only
    calls this after the request went out, so what the proof sends is unchanged,
    and validation judges the response.
    """
    transaction["request"]["body"] = ""
