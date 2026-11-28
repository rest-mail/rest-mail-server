"""Independent CPython poplib client; no server framing helpers are reused.

Uses the installed Python standard library (3.13 in CI), not vendored code:
https://docs.python.org/3.13/library/poplib.html
https://github.com/python/cpython/blob/3.13/Lib/poplib.py
Assertions intentionally remain enabled (the Go driver does not pass -O).
"""

import json
import poplib
import sys


def positive(response):
    assert response == b"+OK" or response.startswith(b"+OK "), response


def rejected(call):
    try:
        call()
    except poplib.error_proto as error:
        assert error.args[0].startswith(b"-ERR"), error
    else:
        raise AssertionError("command unexpectedly accepted")


def multiline(result, expected):
    response, lines, octets = result
    positive(response)
    # poplib independently removes the terminator, dot unstuffing and CRLF.
    decoded = b"".join(line + b"\r\n" for line in lines)
    assert decoded == expected, (decoded, expected)
    assert octets == len(expected), (octets, len(expected))
    return response


fixture = json.load(sys.stdin)
messages = fixture["messages"]
expected = [message["want"].encode("latin-1") for message in messages]
host, port = sys.argv[1].rsplit(":", 1)
client = poplib.POP3(host, int(port), timeout=5)
try:
    positive(client.getwelcome())
    capabilities = client.capa()
    for name in ("USER", "TOP", "UIDL", "RESP-CODES", "PIPELINING"):
        assert capabilities[name] == [], capabilities
    rejected(client.stat)
    positive(client.user("alice"))
    rejected(lambda: client.pass_("wrong"))
    rejected(client.stat)
    positive(client.user("alice"))
    positive(client.pass_("secret"))
    assert client.capa() == capabilities

    def listing(indices):
        assert client.stat() == (len(indices), sum(len(expected[i]) for i in indices))
        sizes = b"".join(f"{i + 1} {len(expected[i])}\r\n".encode() for i in indices)
        uids = b"".join(f"{i + 1} {messages[i]['uid']}\r\n".encode() for i in indices)
        multiline(client.list(), sizes)
        multiline(client.uidl(), uids)

    listing(range(len(messages)))
    for i, raw in enumerate(expected, 1):
        assert client.list(i).split()[1:] == [str(i).encode(), str(len(raw)).encode()]
        assert client.uidl(i).split()[1:] == [str(i).encode(), messages[i - 1]["uid"].encode()]
        response = multiline(client.retr(i), raw)
        assert response.split()[1:] == [str(len(raw)).encode(), b"octets"]
        # A new status proves no terminator/data was left in the response stream.
        positive(client.noop())

    if messages:
        # Fixed TOP expectations are independent of server header/body helpers.
        multiline(client.top(1, 0), b"Subject: dots\r\n\r\n")
        multiline(client.top(1, 2), b"Subject: dots\r\n\r\n.\r\n..\r\n")
        multiline(client.top(1, 99), expected[0])
        positive(client.dele(1))
        listing(range(1, len(messages)))
        for call in (lambda: client.retr(1), lambda: client.list(1),
                     lambda: client.uidl(1), lambda: client.top(1, 1),
                     lambda: client.dele(1)):
            rejected(call)
            positive(client.noop())
        positive(client.rset())
        listing(range(len(messages)))
        multiline(client.retr(1), expected[0])
        positive(client.dele(2))
        listing([0, 2, 3])
    else:
        for call in (lambda: client.retr(1), lambda: client.dele(1),
                     lambda: client.top(1, 0), lambda: client.uidl(1)):
            rejected(call)
        positive(client.rset())
        listing([])

    if fixture["mode"] == "disconnect":
        client.close()  # Intentionally no QUIT: the Go driver checks zero deletes.
    else:
        positive(client.quit())  # The Go driver checks only UID opaque-bravo deleted.
finally:
    client.close()
