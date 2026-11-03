# Forwarding mail

This page covers a person forwarding a message, active aliases, and the
forwarding a Sieve `redirect` does on its own.

## Forwarding messages and attachments

Forwarding uses the source message the caller may read and a sender account the
caller may send as. The ordinary recipient validation, sender-domain policy,
rate limit and outbound pipeline apply. The note appears above the original
message header and body in both the text and HTML alternatives.

The server re-attaches stored parts instead of wrapping the original as an
attached message. Binary files, empty attachments and inline Content-ID parts
are retained. The Sent copy has attachment rows and filesystem files or S3
objects of its own. Deleting an equal original object does not remove the copy.

The complete signed MIME message must fit `SMTP_MAX_MESSAGE_SIZE`, including
base64 encoding, MIME headers and boundaries. The sender's quota counts the
forwarded body and copied attachment bytes. Oversized forwards receive 413;
insufficient sender quota receives 422 with `mailbox_full`.

A missing, changed or unreadable source object fails the forward. The server
does not silently send a message with fewer attachments. Sent, attachment
references, quota and recipient queue or local-delivery rows commit together.
Notifications and queued traces are published after commit.

Object storage is outside the database transaction. A failed transaction can
leave unreferenced copied objects, while database references and quota changes
roll back. The source message and its attachment objects are not modified.

## Aliases

SMTP inbound and RESTMAIL deliveries resolve an active alias to its one stored
destination. There is no local copy and no multi-destination expansion. An exact
mailbox still wins over an alias, including an unavailable mailbox reserving its
identity. An explicit alias wins over plus-address fallback; an inactive alias
continues to reserve its address and delivers nothing.

A destination hosted here uses normal local mailbox delivery, without an
outbound hop or SRS rewrite. The destination's receive permission, quota,
inbound pipeline, account Sieve script and delivery notifications apply. When
raw bytes are supplied, a `Delivered-To` header retains the original alias
recipient. Structured-only local delivery keeps its ordinary body fields and
does not manufacture a header-only raw message.

A destination elsewhere uses the same forward construction and outbound queue
as a Sieve `redirect`, not a separate relay mechanism. The forwarding domain is
the original alias's hosted domain, even when another alias in the chain is on
a different hosted domain. Its inbound pipeline checks the message before it
is queued. The configured alias destination remains authoritative: a domain
Sieve redirect cannot add destinations or turn the alias into a local copy.
Signing and the bounce path follow the Sieve rules below. The original raw
message is required for an external forward; structured-only deliveries are
refused rather than acknowledged without anything to send.

Resolution follows at most eight alias rows. A loop, a longer chain, an
ambiguous legacy identity, an inactive alias or a missing hosted destination
is refused with an explicit routing reason; none is relayed back to this
server's own MX or silently acknowledged. SMTP refuses a broken route at RCPT
with `550` and its routing reason; RESTMAIL refuses it with
`422 undeliverable`. A database or queue failure defers delivery instead.
An external message returning through an alias already in its `Delivered-To`
chain is also refused.

SMTP rechecks all recipients before DATA delivery starts. If an alias became
a loop after RCPT, the message is refused before any recipient is stored. A
permanent alias routing failure racing even later is still returned as `550`,
not hidden behind another recipient's success. SMTP delivery to several
recipients is not an atomic transaction: a mailbox committed before that late
refusal remains delivered, as with the existing delivery path.

The required delivery stage queues and stores nothing. For content-stage
delivery, removing the inbound queue row and either storing the destination
message or creating the external forward commit together. A stale replay
whose inbound row has already been removed rolls back, without a second
message or forward. This retains the existing limits: Sieve redirects and
vacation replies performed by a local mailbox's pipeline before its storage
transaction are not made exactly-once by alias resolution.

## Sieve redirects, SPF and SRS

A Sieve `redirect` passes a message on to another address. The next server checks
SPF against the envelope sender, and this server is not in the sender's SPF
record, so a forward that kept its sender could fail there. REST Mail therefore
rewrites the sender with the Sender Rewriting Scheme (SRS). The forward is also
DKIM-signed and ARC-sealed by the forwarding domain, the hosted domain of the
mailbox that forwarded it.

An existing author's DKIM signature is retained. It does not replace the
forwarding domain's signature: the outbound worker adds that domain's DKIM
signature before adding its ARC set, including when the original was signed.

**What is rewritten.** Every forward sent over SMTP whose envelope sender is not
on a domain hosted here. There is no per-domain switch. These are not rewritten:

- a sender on a domain hosted here, whose SPF record already names this server;
- a null sender, which stays null (RFC 5321 §6.1);
- a local alias, which makes no hop;
- a delivery over RESTMAIL, which authenticates the sending server, not SPF.

`redirect :copy` works as it did: it only decides whether a local copy is kept.

**The address.** `alice@orig.example` forwarded through `forward.example` leaves as:

```text
SRS0=HHHH=TT=orig.example=alice@forward.example
```

`TT` is the day it was made, as the day count mod 1024 in two base 32 characters.
`HHHH` is a truncated HMAC-SHA256 over `TT`, the domain and the local part, in four
base 32 characters. The address lives under the forwarding domain, whose SPF record
already names this server, so SRS needs no new DNS. A sender that is already another
forwarder's SRS address is rewritten the same way: there is no SRS1.

**Bounces.** A next hop that cannot deliver the forward sends its bounce to that
address, and the inbound SMTP gateway checks it at RCPT:

- With a good hash, and no more than 21 days old, it is accepted. The message is
  queued to the original sender with a null reverse path, as a delivery status
  notification, sent as the forwarding domain and held to the same per-recipient
  limit as any DSN for a sender elsewhere (`BOUNCE_DSN_MAX_PER_RECIPIENT`).
- A forged or expired address gets `550 5.1.1`, as any unknown recipient does.

Mail systems can change the case of a local part. The hash, timestamp and prefix
are checked whatever their case, as postsrsd does.

The queue row of a forward keeps the original sender, and only the SMTP envelope
carries the address. So when this server itself gives up on a forward, its DSN
goes straight to the original sender.

The secret that signs these addresses is `SRS_SECRET`. See
[the operator manual](MANUAL.md#710-forwarding-and-the-srs-secret) for how it is
made and rotated.
