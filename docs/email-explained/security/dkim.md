---
title: "DKIM: signing the message itself"
part: The security layers, one at a time
summary: How DKIM signatures prove a message came from a domain and was not changed, how keys and selectors work, and the ordinary things that break a signature.
diagram: ../diagrams/dkim.sequence.json
---

# DKIM: signing the message itself

[SPF](spf.md) checks the path a message took: which server handed it over. That
breaks as soon as the message takes a different path, through a forwarder or a
mailing list, and it says nothing about whether the message was changed on the
way.

**DKIM**, DomainKeys Identified Mail, signs the message itself
([RFC 6376](https://www.rfc-editor.org/rfc/rfc6376), 2011, after
[RFC 4871](https://www.rfc-editor.org/rfc/rfc4871) of 2007). The sending domain
adds a cryptographic signature over the body and the important header lines,
and publishes the key that checks it in the DNS. Any server, anywhere along the
way, can then check that the message really was signed by that domain and has
not been changed since.

![Signing and checking a DKIM signature](../diagrams/dkim.svg)

*The diagram's source is [`dkim.sequence.json`](../diagrams/dkim.sequence.json).*

## The signature

The sending server adds one header line, shown here unfolded:

```
DKIM-Signature: v=1; a=rsa-sha256; c=relaxed/relaxed; d=example.com;
  s=2026a; t=1792760400; h=from:to:subject:date:message-id:mime-version;
  bh=2jUSOH9NhtVGCQWNr9BrIAPreKQjO6Sn7XIkfJVOzv8=;
  b=dzdVyOfAKCdLXdJOc9G2q8LoXSlEniSbav+yuU4zGeeruD00lszZVoG4ZHRNiYzR...
```

| Tag | Meaning |
|---|---|
| `d=` | The signing domain: who takes responsibility for the message |
| `s=` | The **selector**: which of the domain's keys was used |
| `a=` | The algorithm: `rsa-sha256`, or `ed25519-sha256` ([RFC 8463](https://www.rfc-editor.org/rfc/rfc8463)) |
| `h=` | The header lines that were signed, in order |
| `bh=` | A hash of the body |
| `b=` | The signature itself, over the signed header lines and `bh=` |
| `c=` | How the message was normalised before hashing: `simple` or `relaxed` |

The receiver fetches the public key from `<selector>._domainkey.<domain>`:

```
2026a._domainkey.example.com.  TXT  "v=DKIM1; k=rsa; p=MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8A..."
```

It hashes the body and the listed header lines the same way the sender did,
and checks the signature with the key. If anything signed has changed, by a
single character, the check fails.

## Normalising: simple and relaxed

Mail servers make harmless changes on the way: re-folding long header lines,
changing the case of header names, adding or trimming spaces at line ends. The
`c=` tag says how forgiving the check is ([§3.4](https://www.rfc-editor.org/rfc/rfc6376#section-3.4)):

- **`simple`** tolerates almost nothing.
- **`relaxed`** ignores the case of header names, folding, and runs of spaces.

`relaxed/relaxed` (header/body) is the usual choice.

## Selectors and keys

The selector lets one domain have many keys at once: one per mail service it
uses, one for each period of time. That is what makes rotation possible:
publish a new key under a new selector, start signing with it, and remove the
old one once mail signed with it has been delivered.

- **Key size.** "Signers MUST use RSA keys of at least 1024 bits for all keys.
  Signers SHOULD use RSA keys of at least 2048 bits," and the old `rsa-sha1`
  "MUST NOT be used for signing or verifying"
  ([RFC 8301](https://www.rfc-editor.org/rfc/rfc8301)). Short keys are not a
  theoretical risk: in 2012, after it was shown that mail from companies with
  weak keys could be forged, M3AAWG urged senders to "replace previously secure
  512- and 768-bit verification keys with 1024-bit and higher encryption"
  ([M3AAWG, 2012](https://www.m3aawg.org/news/m3aawg-issues-dkim-new-best-practices-in-wake-of-disclosed-key-length-vulnerability)).
- **Rotation.** "DKIM keys should be rotated at least every six months"
  ([M3AAWG, DKIM Key Rotation Best Common Practices, 2019](https://www.m3aawg.org/DKIMKeyRotation)),
  so that a stolen key is useful for months at most, and so that changing keys
  in an emergency is a routine everyone has practised.

## What breaks a signature

A valid signature can fail for reasons that have nothing to do with forgery:

- **Mailing lists** that add a footer, or a `[list-name]` tag to the subject,
  change signed content. This is the most common cause, and why [ARC](arc.md)
  exists.
- **Security gateways and antivirus** that add "scanned by" lines or
  disclaimers to the body.
- **Re-encoding.** A relay that converts an 8-bit body to quoted-printable for a
  7-bit hop changes every byte the body hash covered.
- **Unsigned or wrongly signed headers.** "The From header field MUST be
  signed" ([§5.4](https://www.rfc-editor.org/rfc/rfc6376#section-5.4)), and a
  sender that signs header lines that relays routinely rewrite loses the
  signature for no benefit.
- **The key is gone.** A selector removed from DNS too early, or not moved when
  the DNS host changed, fails every message still in transit.

Two features exist mostly to warn against. The `l=` tag signs only the first
part of the body, so lists can append to it; RFC 6376 warns that it "enables
attacks in which an intermediary with malicious intent can modify a message to
include content that solely benefits the attacker"
([§8.2](https://www.rfc-editor.org/rfc/rfc6376#section-8.2)). And a valid
signature can be **replayed**: a spammer who gets one message signed by a
reputable domain can send that exact message to millions, and every copy
verifies ([§8.6](https://www.rfc-editor.org/rfc/rfc6376#section-8.6)).

## DKIM is not about the From line, yet

The signing domain `d=` can be anything: a newsletter service signs with its
own domain for all its customers. A message from `ceo@example.com` signed by
`d=mailer.example` passes DKIM, and says nothing about whether `example.com`
sent it. [DMARC](dmarc.md) is what requires the two to match.

## What goes wrong

- **No DKIM at all,** or DKIM only from some of the services a domain sends
  through.
- **Keys shorter than 1024 bits,** or still `rsa-sha1`.
- **A TXT record split wrongly.** A 2048-bit key is longer than one DNS string
  (255 characters), and must be split into several strings in one record, not
  several records.
- **Keys never rotated,** or rotated by deleting the old selector at once.
- **Body changes after signing,** by the server's own footer or disclaimer
  feature, added after the signing step.

## Check your own domain

```sh
dig +short TXT 2026a._domainkey.example.com
```

Then send a message to an account elsewhere, open "Show original", and read the
`Authentication-Results` line: it says `dkim=pass` with the domain and selector,
or why not.

## How REST Mail handles it

- **Every outbound message is signed, once, over its final bytes,** after the
  Bcc line is removed and whichever way it was sent: the REST API, SMTP
  submission, or a message the queue relays. A domain with a key that cannot
  sign does not send unsigned; the message waits
  ([`internal/outbound/prepare.go`](../../../internal/outbound/prepare.go), `SignDKIM`).
- **Keys** are RSA of at least 2048 bits, as RFC 8301 recommends, or Ed25519;
  the library, [`rest-mail/go-dkim`](https://github.com/rest-mail/go-dkim),
  refuses `rsa-sha1` both ways ([`internal/outbound/dkimkey.go`](../../../internal/outbound/dkimkey.go)).
- **`List-Unsubscribe` and `List-Unsubscribe-Post` are signed** when a message
  carries them, which one-click unsubscribe requires
  ([Bulk mail](bulk-mail.md)).
- **The DNS record** each domain needs is printed by `chore dns:show`, and each
  key's age is exported as `restmail_dkim_key_age_seconds`
  ([`docs/dkim-key-age.md`](../../dkim-key-age.md)).
- **Incoming mail** is checked by the `dkim_verify` filter, one result per
  signature, recorded for DMARC; by default a failing signature alone does not
  reject ([`internal/pipeline/filters/dkim.go`](../../../internal/pipeline/filters/dkim.go)).
- **Rotation:** a per-domain interval can be saved, and the code to stage and
  activate a new key exists, but nothing runs it yet:
  [#728](https://github.com/rest-mail/rest-mail-server/issues/728).

## Sources

- [RFC 6376](https://www.rfc-editor.org/rfc/rfc6376): DomainKeys Identified Mail (DKIM) Signatures.
  [RFC 4871](https://www.rfc-editor.org/rfc/rfc4871): the 2007 original.
- [RFC 8301](https://www.rfc-editor.org/rfc/rfc8301): cryptographic algorithm and key usage update.
  [RFC 8463](https://www.rfc-editor.org/rfc/rfc8463): Ed25519 for DKIM.
- M3AAWG: [DKIM key length best practices (2012)](https://www.m3aawg.org/news/m3aawg-issues-dkim-new-best-practices-in-wake-of-disclosed-key-length-vulnerability),
  [DKIM Key Rotation Best Common Practices (2019)](https://www.m3aawg.org/DKIMKeyRotation).
- REST Mail: [`internal/outbound/`](../../../internal/outbound/),
  [`internal/pipeline/filters/dkim.go`](../../../internal/pipeline/filters/dkim.go),
  [`rest-mail/go-dkim`](https://github.com/rest-mail/go-dkim).
