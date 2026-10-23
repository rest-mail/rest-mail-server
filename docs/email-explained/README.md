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
made interactive, so the two never drift apart. How that build works is being
designed in [docs/design/email-explained-website.md](../design/email-explained-website.md).

## Contents

### Part 1: How email works

- [The journey of a message](how-email-works/journey-of-a-message.md): from your mail app to someone else's inbox, hop by hop
- [DNS for email](how-email-works/dns-for-email.md): MX, A and AAAA, reverse DNS, null MX
- [SMTP as a conversation](how-email-works/smtp-conversation.md): `EHLO`, `MAIL FROM`, `RCPT TO`, `DATA`, and the reply codes
- [The message itself](how-email-works/the-message.md): headers, MIME, encodings, envelope versus header
- [Bounces, queues and retries](how-email-works/bounces-queues-retries.md): delivery status notifications, deferral, backscatter
- [Reading mail: POP3, IMAP, JMAP and Sieve](how-email-works/reading-mail.md)
- [Finding the server: autoconfig, Autodiscover and SRV](how-email-works/finding-the-server.md)

### Part 2: The security layers, one at a time

- [Encryption in transit: STARTTLS and implicit TLS](security/encryption-in-transit.md), and downgrade attacks
- [MTA-STS, TLS-RPT, DANE and DNSSEC](security/mta-sts-and-dane.md)
- [Who may send: SMTP AUTH, SASL and app passwords](security/who-may-send.md), and why submission needs a login
- [SPF](security/spf.md): who may send for a domain, the 10-lookup limit, and why forwarding breaks it
- [DKIM](security/dkim.md): signatures, selectors, key rotation, and what breaks them
- [DMARC](security/dmarc.md): alignment, policies, reports, and `p=reject` versus mailing lists
- [ARC](security/arc.md): authentication that survives forwarding
- [Spam defences](security/spam-defences.md): blocklists, greylisting, limits and filters
- [Bulk mail](security/bulk-mail.md): List-Unsubscribe, one-click unsubscribe, and the 2024 sender rules

### Part 3: How it goes wrong

- [The open relay and the spam machine](how-it-goes-wrong/open-relay-and-spam-machine.md)
- [The black hole: how mail is silently lost](how-it-goes-wrong/the-black-hole.md)
- [Why the security layers are so hard to get right](how-it-goes-wrong/why-security-is-hard.md)
- [Deliverability](how-it-goes-wrong/deliverability.md): reputation, warm-up, and why big providers distrust small servers

### Part 4: The landscape, and where REST Mail fits

- [The servers compared](landscape/servers-compared.md): Postfix, Exim, Sendmail, OpenSMTPD, Haraka, Dovecot, Cyrus,
  Stalwart, Maddy, and the hosted, proprietary services (Microsoft 365 and Google Workspace)
- [Containers and bundles](landscape/containers-and-bundles.md): what packaging solves, and what stays the same. How
  docker-mailserver, mailcow, Mailu, Mail-in-a-Box, iRedMail and kubernetes-mail-server package Postfix, Dovecot and
  the rest. REST Mail's author built kubernetes-mail-server, and a Docker-based server before it; this chapter draws on
  that experience
- [Efforts to modernise email](landscape/modernising-email.md): JMAP, end-to-end encryption, and others, what each
  solves, where each stands today, and how they relate to REST Mail
- [What REST Mail does differently](landscape/what-rest-mail-does-differently.md)
- [RESTMAIL: delivery between servers over HTTPS](landscape/restmail-protocol.md)
- [Migrating step by step, without breaking anything](landscape/migrating.md)
- [Solved, solving, still open](landscape/solved-solving-open.md)

### Reference

- [Glossary](reference/glossary.md)
- [Check your own server: a checklist](reference/checklist.md)

## Writing a chapter

See [WRITING.md](WRITING.md) for the chapter pattern, how sources are cited, and
how diagrams are made.
