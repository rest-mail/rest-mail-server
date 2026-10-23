# Email, explained

How email actually works, why mail servers are so easy to get wrong, what fifty
years of fixes have solved, and what is still broken. Written for anyone who
has tried to run a mail server, or wondered why it is so hard.

It is the guide we wished we had when we were learning. It is written to be
useful whatever server you run; REST Mail's own approach comes last in each
chapter, after the problem and the standard fixes, never instead of them.

## How to read it

Each chapter follows the same pattern:

1. **The problem.** What goes wrong without this, in plain words.
2. **How it works.** The protocol or setting, with a diagram.
3. **What goes wrong.** The common mistakes, and why they are easy to make.
4. **How REST Mail handles it.** With links to the code that does it.
5. **Sources.** The RFCs and documentation every claim comes from.

These files are the source of truth. The guide on
[rest-mail.com](https://rest-mail.com) is built from them, with the diagrams
made interactive, so the two never drift apart.

## Contents

Chapters with a link are written. The rest are planned.

### Part 1: How email works

- The journey of a message: from your mail app to someone else's inbox, hop by hop
- DNS for email: MX, A and AAAA, reverse DNS, null MX
- SMTP as a conversation: `EHLO`, `MAIL FROM`, `RCPT TO`, `DATA`, and the reply codes
- The message itself: headers, MIME, encodings, envelope versus header
- Bounces, queues and retries: delivery status notifications, deferral, backscatter
- Reading mail: POP3, IMAP, JMAP, Sieve and ManageSieve
- Finding the server: autoconfig, Autodiscover and SRV records

### Part 2: The security layers, one at a time

- Encryption in transit: STARTTLS versus implicit TLS, and downgrade attacks
- MTA-STS, TLS-RPT, DANE and DNSSEC
- Who may send: SMTP AUTH, SASL, app passwords, and why submission needs a login
- SPF: who may send for a domain, the 10-lookup limit, and why forwarding breaks it
- DKIM: signatures, selectors, key rotation, and what breaks them
- DMARC: alignment, policies, reports, and `p=reject` versus forwarding
- ARC: authentication that survives forwarding
- Spam defences: blocklists, greylisting, rate limits, content scanning, reputation
- Bulk mail: List-Unsubscribe, one-click unsubscribe, and the 2024 sender rules

### Part 3: How it goes wrong

- [The open relay and the spam machine](how-it-goes-wrong/open-relay-and-spam-machine.md)
- The black hole: how mail is silently lost
- Why the security layers are so hard to get right
- Deliverability: reputation, warm-up, and why big providers distrust small servers

### Part 4: The landscape, and where REST Mail fits

- The servers compared: Postfix, Exim, Sendmail, OpenSMTPD, Dovecot, Stalwart,
  Maddy, Haraka, and the paid, proprietary services (Microsoft Exchange and
  Microsoft 365, Google Workspace)
- Containers and bundles: what packaging solves, and what stays the same. How
  docker-mailserver, mailcow, Mailu, Mail-in-a-Box, iRedMail and
  kubernetes-mail-server package Postfix, Dovecot and the rest, what that
  makes easier, and what is still inside. REST Mail's author built
  kubernetes-mail-server, and a Docker-based server before it; this chapter
  draws on that experience
- Efforts to modernise email: JMAP and others, what each solves, where each
  stands today, and how they relate to REST Mail
- What REST Mail does differently
- RESTMAIL: delivery between servers over HTTPS
- Migrating step by step, without breaking anything
- Solved, solving, still open

### Reference

- Glossary
- Check your own server: a checklist

## Writing a chapter

See [WRITING.md](WRITING.md) for the chapter pattern, how sources are cited, and
how diagrams are made.
