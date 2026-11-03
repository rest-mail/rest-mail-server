# Forwarding mail

This page covers a person forwarding a message, and the forwarding a Sieve
`redirect` does on its own.

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

## Sieve redirects, SPF and SRS

A Sieve `redirect` passes a message on to another address. The next server checks
SPF against the envelope sender, and this server is not in the sender's SPF
record, so a forward that kept its sender could fail there. REST Mail therefore
rewrites the sender with the Sender Rewriting Scheme (SRS). The forward is also
DKIM-signed and ARC-sealed by the forwarding domain, the hosted domain of the
mailbox that forwarded it.

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
