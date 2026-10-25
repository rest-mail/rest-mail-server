---
title: Why the security layers are so hard to get right
part: How it goes wrong
summary: Each protection was added on its own, decades apart, by different people, in different places. Here is everything one domain needs, and why mistakes go unnoticed.
diagram: ../diagrams/domain-needs.tree.json
---

# Why the security layers are so hard to get right

Each chapter of [Part 2](../README.md#part-2-the-security-layers-one-at-a-time)
explains one protection, and each one, on its own, is manageable. The trouble
is that a domain needs all of them at once, and they were never designed
together. They were added one at a time over twenty years, each to fix the
problem in front of it, each with its own record, its own syntax, its own
software, and its own way of failing.

A 2023 study of email security across the top 10 million domains summed up the
result as "grim yet slightly improving"
([Blechschmidt and Stock, USENIX Security 2023](https://www.usenix.org/conference/usenixsecurity23/presentation/blechschmidt)).

![Everything one domain needs for mail that is delivered and trusted](../diagrams/domain-needs.svg)

*The diagram's source is [`domain-needs.tree.json`](../diagrams/domain-needs.tree.json).*

## Many layers, added decades apart

| Year | Layer | Standard |
|---|---|---|
| 1982 | SMTP: no encryption, no authentication | [RFC 821](https://www.rfc-editor.org/rfc/rfc821) |
| 1999 | STARTTLS, and sign-in for sending | [RFC 2487](https://www.rfc-editor.org/rfc/rfc2487), [RFC 2554](https://www.rfc-editor.org/rfc/rfc2554) |
| 2006 | SPF (experimental; standard in 2014) | [RFC 4408](https://www.rfc-editor.org/rfc/rfc4408), [RFC 7208](https://www.rfc-editor.org/rfc/rfc7208) |
| 2007 | DKIM (revised 2011) | [RFC 4871](https://www.rfc-editor.org/rfc/rfc4871), [RFC 6376](https://www.rfc-editor.org/rfc/rfc6376) |
| 2015 | DMARC (informational) | [RFC 7489](https://www.rfc-editor.org/rfc/rfc7489) |
| 2015 | DANE for SMTP | [RFC 7672](https://www.rfc-editor.org/rfc/rfc7672) |
| 2018 | MTA-STS and TLS-RPT | [RFC 8461](https://www.rfc-editor.org/rfc/rfc8461), [RFC 8460](https://www.rfc-editor.org/rfc/rfc8460) |
| 2019 | ARC (experimental) | [RFC 8617](https://www.rfc-editor.org/rfc/rfc8617) |
| 2026 | DMARC as a standard | [RFC 9989](https://www.rfc-editor.org/rfc/rfc9989) |

Every one of them had to work with the mail system as it already was, without
breaking any server that did not know about it yet. So each is optional, each
is checked separately, and each fails in its own way.

## Many places, many owners

For one domain to send and receive mail that other servers trust, these all
have to be right at the same time:

- **In the domain's DNS:** MX, A and AAAA records, an SPF record, a DKIM key for
  each selector, a DMARC record, the MTA-STS and TLS-RPT records, SRV and
  autoconfig records for mail apps, and TLSA records if it uses DANE.
- **Somewhere else:** the reverse DNS (PTR) record, set by whoever owns the IP
  address, usually the hosting provider; DNSSEC, which also needs a record at
  the registrar.
- **On web servers:** the MTA-STS policy file at `mta-sts.<domain>`, and the
  autoconfig file, each over HTTPS with its own certificate.
- **In certificates:** one valid for the MX name, for submission, for IMAP, for
  `mta-sts.`, for `autoconfig.`, renewed every few months.
- **In keys:** DKIM private keys, rotated, with the old selector kept until mail
  signed with it has arrived.
- **In software:** in the classic stack, a different program for each job. One
  Postfix and Dovecot installation commonly adds OpenDKIM, OpenDMARC, Rspamd or
  SpamAssassin, ClamAV, Postgrey and Fail2ban, as the bundles in
  [Containers and bundles](../landscape/containers-and-bundles.md) show. Each has its own configuration file and syntax.
- **In reports:** DMARC and TLS-RPT reports, arriving daily, as XML and JSON
  attachments, to be read by someone.

Often these belong to different people: the DNS to one team, the IP address to
the hosting provider, the web server to another team, the certificates to an
automation nobody remembers setting up.

## Mistakes are quiet

What makes the layers truly hard is not their number. It is that almost every
mistake is silent where it is made, and shows up somewhere else, later:

- **A broken SPF record** (eleven lookups, or two records) does not stop your
  server sending. It makes other servers distrust your mail, sometimes, by an
  amount you cannot see.
- **A DKIM key deleted too early** fails only the mail in transit, and only
  at receivers that check.
- **An expired certificate on `mta-sts.`** changes nothing until senders'
  cached policies expire, weeks later.
- **A missing PTR record** shows up as a few providers junking your mail, while
  others deliver it.
- **A DMARC record at `p=reject`** costs nothing until a user posts to a mailing
  list ([DMARC](../security/dmarc.md#why-preject-and-mailing-lists-do-not-mix)).

The feedback, when there is any, arrives as a bounce days later, a report
nobody reads, or a user saying "they never got my email". The 2023 study found
the same at scale: 30% of MX certificates invalid, only about 40% of domains
publishing SPF, and DKIM and DANE "neither [...] validated or enforced by all
providers" ([Blechschmidt and Stock, 2023](https://www.usenix.org/conference/usenixsecurity23/presentation/blechschmidt)).

## Tutorials make it worse

Most mail servers are built by following a tutorial, and each tutorial covers
the layers that existed when it was written, in the syntax of the software
versions it used. A server built from a 2014 guide has no MTA-STS; one built
from a 2008 guide may have no DKIM. Each later guide adds a layer on top, in a
different style. [The open relay and the spam machine](open-relay-and-spam-machine.md#why-age-makes-it-worse)
shows how that goes wrong for relaying; it goes wrong the same way for every
layer here.

## What would make it easier

Not fewer protections: each one closes a real hole. What helps is having one
place that knows all of them:

- **One list of every record a domain needs,** generated, not copied from a
  tutorial, and checked against what is actually published.
- **Keys and certificates made, rotated and renewed automatically,** with the
  DNS changes they need published in the right order.
- **One program doing the jobs,** so the protections are configured together,
  in one way.
- **Reports read by software,** and turned into plain warnings.

## How REST Mail handles it

REST Mail is built around that list:

- **One server, one configuration.** SMTP, IMAP, POP3, signing, checking and
  filtering are one program behind one REST API, not a stack of daemons
  ([What REST Mail does differently](../landscape/what-rest-mail-does-differently.md)).
- **Records generated and checked:** `chore dns:show` prints the A, MX, SPF,
  DKIM, DMARC and PTR records each domain needs, from the server's own state, and
  `chore dns:check` compares them with what is published
  ([`internal/dns/mail_records.go`](../../../internal/dns/mail_records.go)), with
  the SRV records clients find the server by (#712). The DMARC record asks for
  the domain's own policy, `p=none` for a new domain, raised by an admin once
  the reports show its mail passes (#729). Not yet included: MTA-STS and TLS-RPT
  ([#727](https://github.com/rest-mail/rest-mail-server/issues/727),
  [#726](https://github.com/rest-mail/rest-mail-server/issues/726)).
- **Keys made for you:** each domain's DKIM key is generated at 2048 bits and
  stored encrypted; automatic rotation is
  [#728](https://github.com/rest-mail/rest-mail-server/issues/728).
- **Certificates renewed with ACME,** and reloaded without a restart.
- **Reports read:** DMARC reports are sent and TLS-RPT reports received by the
  server itself; the gaps are in [#726](https://github.com/rest-mail/rest-mail-server/issues/726)
  and [#730](https://github.com/rest-mail/rest-mail-server/issues/730).

## Sources

- Blechschmidt and Stock: [Extended Hell(o), USENIX Security 2023](https://www.usenix.org/conference/usenixsecurity23/presentation/blechschmidt).
- The RFCs in the table above.
- REST Mail: [`internal/dns/`](../../../internal/dns/),
  [`internal/outbound/`](../../../internal/outbound/),
  [`internal/acme/`](../../../internal/acme/).
