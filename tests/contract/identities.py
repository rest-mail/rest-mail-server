"""Canonical keys for hosted identities, without changing probe request bytes.

The server stores domains as lowercase UTS 46 A-labels and mailbox/alias local
parts as lowercase NFC. Python's built-in IDNA 2003 codec would conflate ß and
ss, unlike the server's non-transitional lookup profile.
"""

import unicodedata

import idna


def domain_key(name):
    name = name.strip().removesuffix(".")
    try:
        return idna.encode(name, uts46=True, transitional=False).decode("ascii").lower()
    except idna.IDNAError:
        return name.lower()


def address_key(address):
    local, separator, domain = address.rpartition("@")
    if not separator:
        return address.lower()
    return unicodedata.normalize("NFC", local.lower()) + "@" + domain_key(domain)
