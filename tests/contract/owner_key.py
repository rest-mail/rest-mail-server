#!/usr/bin/env python3
"""A throwaway owner key for the contract run's authorization phase (#1162).

The owner endpoints (ownerChallenge, ownerStatus, ownerToken, resetAdmin) only
answer their success to a key in the server's owner registry. The topology
lists none, so the run makes one of its own: `new` writes a fresh Ed25519 seed
to a private cache file and prints only the public key, which `chore
test:contract-owner-key -- add` registers with the documented operator action
(restmail-api owner-key add) just before the authorization phase and revokes
after it. The private half never leaves this process and the hooks; nothing
here prints it, a signature, or a minted token.

Ed25519 is the RFC 8032 reference algorithm, in the standard library only: the
runner's Python has no cryptography package to rely on.
"""

import base64
import hashlib
import json
import os
import struct
import sys
import time
import urllib.request
from pathlib import Path

KEY = Path(os.environ.get("CONTRACT_OWNER_KEY", ".cache/contract/owner-key.json"))
COMMENT = "contract-owner"

_P = 2**255 - 19
_L = 2**252 + 27742317777372353535851937790883648493
_D = -121665 * pow(121666, _P - 2, _P) % _P
_I = pow(2, (_P - 1) // 4, _P)


def _x_of(y, sign):
    xx = (y * y - 1) * pow(_D * y * y + 1, _P - 2, _P) % _P
    x = pow(xx, (_P + 3) // 8, _P)
    if (x * x - xx) % _P:
        x = x * _I % _P
    if x & 1 != sign:
        x = _P - x
    return x


_GY = 4 * pow(5, _P - 2, _P) % _P
_GX = _x_of(_GY, 0)
_G = (_GX, _GY, 1, _GX * _GY % _P)


def _add(a, b):
    x1, y1, z1, t1 = a
    x2, y2, z2, t2 = b
    pa = (y1 - x1) * (y2 - x2) % _P
    pb = (y1 + x1) * (y2 + x2) % _P
    pc = t1 * 2 * _D * t2 % _P
    pd = z1 * 2 * z2 % _P
    e, f, g, h = pb - pa, pd - pc, pd + pc, pb + pa
    return (e * f % _P, g * h % _P, f * g % _P, e * h % _P)


def _times(scalar, point):
    total = (0, 1, 1, 0)
    while scalar:
        if scalar & 1:
            total = _add(total, point)
        point = _add(point, point)
        scalar >>= 1
    return total


def _encode(point):
    inverse = pow(point[2], _P - 2, _P)
    x, y = point[0] * inverse % _P, point[1] * inverse % _P
    return (y | (x & 1) << 255).to_bytes(32, "little")


def _expand(seed):
    digest = hashlib.sha512(seed).digest()
    scalar = int.from_bytes(digest[:32], "little")
    scalar &= (1 << 254) - 8
    scalar |= 1 << 254
    return scalar, digest[32:]


def public_key(seed):
    """The 32-byte Ed25519 public key of a 32-byte seed."""
    return _encode(_times(_expand(seed)[0], _G))


def sign(seed, message):
    """The 64-byte Ed25519 signature of message (RFC 8032, section 5.1.6)."""
    scalar, prefix = _expand(seed)
    public = _encode(_times(scalar, _G))
    r = int.from_bytes(hashlib.sha512(prefix + message).digest(), "little") % _L
    big_r = _encode(_times(r, _G))
    h = int.from_bytes(hashlib.sha512(big_r + public + message).digest(), "little") % _L
    return big_r + ((r + h * scalar) % _L).to_bytes(32, "little")


def _ssh_string(value):
    return struct.pack(">I", len(value)) + value


def authorized_key(seed):
    """The public key as an authorized_keys line, what the registry stores."""
    blob = _ssh_string(b"ssh-ed25519") + _ssh_string(public_key(seed))
    return "ssh-ed25519 " + base64.b64encode(blob).decode() + " " + COMMENT


def owner_message(challenge, purpose, address):
    """What an owner signs (bootstrap.OwnerMessage)."""
    return ("restmail-owner-v1\n" + purpose + "\n" + address + "\n" + challenge).encode()


def ssh_signature(seed, message):
    """The SSH wire-format signature, base64, as the owner endpoints read it."""
    wire = _ssh_string(b"ssh-ed25519") + _ssh_string(sign(seed, message))
    return base64.b64encode(wire).decode()


def new(path=None):
    """Write a fresh seed to path, readable by its owner only, and return the
    public key line. An existing key is replaced: each run registers its own."""
    path = path or KEY
    path.parent.mkdir(parents=True, exist_ok=True)
    if path.exists():
        path.unlink()
    seed = os.urandom(32)
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w") as handle:
        json.dump({"seed": seed.hex()}, handle)
    return authorized_key(seed)


def _seed(path=None):
    path = path or KEY
    if not path.exists():
        raise RuntimeError("the contract owner key is not registered (chore test:contract-owner-key -- add)")
    return bytes.fromhex(json.loads(path.read_text())["seed"])


def _post(base_url, path, body):
    request = urllib.request.Request(
        base_url + path,
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        return json.load(response)["data"]


def signed_answer(base_url, purpose, address="", path=None):
    """A fresh challenge from the server, signed by the run's owner key for
    purpose and address: the challenge, public_key and signature fields of an
    owner request. The challenge is unspent until the request it goes into."""
    seed = _seed(path)
    challenge = _post(base_url, "/api/v1/auth/owner/challenge", {})["challenge"]
    return {
        "challenge": challenge,
        "public_key": authorized_key(seed),
        "signature": ssh_signature(seed, owner_message(challenge, purpose, address)),
    }


def reset_token(base_url, address, path=None):
    """A reset-admin token for address, minted with the run's owner key. The
    owner bucket is paced between the challenge and the trade."""
    answer = signed_answer(base_url, "reset-admin", address, path)
    time.sleep(1.1)
    return _post(base_url, "/api/v1/auth/owner/token",
                 {"purpose": "reset-admin", "address": address, **answer})["token"]


if __name__ == "__main__":
    if sys.argv[1:] != ["new"]:
        sys.exit("usage: owner_key.py new  (writes the seed to $CONTRACT_OWNER_KEY, prints the public key)")
    print(new())
