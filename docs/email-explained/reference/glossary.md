---
title: Glossary
part: Reference
summary: Every term this guide uses, in a sentence, with the chapter that explains it.
---

# Glossary

**ARC (Authenticated Received Chain).** Header fields with which a forwarder or
mailing list records, and signs, the authentication results it saw.
[ARC](../security/arc.md).

**Alignment.** DMARC's requirement that the domain that passed SPF or DKIM match
the `From:` domain. [DMARC](../security/dmarc.md#alignment).

**Autoconfig.** A file a mail provider publishes so mail apps can find its
servers. [Finding the server](../how-email-works/finding-the-server.md).

**Autodiscover.** Microsoft's equivalent of autoconfig, used by Outlook.
[Finding the server](../how-email-works/finding-the-server.md#autodiscover-microsofts-way).

**Backscatter.** Bounces sent to the forged sender of spam, an innocent party.
[Bounces](../how-email-works/bounces-queues-retries.md#refuse-dont-bounce).

**Bcc.** A recipient who appears only in the envelope, never in the header.
[The message itself](../how-email-works/the-message.md#the-envelope-and-the-header-are-different-things).

**BIMI.** A way for a domain with DMARC at enforcement to publish a logo for
mail apps to show. [Efforts to modernise email](../landscape/modernising-email.md).

**Blocklist (DNSBL).** A DNS-published list of addresses known to send spam.
[Spam defences](../security/spam-defences.md#on-the-connection-who-is-this).

**Bounce (DSN).** A message telling the sender that delivery failed.
[Bounces](../how-email-works/bounces-queues-retries.md#the-bounce).

**Canonicalization.** How a message is normalised before DKIM hashes it:
`simple` or `relaxed`. [DKIM](../security/dkim.md#normalising-simple-and-relaxed).

**DANE.** Publishing a server's TLS key in DNSSEC-signed DNS, so senders can
require and verify TLS. [MTA-STS and DANE](../security/mta-sts-and-dane.md#dane-keys-in-signed-dns).

**DKIM.** A signature over a message, by a domain, checked with a key in the DNS.
[DKIM](../security/dkim.md).

**DMARC.** A domain's policy for mail that fails SPF and DKIM alignment, and its
reports. [DMARC](../security/dmarc.md).

**DNSSEC.** Signatures on DNS answers, so they can be checked.
[MTA-STS and DANE](../security/mta-sts-and-dane.md).

**Envelope.** The sender (`MAIL FROM`) and recipients (`RCPT TO`) given in the
SMTP conversation, separate from the message.
[The message itself](../how-email-works/the-message.md).

**Greylisting.** Temporarily refusing a first attempt from an unknown sender,
and accepting the retry. [Spam defences](../security/spam-defences.md#in-the-envelope-who-says-they-are-sending-to-whom).

**HELO / EHLO.** The command that opens an SMTP conversation and names the
client. [SMTP](../how-email-works/smtp-conversation.md#the-commands).

**IMAP.** The protocol mail apps use to read mail kept on the server.
[Reading mail](../how-email-works/reading-mail.md#imap-keep-it-on-the-server).

**Implicit MX.** Delivering to a domain's own address when it has no MX records.
[DNS for email](../how-email-works/dns-for-email.md#when-there-is-no-mx).

**Implicit TLS.** TLS from the first byte of the connection, on ports 465, 993
and 995. [Encryption in transit](../security/encryption-in-transit.md#two-ways-to-encrypt).

**JMAP.** A JSON-over-HTTPS protocol for mail apps, IMAP's modern successor.
[Reading mail](../how-email-works/reading-mail.md#jmap-mail-over-http).

**LMTP.** A local variant of SMTP used to hand mail to the mailbox server.
[The journey of a message](../how-email-works/journey-of-a-message.md#6-delivery-into-bobs-mailbox).

**MIME.** The standard for messages with several parts: text, HTML, attachments.
[The message itself](../how-email-works/the-message.md#mime-one-message-many-parts).

**MTA (mail transfer agent).** A server that moves mail between servers, such as
Postfix or Exim. [The servers compared](../landscape/servers-compared.md).

**MTA-STS.** A policy, published over HTTPS, that a domain requires TLS to
named MX servers. [MTA-STS and DANE](../security/mta-sts-and-dane.md#mta-sts-a-policy-over-https).

**MUA (mail user agent).** A mail app.
[The journey of a message](../how-email-works/journey-of-a-message.md#the-cast).

**MX record.** The DNS record naming the servers that accept a domain's mail.
[DNS for email](../how-email-works/dns-for-email.md#mx-where-mail-for-a-domain-goes).

**Null MX.** `MX 0 .`: a domain that accepts no mail.
[DNS for email](../how-email-works/dns-for-email.md#null-mx-this-domain-takes-no-mail).

**One-click unsubscribe.** `List-Unsubscribe-Post`: unsubscribing with one HTTPS
POST. [Bulk mail](../security/bulk-mail.md#one-click).

**Open relay.** A server that delivers mail for anyone, to anyone.
[The open relay](../how-it-goes-wrong/open-relay-and-spam-machine.md).

**Opportunistic TLS.** Encrypting when both sides can, without requiring it.
[Encryption in transit](../security/encryption-in-transit.md#why-port-25-cannot-require-encryption).

**POP3.** A protocol for downloading mail to one device.
[Reading mail](../how-email-works/reading-mail.md#pop3-download-it).

**PTR record (reverse DNS).** A record mapping an IP address back to a name.
[DNS for email](../how-email-works/dns-for-email.md#a-aaaa-and-reverse-dns).

**Queue.** Where a server keeps mail it has not yet delivered.
[Bounces](../how-email-works/bounces-queues-retries.md).

**Relaying.** Passing mail on to another server.
[The open relay](../how-it-goes-wrong/open-relay-and-spam-machine.md#the-rule-every-server-has-to-get-right).

**Return path.** The envelope sender: where bounces go.
[The message itself](../how-email-works/the-message.md).

**RESTMAIL.** REST Mail's delivery between REST Mail servers over HTTPS.
[RESTMAIL](../landscape/restmail-protocol.md).

**Selector.** The name under which a domain publishes one of its DKIM keys.
[DKIM](../security/dkim.md#selectors-and-keys).

**Sieve.** A language for mail rules that run on the server.
[Reading mail](../how-email-works/reading-mail.md#sieve-rules-that-run-on-the-server).

**SMTP.** The protocol that moves mail between servers, and from apps to servers.
[SMTP as a conversation](../how-email-works/smtp-conversation.md).

**SMTP AUTH (SASL).** Signing in over SMTP. [Who may send](../security/who-may-send.md).

**SPF.** A DNS record listing which servers may send for a domain.
[SPF](../security/spf.md).

**SRS.** Rewriting a forwarded message's envelope sender, so SPF passes at the
next hop. [SPF](../security/spf.md#why-forwarding-breaks-spf).

**STARTTLS.** Upgrading an SMTP, IMAP or POP3 connection to TLS.
[Encryption in transit](../security/encryption-in-transit.md).

**Submission.** Sending from a mail app, with sign-in, on port 465 or 587.
[Who may send](../security/who-may-send.md#two-doors).

**TLS-RPT.** Daily reports from senders about TLS failures reaching a domain.
[MTA-STS and DANE](../security/mta-sts-and-dane.md#tls-rpt-being-told-what-went-wrong).

**TTL.** How long a DNS record may be cached.
[DNS for email](../how-email-works/dns-for-email.md#changes-take-time).

**Warm-up.** Raising a new server's sending volume slowly, to build reputation.
[Deliverability](../how-it-goes-wrong/deliverability.md#building-a-reputation).
