---
title: "Encryption in transit: STARTTLS and implicit TLS"
part: The security layers, one at a time
summary: How mail is encrypted between servers and apps, why the way it was added leaves it open to downgrade, and what to do about it.
diagram: ../diagrams/starttls.sequence.json
---

# Encryption in transit: STARTTLS and implicit TLS

SMTP was designed in 1982 to send everything in plain text, across networks
run by people who trusted each other. Anyone on the path between two mail
servers could read every message, and change it.

Encryption was added in 1999, as an upgrade a server could offer and a client
could take: **STARTTLS** ([RFC 2487](https://www.rfc-editor.org/rfc/rfc2487),
now [RFC 3207](https://www.rfc-editor.org/rfc/rfc3207)). It worked: today almost
all mail between large providers is encrypted. Google reports that 98 to 99% of
the mail Gmail sent to other providers between July and October 2026 was
encrypted in transit
([Google Transparency Report](https://transparencyreport.google.com/safer-email/overview)).
But the way it was added, as an optional upgrade offered in the clear, leaves a
gap that an attacker on the path can use.

![How STARTTLS can be stripped](../diagrams/starttls.svg)

*The diagram's source is [`starttls.sequence.json`](../diagrams/starttls.sequence.json).*

## Two ways to encrypt

- **STARTTLS:** connect in the clear, ask "do you support encryption?", and
  upgrade the same connection if the answer is yes.
- **Implicit TLS:** connect to a port where TLS starts with the first byte, the
  way HTTPS does. There is no unencrypted moment.

| Port | Used for | Encryption |
|---|---|---|
| 25 | Server to server | STARTTLS, optional |
| 587 | Submission, from mail apps | STARTTLS |
| 465 | Submission, from mail apps | Implicit TLS |
| 143 / 993 | IMAP | STARTTLS / implicit TLS |
| 110 / 995 | POP3 | STARTTLS (`STLS`) / implicit TLS |

For connections from mail apps, the IETF now prefers implicit TLS.
[RFC 8314](https://www.rfc-editor.org/rfc/rfc8314), "Cleartext Considered
Obsolete" (2018), asks providers to offer submission on 465, IMAP on 993 and POP3
on 995, and apps to use them.

## Why port 25 cannot require encryption

Between servers, there is only one port, 25, and it has to start in the clear,
because the sender cannot know in advance whether the receiver supports
encryption. And a server that receives mail for a domain must not refuse it for
being unencrypted: "A publicly-referenced SMTP server MUST NOT require use of
the STARTTLS extension in order to deliver mail locally"
([RFC 3207 §4](https://www.rfc-editor.org/rfc/rfc3207#section-4)).

So encryption between servers is **opportunistic**: used when both sides can,
skipped when they cannot. [RFC 7435](https://www.rfc-editor.org/rfc/rfc7435#section-3)
describes the idea: "The primary goal of OS is to not impede communication while
maximizing the deployment of usable security."

## The downgrade

The offer to encrypt travels in the clear, in the `EHLO` reply. Anyone who can
change traffic on the path can delete the line `250-STARTTLS`. The sending
server then sees a receiver that does not support encryption, and, being
opportunistic, sends the message in plain text. Neither side notices.

This is not hypothetical. In 2014 the EFF reported "ISPs in the US and Thailand
intercepting their customers' data to strip a security flag—called
STARTTLS—from email traffic", and firewalls that did the same
([EFF, 2014](https://www.eff.org/deeplinks/2014/11/starttls-downgrade-attacks)).

## Certificates nobody checks

Even when the upgrade happens, the sending server usually does not check who it
is talking to. A certificate proves the server's name, but on port 25 there is
no standard way to know which name to expect, or whether to insist. So most
servers accept any certificate, and most receivers' certificates would fail a
check: a 2023 study of the top 10 million domains found that over 96% of
reachable MX servers offer STARTTLS, but "30% of presented certificates are
invalid, mostly because of hostname issues", and every one of the 47 mail
providers it tested delivered to servers with expired, self-signed or
mismatched certificates
([Blechschmidt and Stock, USENIX Security 2023](https://www.usenix.org/conference/usenixsecurity23/presentation/blechschmidt)).

That is encryption against someone listening, not against someone who can
redirect the connection to their own server. Closing that gap, by publishing
which names and certificates to expect, is what
[MTA-STS and DANE](mta-sts-and-dane.md) are for.

## STARTTLS is hard to implement

Upgrading a connection halfway through is subtle. In 2011, Postfix's author
found that many servers, Postfix included, would treat commands an attacker
injected in the clear, right after `STARTTLS`, as if they had arrived encrypted
([Postfix: CVE-2011-0411](https://www.postfix.org/CVE-2011-0411.html)). In 2021,
a study of 28 mail apps and 23 servers found more than 40 STARTTLS flaws, some of
which let an attacker steal passwords or show a user forged mailbox contents, and
recommended implicit TLS wherever possible
([Poddebniak et al., USENIX Security 2021](https://www.usenix.org/conference/usenixsecurity21/presentation/poddebniak),
[nostarttls.secvuln.info](https://nostarttls.secvuln.info/)).

## Asking for encryption, per message

Two newer standards let a sender say what it needs:

- **REQUIRETLS** ([RFC 8689](https://www.rfc-editor.org/rfc/rfc8689)): this
  message may only travel over TLS to an authenticated server, on every hop,
  or it must bounce.
- **`TLS-Required: No`** (the same RFC): ignore the recipient's TLS policy for
  this one message, for example to tell them their certificate has expired.

## What goes wrong

- **Requiring TLS on port 25.** Mail from senders that cannot encrypt bounces.
- **Offering sign-in before encryption,** on 587 or 143 without `STARTTLS`, so a
  password can cross the network in the clear.
- **Expired or mismatched certificates.** Most senders still deliver, but
  MTA-STS and DANE senders will not, and mail apps show warnings users learn to
  click through.
- **Old TLS versions.** TLS 1.0 and 1.1 are deprecated
  ([RFC 8996](https://www.rfc-editor.org/rfc/rfc8996), 2021).
- **Trusting `STARTTLS` alone for anything that matters,** when it can be
  stripped.

## Check your own server

```sh
# Port 25: is STARTTLS offered, and what certificate comes back?
openssl s_client -starttls smtp -connect mx.example.org:25 -servername mx.example.org -brief

# Submission and IMAP over implicit TLS
openssl s_client -connect mail.example.org:465 -brief
openssl s_client -connect mail.example.org:993 -brief
```

Look at the protocol version, and whether the certificate's name matches the
host name the MX record gives.

## How REST Mail handles it

- **Port 25 offers STARTTLS on every connection, and accepts mail without it,**
  as RFC 3207 requires. A message that arrives unencrypted is recorded as such,
  so filters can treat it with more suspicion
  ([`internal/gateway/smtp/session.go`](../../../internal/gateway/smtp/session.go), `Mail`; #292).
- **Every other port is implicit TLS only:** submission on 465, IMAP on 993, POP3
  on 995, as RFC 8314 prefers. There is no setting for 587, 143 or 110, and a
  production server refuses to start without a certificate
  ([`internal/config/config.go`](../../../internal/config/config.go)).
- **TLS 1.2 or later** on every listener.
- **Certificates are chosen by the name the client asked for (SNI),** reloaded
  without a restart when they are renewed, and renewed with ACME
  ([`internal/gateway/tlsutil/`](../../../internal/gateway/tlsutil/), [`internal/acme/`](../../../internal/acme/)).
- **STARTTLS command injection:** after the handshake, REST Mail's SMTP library
  reads from a fresh reader on the encrypted connection, so nothing sent in the
  clear alongside `STARTTLS` is read as if it had been encrypted
  ([`rest-mail/go-smtp`](https://github.com/rest-mail/go-smtp)).
- **Sending:** REST Mail uses STARTTLS whenever the receiver offers it, and
  enforces a recipient's MTA-STS policy. With no policy it is opportunistic, as
  RFC 7435 and Postfix are: it encrypts whatever the certificate, checks it, and
  records each delivery as authenticated, encrypted or cleartext
  (`restmail_outbound_transport_total`), never calling an unverified
  certificate authenticated. A handshake that fails is retried on a new
  connection without TLS ([`internal/gateway/queue/worker.go`](../../../internal/gateway/queue/worker.go), #723).
- **`TLS-Required: No`** is honoured: a message that carries it goes without
  the recipient's MTA-STS policy, its policy not even looked up, with
  `STARTTLS` still used whenever the MX offers it (RFC 8689 §4.2.2).
  **REQUIRETLS** itself is not supported yet:
  [#724](https://github.com/rest-mail/rest-mail-server/issues/724).

## Sources

- STARTTLS: [RFC 3207](https://www.rfc-editor.org/rfc/rfc3207) (originally [RFC 2487](https://www.rfc-editor.org/rfc/rfc2487)).
- [RFC 8314](https://www.rfc-editor.org/rfc/rfc8314): Cleartext Considered Obsolete.
- [RFC 7435](https://www.rfc-editor.org/rfc/rfc7435): Opportunistic Security.
- [RFC 8689](https://www.rfc-editor.org/rfc/rfc8689): REQUIRETLS.
- [RFC 8996](https://www.rfc-editor.org/rfc/rfc8996): deprecating TLS 1.0 and 1.1.
- Google: [Transparency Report, email encryption in transit](https://transparencyreport.google.com/safer-email/overview) (read 2026-10-23).
- EFF: [ISPs removing their customers' email encryption (2014)](https://www.eff.org/deeplinks/2014/11/starttls-downgrade-attacks).
- Blechschmidt and Stock: [Extended Hell(o), USENIX Security 2023](https://www.usenix.org/conference/usenixsecurity23/presentation/blechschmidt).
- Poddebniak, Ising, Böck and Schinzel: [Why TLS is better without STARTTLS, USENIX Security 2021](https://www.usenix.org/conference/usenixsecurity21/presentation/poddebniak).
- Postfix: [CVE-2011-0411](https://www.postfix.org/CVE-2011-0411.html), [TLS_README](https://www.postfix.org/TLS_README.html).
- REST Mail: [`internal/gateway/smtp/`](../../../internal/gateway/smtp/),
  [`internal/gateway/queue/worker.go`](../../../internal/gateway/queue/worker.go).
