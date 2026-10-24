---
title: SMTP as a conversation
part: How email works
summary: The commands and replies two mail servers exchange, line by line, what the reply codes mean, and how to hold the conversation yourself.
diagram: ../diagrams/smtp.sequence.json
---

# SMTP as a conversation

SMTP, the Simple Mail Transfer Protocol, is how every email moves between
servers, and from a mail app to its own server. It was first published in 1982
([RFC 821](https://www.rfc-editor.org/rfc/rfc821)). Today's version is
[RFC 5321](https://www.rfc-editor.org/rfc/rfc5321), from 2008, and it is still
a conversation in plain text: one side sends a command, the other answers with
a number and a sentence.

That is good news for anyone running a mail server. Every log line, every
bounce and every error message is a piece of this conversation. Once you can
read it, you can tell where a message stopped, and why.

![The SMTP conversation between two servers](../diagrams/smtp.svg)

*The diagram's source is [`smtp.sequence.json`](../diagrams/smtp.sequence.json).*

## A whole conversation

Here is one message delivered from `mail.example.com` to `mx.example.org`. The
client's lines are marked `C:` and the server's `S:`.

```
S: 220 mx.example.org ESMTP ready
C: EHLO mail.example.com
S: 250-mx.example.org Hello mail.example.com
S: 250-SIZE 52428800
S: 250-8BITMIME
S: 250-PIPELINING
S: 250-STARTTLS
S: 250 ENHANCEDSTATUSCODES
C: STARTTLS
S: 220 2.0.0 Ready to start TLS
   (the TLS handshake; everything after it is encrypted)
C: EHLO mail.example.com
S: 250-mx.example.org Hello mail.example.com
S: 250 ...
C: MAIL FROM:<alice@example.com>
S: 250 2.1.0 OK
C: RCPT TO:<bob@example.org>
S: 250 2.1.5 OK
C: DATA
S: 354 Start mail input; end with <CRLF>.<CRLF>
C: From: Alice <alice@example.com>
C: To: Bob <bob@example.org>
C: Subject: Lunch?
C: Date: Fri, 23 Oct 2026 12:00:00 +0000
C: Message-ID: <1234@example.com>
C:
C: Are you free on Tuesday?
C: .
S: 250 2.0.0 OK queued as 7F3A1
C: QUIT
S: 221 2.0.0 Bye
```

## The commands

- **`EHLO`** ("extended hello") opens the conversation and names the client.
  The server answers with the **extensions** it supports, one per line. The old
  `HELO` does the same without extensions
  ([RFC 5321 §4.1.1.1](https://www.rfc-editor.org/rfc/rfc5321#section-4.1.1.1)).
- **`STARTTLS`** upgrades the connection to an encrypted one, after which the
  client starts again with `EHLO`
  ([RFC 3207](https://www.rfc-editor.org/rfc/rfc3207)). Why that upgrade can be
  stripped by an attacker is in [Encryption in transit](../security/encryption-in-transit.md).
- **`AUTH`** signs in, on a submission port
  ([RFC 4954](https://www.rfc-editor.org/rfc/rfc4954); see [Who may send](../security/who-may-send.md)).
- **`MAIL FROM`** starts a message and gives the **envelope sender**: where
  bounces go. It is often different from the `From:` line the reader sees
  ([The message itself](the-message.md)).
- **`RCPT TO`** gives one recipient. A message for three people has three
  `RCPT TO` lines, and the server answers each one separately.
- **`DATA`** sends the message itself: header lines, a blank line, then the
  body. It ends with a line holding a single dot. A body line that really
  starts with a dot gets a second one added, which the server removes
  ("dot-stuffing", [§4.5.2](https://www.rfc-editor.org/rfc/rfc5321#section-4.5.2)).
- **`RSET`** abandons the current message, **`NOOP`** does nothing (useful to
  keep a connection alive), and **`QUIT`** ends the conversation.
- **`VRFY`** and **`EXPN`** ask whether an address exists, or what a list
  expands to. Most servers disable them, because they hand spammers a list of
  real addresses; RFC 5321 allows that
  ([§7.3](https://www.rfc-editor.org/rfc/rfc5321#section-7.3)).

## Reading the replies

Every reply starts with three digits. The first one says what kind of answer it
is ([RFC 5321 §4.2.1](https://www.rfc-editor.org/rfc/rfc5321#section-4.2.1)):

| First digit | Meaning | What the client does |
|---|---|---|
| `2` | Done | Carries on |
| `3` | Go on, send more | Sends the message data |
| `4` | Failed **for now** | Keeps the message, and tries again later |
| `5` | Failed **for good** | Gives up, and sends a bounce |

The difference between `4` and `5` decides whether a message is retried for
days or bounced at once. Getting it wrong either way hurts: a `4` for a
mailbox that will never exist fills queues, and a `5` for a passing problem
loses mail.

Most servers add an **enhanced status code** after the number, such as
`5.1.1`. Its three parts give the class (2, 4 or 5 again), the subject (1
addressing, 2 mailbox, 3 mail system, 4 network, 5 protocol, 6 content, 7
security) and the detail ([RFC 3463](https://www.rfc-editor.org/rfc/rfc3463),
announced by `ENHANCEDSTATUSCODES`, [RFC 2034](https://www.rfc-editor.org/rfc/rfc2034)).

The replies you will meet most often:

| Reply | Meaning |
|---|---|
| `220` | Ready (the greeting, and the go-ahead for `STARTTLS`) |
| `221` | Closing the connection |
| `250` | OK |
| `354` | Send the message data |
| `421` | Service not available now; try later |
| `450` / `451` | Not now: mailbox busy, greylisted, local error |
| `452` | Not now: out of storage, or too many recipients |
| `454 4.7.1` | Not now: often "Relay access denied" (Postfix's default) |
| `530 5.7.0` | Sign in first |
| `535 5.7.8` | Sign-in failed |
| `550 5.1.1` | No such mailbox |
| `550 5.7.1` | Refused by policy: blocklisted, failed DMARC, not allowed to relay |
| `552` | Message too big, or mailbox full |
| `554` | Transaction failed |
| `556 5.1.10` | The domain accepts no mail ([null MX](dns-for-email.md#null-mx-this-domain-takes-no-mail)) |

The sentence after the code is for people, and servers put useful detail there:
which blocklist listed you, which DMARC policy failed. Read it.

## Extensions

`EHLO`'s answer lists what the server can do beyond the 1982 basics. Each was
added by an RFC of its own:

| Extension | What it adds | Standard |
|---|---|---|
| `SIZE` | The largest message accepted, so a client need not send a huge one only to be refused | [RFC 1870](https://www.rfc-editor.org/rfc/rfc1870) |
| `8BITMIME` | Message bodies with bytes above 127, not only 7-bit ASCII | [RFC 6152](https://www.rfc-editor.org/rfc/rfc6152) |
| `PIPELINING` | Several commands sent without waiting for each reply | [RFC 2920](https://www.rfc-editor.org/rfc/rfc2920) |
| `STARTTLS` | Upgrade to an encrypted connection | [RFC 3207](https://www.rfc-editor.org/rfc/rfc3207) |
| `AUTH` | Sign in | [RFC 4954](https://www.rfc-editor.org/rfc/rfc4954) |
| `ENHANCEDSTATUSCODES` | The `x.y.z` codes | [RFC 2034](https://www.rfc-editor.org/rfc/rfc2034) |
| `CHUNKING` | `BDAT`: the message sent in counted chunks, with no dot-stuffing | [RFC 3030](https://www.rfc-editor.org/rfc/rfc3030) |
| `SMTPUTF8` | Addresses and headers in any script, such as `用户@例子.广告` | [RFC 6531](https://www.rfc-editor.org/rfc/rfc6531) |
| `DSN` | Ask for a delivery report, success or failure | [RFC 3461](https://www.rfc-editor.org/rfc/rfc3461) |
| `REQUIRETLS` | This message may only travel encrypted | [RFC 8689](https://www.rfc-editor.org/rfc/rfc8689) |
| `LIMITS` | The server's limits, such as recipients per message | [RFC 9422](https://www.rfc-editor.org/rfc/rfc9422) |

## Hold the conversation yourself

Talking to a server by hand is the quickest way to see what it does. Port 25
on most servers upgrades with `STARTTLS`:

```sh
openssl s_client -starttls smtp -connect mx.example.org:25 -crlf
```

Then type `EHLO test.example`, and read the extensions. To send a test message
with less typing, [swaks](https://github.com/jetmore/swaks) holds the whole
conversation and prints it:

```sh
swaks --server mx.example.org --to postmaster@example.org
```

## What goes wrong

- **Line endings.** SMTP lines end with carriage return and line feed (CRLF).
  Some servers have long also accepted a bare line feed. In December 2023, SEC
  Consult showed that two servers which disagree about where a message ends
  can be made to accept a second, forged message hidden inside the first:
  **SMTP smuggling**
  ([SEC Consult, 2023](https://sec-consult.com/blog/detail/smtp-smuggling-spoofing-e-mails-worldwide/)).
  Postfix's author explains the fixes Postfix shipped, and why the bare line
  feed was accepted in the first place, in
  [SMTP Smuggling](https://www.postfix.org/smtp-smuggling.html).
- **A `4` that should be a `5`, or the reverse.** See the table above.
- **A greeting that lies.** A server's `EHLO` name should be its real host name,
  with matching DNS. Many receivers score or refuse clients whose `EHLO` does
  not resolve.
- **Talking before the greeting.** A client that sends commands before the
  server's `220` is almost always a spam bot. Postfix's postscreen waits a few
  seconds to catch exactly that
  ([POSTSCREEN_README](https://www.postfix.org/POSTSCREEN_README.html)).

## How REST Mail handles it

REST Mail's SMTP is [`rest-mail/go-smtp`](https://github.com/rest-mail/go-smtp),
a fork of [`emersion/go-smtp`](https://github.com/emersion/go-smtp), with a
test for each RFC it implements. The gateway configures it in
[`internal/gateway/smtp/server.go`](../../../internal/gateway/smtp/server.go):

- **Extensions advertised:** `PIPELINING`, `8BITMIME`, `ENHANCEDSTATUSCODES`,
  `CHUNKING`, `SMTPUTF8`, `SIZE`, `LIMITS RCPTMAX=100`, `STARTTLS` on port 25,
  `AUTH` only on an encrypted submission connection, and `RESTMAIL`, its own
  extension for delivery over HTTPS ([RESTMAIL](../landscape/restmail-protocol.md)).
- **Only CRLF ends a line.** A command ended by a bare line feed is refused
  with `500 5.5.2` and the connection is closed. Only `<CR><LF>.<CR><LF>` ends
  a message, which is what SMTP smuggling relies on servers getting wrong.
- **The server waits at least 5 minutes** for the next command, as RFC 5321
  asks, and a message that is still arriving at a reasonable rate is never cut
  off for being large (`transfer_rate.go`).
- **As a client,** delivering to other servers, REST Mail today times each
  attempt as a whole, where RFC 5321 asks for a timeout per command, with 5
  minutes for the greeting and 10 for the final reply. Changing that is
  [#714](https://github.com/rest-mail/rest-mail-server/issues/714).

## Sources

- [RFC 5321](https://www.rfc-editor.org/rfc/rfc5321): Simple Mail Transfer Protocol.
  [RFC 821](https://www.rfc-editor.org/rfc/rfc821): the 1982 original.
- [RFC 3463](https://www.rfc-editor.org/rfc/rfc3463): Enhanced Mail System Status Codes.
- The extensions: [RFC 1870](https://www.rfc-editor.org/rfc/rfc1870),
  [RFC 2034](https://www.rfc-editor.org/rfc/rfc2034),
  [RFC 2920](https://www.rfc-editor.org/rfc/rfc2920),
  [RFC 3030](https://www.rfc-editor.org/rfc/rfc3030),
  [RFC 3207](https://www.rfc-editor.org/rfc/rfc3207),
  [RFC 3461](https://www.rfc-editor.org/rfc/rfc3461),
  [RFC 4954](https://www.rfc-editor.org/rfc/rfc4954),
  [RFC 6152](https://www.rfc-editor.org/rfc/rfc6152),
  [RFC 6531](https://www.rfc-editor.org/rfc/rfc6531),
  [RFC 8689](https://www.rfc-editor.org/rfc/rfc8689),
  [RFC 9422](https://www.rfc-editor.org/rfc/rfc9422).
- SMTP smuggling: [SEC Consult (2023)](https://sec-consult.com/blog/detail/smtp-smuggling-spoofing-e-mails-worldwide/),
  [Postfix: SMTP Smuggling](https://www.postfix.org/smtp-smuggling.html).
- Postfix: [POSTSCREEN_README](https://www.postfix.org/POSTSCREEN_README.html).
- REST Mail: [`internal/gateway/smtp/`](../../../internal/gateway/smtp/),
  [`rest-mail/go-smtp`](https://github.com/rest-mail/go-smtp).
