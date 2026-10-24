---
title: "Spam defences: blocklists, greylisting, limits and filters"
part: The security layers, one at a time
summary: The layers a receiving server uses to keep spam out, from cheap checks on the connection to expensive ones on the content, and what each costs legitimate senders.
diagram: ../diagrams/spam.workflow.json
---

# Spam defences: blocklists, greylisting, limits and filters

Nearly half of all email is spam. Kaspersky measured that "In 2024, spam emails
accounted for 47.27% of the total global email traffic"
([Kaspersky, Spam and phishing in 2024](https://securelist.com/spam-phishing-report-2024/115536/)).
A server that accepts mail from the internet has to sort it, millions of times
a day, without losing the mail that matters.

No single check does it. Defences are layered along the SMTP conversation:
cheap checks first, on the connection, before the spammer has sent anything;
expensive ones last, on the content. Each layer turns away what it can, and
passes the rest on.

![The layers of a receiving server's spam defences](../diagrams/spam.svg)

*The diagram's source is [`spam.workflow.json`](../diagrams/spam.workflow.json).*

## On the connection: who is this?

- **DNS blocklists (DNSBLs)** list addresses known to send spam. The receiver
  reverses the connecting address and looks it up as a DNS name: for
  `192.0.2.99` and the list `zen.example.org`, it asks for
  `99.2.0.192.zen.example.org`. An answer means listed
  ([RFC 5782](https://www.rfc-editor.org/rfc/rfc5782)). Spamhaus and others run
  them; some list known spammers, others list home connections that should not
  send mail directly at all.
- **Reverse DNS:** an address with no PTR record, or a generic one
  (`host-192-0-2-99.dynamic.example`), is very likely a home computer, not a
  mail server ([DNS for email](../how-email-works/dns-for-email.md#a-aaaa-and-reverse-dns)).
- **Talking too early:** a client that sends commands before the server's
  greeting is almost always a bot. Postfix's postscreen waits a few seconds
  before greeting, to catch exactly that
  ([POSTSCREEN_README](https://www.postfix.org/POSTSCREEN_README.html)).
- **Connection limits:** too many connections from one address at once, or too
  many failed sign-ins, and it is slowed or banned for a while.

## In the envelope: who says they are sending, to whom?

- **[SPF](spf.md)** on the `MAIL FROM` and `HELO` identities.
- **Does the recipient exist?** Refusing unknown recipients with `550 5.1.1`
  during the conversation, rather than bouncing later, avoids
  [backscatter](../how-email-works/bounces-queues-retries.md#refuse-dont-bounce).
  Bots that try every common name (`admin@`, `info@`, `sales@`) can be slowed
  down after their first few misses.
- **Greylisting:** refuse the first attempt from an unfamiliar combination of
  sending address, sender and recipient with a temporary `451`, and accept the
  retry a few minutes later. Real mail servers retry; many spam tools did not.
  The idea was published by Evan Harris in 2003
  ([The Next Step in the Spam Control War: Greylisting](http://projects.puremagic.com/greylisting/whitepaper.html)),
  and standardised in [RFC 6647](https://www.rfc-editor.org/rfc/rfc6647) (2012).
  Its cost: legitimate first-time mail is delayed by minutes, sometimes more,
  when the sender retries from a different address.

## After the message: is it what it claims, and is it wanted?

- **[DKIM](dkim.md), [DMARC](dmarc.md) and [ARC](arc.md)**, which need the whole
  message.
- **Content filters** score the message on hundreds of rules: phrases, links to
  known bad sites, broken formatting, mismatched headers, hashes of known spam,
  statistical (Bayesian) models trained on each site's own mail. Common ones are
  [Apache SpamAssassin](https://spamassassin.apache.org/) and
  [Rspamd](https://rspamd.com/). Above one score the message is junked, above a
  higher one refused.
- **Virus scanning,** usually [ClamAV](https://www.clamav.net/).
- **Reputation:** many receivers also weigh what they know of the sending
  domain and address over time ([Deliverability](../how-it-goes-wrong/deliverability.md)).

The decision, ideally, is made before the final `250` of the conversation, so a
refused message is the sender's to bounce, not yours.

## After delivery: the user

- **The junk folder** catches what scored high but not high enough to refuse.
- **"Report spam"** buttons train the filters and, at large providers, feed the
  reputation of the sender. Senders whose mail gets reported too often are
  throttled or blocked ([Bulk mail](bulk-mail.md)).

## What goes wrong

- **Blocklisting too eagerly:** a blocklist that lists whole networks catches
  legitimate small senders on them. Know what each list you use is for.
- **Greylisting the big providers,** which send from large pools of addresses
  and may retry from a different one each time, delaying their mail for hours.
- **Bouncing instead of refusing,** and so sending backscatter.
- **Content filtering after accepting,** then silently discarding: the sender
  thinks it was delivered, the recipient never sees it
  ([The black hole](../how-it-goes-wrong/the-black-hole.md)).
- **Filters nobody watches,** whose rules and training go stale.

## How REST Mail handles it

Each domain has an inbound pipeline of filters, run in order, each able to
continue, reject, quarantine, defer or discard
([`internal/pipeline/`](../../../internal/pipeline/), [`docs/adapter-filters.md`](../../adapter-filters.md)):

- **Recipients are checked during the conversation:** an unknown recipient is
  refused with `550 5.1.1`, and a connection that keeps missing is slowed down,
  more with each miss
  ([`internal/gateway/smtp/tarpit.go`](../../../internal/gateway/smtp/tarpit.go)).
- **Connection and sign-in limits** per address, with temporary bans after
  repeated failed sign-ins, and fail2ban for permanent ones
  ([`internal/gateway/connlimiter/`](../../../internal/gateway/connlimiter/)).
- **Greylisting,** with the triple and its lifetime kept in the database
  ([`internal/pipeline/filters/greylist.go`](../../../internal/pipeline/filters/greylist.go)).
  REST Mail's own outbound queue retries greylisted mail after 7 minutes, so it
  is a good citizen on the other side too.
- **SPF, DKIM, DMARC and ARC** filters (see their chapters), and rate limits per
  sender and domain.
- **Content and virus scanning** through adapters to Rspamd and ClamAV, when
  they are deployed alongside
  ([`internal/pipeline/filters/rspamd.go`](../../../internal/pipeline/filters/rspamd.go),
  [`clamav.go`](../../../internal/pipeline/filters/clamav.go)).
- **Quarantine,** with release and delete from the admin API, and a periodic
  digest to each user ([`internal/digest/`](../../../internal/digest/)).
- **Blocklists, for your own address:** REST Mail can check its own sending
  addresses against DNSBLs on a schedule and report a listing
  ([`docs/dnsbl-monitor.md`](../../dnsbl-monitor.md)). Checking the addresses of
  servers connecting to it, an early-talker check, and reverse-DNS scoring are
  designed but parked (#292).

## Sources

- Kaspersky: [Spam and phishing in 2024](https://securelist.com/spam-phishing-report-2024/115536/).
- [RFC 5782](https://www.rfc-editor.org/rfc/rfc5782): DNS Blacklists and Whitelists.
- [RFC 6647](https://www.rfc-editor.org/rfc/rfc6647): Email Greylisting: An Applicability Statement for SMTP.
- Evan Harris: [The Next Step in the Spam Control War: Greylisting (2003)](http://projects.puremagic.com/greylisting/whitepaper.html).
- Postfix: [POSTSCREEN_README](https://www.postfix.org/POSTSCREEN_README.html).
- [Apache SpamAssassin](https://spamassassin.apache.org/), [Rspamd](https://rspamd.com/), [ClamAV](https://www.clamav.net/).
- REST Mail: [`internal/pipeline/filters/`](../../../internal/pipeline/filters/),
  [`internal/gateway/smtp/`](../../../internal/gateway/smtp/).
