---
title: "Deliverability: reputation, warm-up, and why big providers distrust small servers"
part: How it goes wrong
summary: Why a correctly configured server's mail can still land in spam, how mailbox providers judge a sender, and what a new server can do about it.
diagram: ../diagrams/reputation.workflow.json
---

# Deliverability: reputation, warm-up, and why big providers distrust small servers

You set up a mail server, publish every record in this guide, and pass every
test. Then your first message to a friend at Gmail lands in their spam folder,
and one to Outlook.com is refused outright. Nothing is misconfigured.

This is the part of running a mail server that frustrates people most, and it
is not a protocol problem. It is a trust problem. The large mailbox providers
receive billions of messages a day, about half of them spam
([Spam defences](../security/spam-defences.md)), and they decide what to accept
largely on **reputation**: what they have seen from your address and your domain
before. A new server has none.

![How a mailbox provider decides: inbox, spam folder or refused](../diagrams/reputation.svg)

*The diagram's source is [`reputation.workflow.json`](../diagrams/reputation.workflow.json).*

## What a provider looks at

Every large provider has its own system, and none publishes all of it. What they
do publish, in their sender guidelines, points at the same few things:

- **The sending address (IP).** Is it on a blocklist? Is it a home or cloud
  address that should not send mail directly? Does it have reverse DNS that
  matches? What has it sent before, and did people want it?
- **The domain.** The `From:` domain and the DKIM signing domain carry their
  own reputation, which follows the domain from server to server. That is why
  authentication matters: it lets a provider attach reputation to a name you
  control rather than only to an address.
- **Authentication:** SPF, DKIM and DMARC, aligned
  ([DMARC](../security/dmarc.md#alignment)).
- **How recipients react:** spam reports, mail deleted unread, mail moved out
  of the spam folder. Gmail asks senders to keep spam reports below 0.3%
  ([Google, Email sender guidelines](https://support.google.com/a/answer/81126)).
- **The content and its formatting,** last.

## Why small servers start behind

- **No history.** A new address has sent nothing, so a provider has nothing
  good to weigh against the risk of a sender it does not know.
- **Addresses that should not send mail directly.** Spamhaus's Policy Blocklist
  lists "end-user IP address ranges from which email should never be sent
  directly to the final destination", often "ranges assigned by ISPs to
  broadband or dial-up customers"
  ([Spamhaus, PBL](https://www.spamhaus.org/blocklists/policy-blocklist/)). A
  server at home is almost certainly on it.
- **Cloud neighbours.** An address from a cloud provider's pool may have been
  used by a spammer last month. Many cloud providers block outgoing port 25
  altogether by default: "By default, Amazon EC2 allows outbound traffic over
  port 25 only to private IPv4 addresses"
  ([AWS](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-resource-limits.html)),
  and on Google Cloud "connections to destination TCP Port 25 are blocked when
  the destination is external to your VPC network"
  ([Google Cloud](https://cloud.google.com/compute/docs/tutorials/sending-mail)).
- **Low volume.** A server that sends a few messages a day never builds the
  statistics a provider uses to trust it, and one spam report weighs heavily
  among few messages.

None of this is unfair to small servers on purpose. It is the cost of an open
system where anyone can send, and most of those who try are spammers.

## Building a reputation

- **A clean, dedicated address** with matching forward and reverse DNS, from a
  provider that allows mail, and not on any blocklist: check it before you
  start.
- **Every authentication layer from day one:** SPF, DKIM, DMARC at `p=none` with
  reports, TLS.
- **Warm up.** Google's advice is to "Increase sending volume slowly to avoid
  delivery problems", and that "The more email that you send, the more slowly
  you should increase sending volume". If messages start being deferred,
  "reduce the sending volume until the SMTP error rate decreases"
  ([Google, Email sender guidelines](https://support.google.com/a/answer/81126)).
- **Keep kinds of mail apart.** The same guidelines suggest "a different IP
  address for each message type. For example, use one IP address for sending
  account notifications and a different IP address for sending promotional
  messages", so a newsletter's complaints do not hold up password resets.
- **Stop sending to addresses that bounce,** and to people who complain:
  "Automatically unsubscribe recipients who have multiple bounced messages",
  and "Don't purchase email addresses from other companies" (the same page).
- **Use the providers' own tools:** [Google Postmaster Tools](https://postmaster.google.com/)
  shows Gmail's view of your domain's spam rate and reputation, and Microsoft's
  [Smart Network Data Services](https://sendersupport.olc.protection.outlook.com/snds/)
  shows Outlook.com's view of your addresses. Outlook.com's own words: "Deliverability
  to Outlook.com is based on your reputation."
- **Join feedback loops,** where providers send you a report each time a
  recipient marks your mail as spam ([RFC 5965](https://www.rfc-editor.org/rfc/rfc5965)
  defines the format).

## When you are blocked

The refusal message usually says why, and where to go: a blocklist's name and
lookup page, or a provider's sender support form. Fix the cause first (a
compromised account, an open relay, a missing record), then ask for delisting.
Delisting before the cause is fixed only gets you listed again.

## How REST Mail handles it

- **Authentication from the start:** every message DKIM-signed; SPF, DMARC and
  PTR records printed for each domain; TLS whenever the receiver offers it.
- **Your own address, watched:** REST Mail can check its sending addresses
  against DNS blocklists on a schedule and report a listing in its logs and
  metrics, so you hear before your users do ([`docs/dnsbl-monitor.md`](../../dnsbl-monitor.md)).
- **Limits per account:** a stolen login cannot send thousands of messages and
  burn the server's reputation in an afternoon
  ([Who may send](../security/who-may-send.md#how-rest-mail-handles-it)).
- **Hard bounces remembered:** an address that bounces with "no such mailbox" is
  not sent to again for 90 days.
- **Bulk and transactional kept apart:** a domain can be marked `bulk` or
  `transactional`, and the send API holds it to that ([Bulk mail](../security/bulk-mail.md#how-rest-mail-handles-it)).
- **Not yet:** feedback-loop complaints are not processed
  ([#733](https://github.com/rest-mail/rest-mail-server/issues/733)), and there
  is no built-in warm-up schedule.

## Sources

- Google: [Email sender guidelines](https://support.google.com/a/answer/81126),
  [Postmaster Tools](https://postmaster.google.com/).
- Microsoft: [Smart Network Data Services](https://sendersupport.olc.protection.outlook.com/snds/).
- Spamhaus: [Policy Blocklist (PBL)](https://www.spamhaus.org/blocklists/policy-blocklist/).
- AWS: [Restriction on email sent using port 25](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-resource-limits.html).
- Google Cloud: [Sending email from an instance](https://cloud.google.com/compute/docs/tutorials/sending-mail).
- [RFC 5965](https://www.rfc-editor.org/rfc/rfc5965): An Extensible Format for Email Feedback Reports.
- REST Mail: [`internal/dnsbl/`](../../../internal/dnsbl/),
  [`internal/gateway/queue/suppression.go`](../../../internal/gateway/queue/suppression.go).
