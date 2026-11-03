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
