---
title: The message itself
part: How email works
summary: "What an email is made of: the envelope that routes it, the header the reader sees, and the MIME parts that carry text, HTML and attachments."
diagram: ../diagrams/message.tree.json
---

# The message itself

An email is a text file. Open one with "Show original" in any mail app and you
see it: lines of `Name: value` at the top, a blank line, then the body. Everything
else, the formatting, the pictures, the attachments, the non-English text, is
encoded into that text, because the system it travels through was built for
7-bit ASCII in 1982 and has had to stay compatible ever since.

There is also a part you never see in that file: the **envelope**, which
decides where the message actually goes.

![One email, taken apart](../diagrams/message.svg)

*The diagram's source is [`message.tree.json`](../diagrams/message.tree.json).*

## The envelope and the header are different things

Think of a paper letter. The address on the envelope is what the postal service
reads. The letter inside has its own "Dear Bob" and "From Alice", which only the
reader sees. They usually agree. Nothing makes them.

Email works the same way:

| | The envelope | The header |
|---|---|---|
| What it is | The SMTP commands `MAIL FROM` and `RCPT TO` ([SMTP](smtp-conversation.md)) | The `From:`, `To:`, `Cc:` lines inside the message |
| Who reads it | Mail servers, to route the message | The reader's mail app, to display it |
| Defined by | [RFC 5321](https://www.rfc-editor.org/rfc/rfc5321) | [RFC 5322](https://www.rfc-editor.org/rfc/rfc5322) |
| The sender | `MAIL FROM`: where bounces go, also called the **return path** | `From:`: the author |
| The recipients | One `RCPT TO` per recipient, Bcc included | `To:` and `Cc:`, never Bcc |

Two consequences matter every day:

- **Bcc works because of the envelope.** A Bcc recipient gets a `RCPT TO`, and
  appears nowhere in the header. A mail app or server that leaves a `Bcc:` line
  in the message it sends reveals who was copied.
- **The `From:` line can say anything.** Nothing in SMTP checks it against the
  envelope, or against who connected. That is why phishing works, and why
  [DMARC](../security/dmarc.md) exists: it is the first standard to check the
  `From:` line the reader actually sees.

When the message is finally delivered, the server records the envelope sender
in a `Return-Path:` header line, so the reader can see where bounces would have
gone ([RFC 5321 §4.4](https://www.rfc-editor.org/rfc/rfc5321#section-4.4)).

## The header

Each header line is a field name, a colon, and a value. RFC 5322 requires only
two of them: "The only required header fields are the origination date field
and the originator address field(s)"
([§3.6](https://www.rfc-editor.org/rfc/rfc5322#section-3.6)), which are `Date:`
and `From:`. Every message should also have a `Message-ID:`, a unique name used
to thread replies (`In-Reply-To:`, `References:`).

Others are added along the way:

- **`Received:`**, one per server the message passed through, newest first
  ([The journey of a message](journey-of-a-message.md#every-hop-leaves-a-trace)).
- **`DKIM-Signature:`**, the sender's signature ([DKIM](../security/dkim.md)).
- **`Authentication-Results:`**, the receiving server's verdict on SPF, DKIM and
  DMARC ([RFC 8601](https://www.rfc-editor.org/rfc/rfc8601)).
- **`List-Unsubscribe:`** on mailing lists and newsletters ([Bulk mail](../security/bulk-mail.md)).

A header line may not be longer than 998 characters, and should be no longer
than 78; a long value is "folded" onto the next line, which starts with a space
([RFC 5322 §2.1.1](https://www.rfc-editor.org/rfc/rfc5322#section-2.1.1)).

## Text that is not ASCII

The original format allowed only ASCII. Two ways around that exist:

- **Encoded words** ([RFC 2047](https://www.rfc-editor.org/rfc/rfc2047)) wrap
  non-ASCII header text in an ASCII disguise:
  `Subject: =?UTF-8?B?Q2Fmw6k=?=` is "Café".
- **SMTPUTF8** ([RFC 6531](https://www.rfc-editor.org/rfc/rfc6531),
  [RFC 6532](https://www.rfc-editor.org/rfc/rfc6532)) lets UTF-8 appear directly
  in headers and addresses, between servers that both announce it. It is what
  makes an address like `用户@例子.广告` possible.

## MIME: one message, many parts

**MIME** ([RFC 2045](https://www.rfc-editor.org/rfc/rfc2045) to
[RFC 2049](https://www.rfc-editor.org/rfc/rfc2049)) turns a body into a tree of
parts. Each part has its own small header saying what it is (`Content-Type:`)
and how it is encoded for the trip (`Content-Transfer-Encoding:`). A typical
message with an HTML body and an attachment looks like this, shortened:

```
From: Alice <alice@example.com>
To: Bob <bob@example.org>
Subject: The report
Date: Fri, 23 Oct 2026 12:00:00 +0000
Message-ID: <5678@example.com>
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="outer"

--outer
Content-Type: multipart/alternative; boundary="inner"

--inner
Content-Type: text/plain; charset=UTF-8
Content-Transfer-Encoding: quoted-printable

Here is the report. Caf=C3=A9 on Tuesday?
--inner
Content-Type: text/html; charset=UTF-8

<p>Here is the report. Café on Tuesday?</p>
--inner--
--outer
Content-Type: application/pdf; name="report.pdf"
Content-Disposition: attachment; filename="report.pdf"
Content-Transfer-Encoding: base64

JVBERi0xLjcKJcfsj6IKNSAwIG9iago8PC9MZW5ndGggNiAwIFI...
--outer--
```

- **`multipart/mixed`** holds parts that belong together: the message and its
  attachments.
- **`multipart/alternative`** holds the same content in several forms, plainest
  first. A mail app shows the last one it can display: the "best choice is
  the LAST part of a type supported by the recipient"
  ([RFC 2046 §5.1.4](https://www.rfc-editor.org/rfc/rfc2046#section-5.1.4)).
- **The boundary** is a string that marks where each part starts and ends. It
  must not appear inside any part.
- **`Content-Disposition`** says whether a part is shown inline or offered as an
  attachment ([RFC 2183](https://www.rfc-editor.org/rfc/rfc2183)).

## The encodings

| Encoding | What it is for | Cost |
|---|---|---|
| `7bit` | Plain ASCII text, short lines | None |
| `8bit` | Text with non-ASCII bytes, to servers that announce `8BITMIME` | None |
| `quoted-printable` | Mostly-ASCII text with some accented letters: `é` becomes `=C3=A9` | Small, for mostly-ASCII text |
| `base64` | Anything binary: images, PDFs, archives | A third bigger: 3 bytes become 4 characters |

The base64 cost is why a 15 MB attachment can hit a 20 MB message limit: once
encoded, it is about 20 MB.

## What goes wrong

- **A `Bcc:` line left in the sent message,** revealing who was copied.
- **Missing `Date:` or `Message-ID:`.** Gmail requires every sender to format
  messages "according to the Internet Message Format standard, RFC 5322"
  ([Google](https://support.google.com/a/answer/81126)), and spam filters
  score malformed headers.
- **Changing a message in transit.** A mailing list that adds a footer or a
  `[list]` tag to the subject changes signed content, and breaks the DKIM
  signature ([DKIM](../security/dkim.md), [ARC](../security/arc.md)).
- **HTML only.** A message with no plain text alternative is harder to read for
  some readers, and scores worse with some filters.
- **Size limits after encoding.** An attachment that fits the limit as a file
  may not fit once base64 has grown it by a third.

## How REST Mail handles it

- **MIME** is parsed with [`emersion/go-message`](https://github.com/emersion/go-message),
  into the parts the REST API returns: text, HTML, attachments and inline
  images ([`internal/mime/`](../../../internal/mime/)).
- **Bcc is stripped** from every message before it is relayed, once, in the
  same place it is DKIM-signed, so no send path can forget
  ([`internal/outbound/prepare.go`](../../../internal/outbound/prepare.go), #171).
- **Header checks** are a pipeline filter: by default it requires `From:` and
  `Date:`, and rejects header injection; requiring `Message-ID:` is a setting
  ([`header_validate.go`](../../../internal/pipeline/filters/header_validate.go)).
- **Message-IDs** REST Mail creates are random and unique, in the sender's
  domain ([`internal/mail/messageid.go`](../../../internal/mail/messageid.go)).
- **UTF-8 throughout.** REST Mail accepts SMTPUTF8, and for an IMAP or POP3 app
  that has not asked for UTF-8 it rewrites the header into ASCII encoded words,
  as RFC 6855 to RFC 6858 describe, leaving the body untouched
  ([`internal/gateway/rawmsg/downgrade.go`](../../../internal/gateway/rawmsg/downgrade.go)).

## Sources

- [RFC 5322](https://www.rfc-editor.org/rfc/rfc5322): Internet Message Format.
- [RFC 5321 §4.4](https://www.rfc-editor.org/rfc/rfc5321#section-4.4): trace fields and `Return-Path`.
- MIME: [RFC 2045](https://www.rfc-editor.org/rfc/rfc2045),
  [RFC 2046](https://www.rfc-editor.org/rfc/rfc2046),
  [RFC 2047](https://www.rfc-editor.org/rfc/rfc2047),
  [RFC 2049](https://www.rfc-editor.org/rfc/rfc2049),
  [RFC 2183](https://www.rfc-editor.org/rfc/rfc2183).
- Internationalised email: [RFC 6531](https://www.rfc-editor.org/rfc/rfc6531),
  [RFC 6532](https://www.rfc-editor.org/rfc/rfc6532),
  [RFC 6855](https://www.rfc-editor.org/rfc/rfc6855),
  [RFC 6856](https://www.rfc-editor.org/rfc/rfc6856).
- [RFC 8601](https://www.rfc-editor.org/rfc/rfc8601): Authentication-Results.
- Google: [Email sender guidelines](https://support.google.com/a/answer/81126).
- REST Mail: [`internal/mime/`](../../../internal/mime/),
  [`internal/outbound/`](../../../internal/outbound/),
  [`internal/gateway/rawmsg/`](../../../internal/gateway/rawmsg/).
