---
title: Migrating step by step, without breaking anything
part: The landscape, and where REST Mail fits
summary: How to move a domain's mail from one server to another while mail keeps flowing, in an order where every step can be undone.
diagram: ../diagrams/migration.workflow.json
---

# Migrating step by step, without breaking anything

Moving a domain to a new mail server is nerve-racking because mail does not
stop while you work. Messages arrive at whichever server the sender's DNS cache
points to, signatures are checked against whichever keys are published, and mail
apps keep connecting to the old settings. The way through is to run both servers
side by side, change one thing at a time, and keep every step reversible until
the end.

This chapter is written for any move between servers. It applies to moving onto
REST Mail, from Postfix and Dovecot or a bundle, and equally to moving away.

![A migration in four phases, each reversible](../diagrams/migration.svg)

*The diagram's source is [`migration.workflow.json`](../diagrams/migration.workflow.json).*

## 1. Prepare

- **Write down what exists:** every domain, mailbox and alias, every forward and
  Sieve rule, every service that sends as the domain (the website, the
  newsletter, the invoicing system), the DKIM selectors in use, and the current
  MX, SPF, DMARC and PTR records.
- **Lower the TTLs.** A record's TTL is "the time interval that the resource
  record may be cached before the source of the information should again be
  consulted" ([RFC 1035 §3.2.1](https://www.rfc-editor.org/rfc/rfc1035#section-3.2.1)).
  At least one full old TTL before the cutover, lower the MX and the mail host's
  A and AAAA records to a few minutes, so the switch takes effect quickly, and
  can be undone quickly.
- **Get the new server's address right first:** a clean IP, with a PTR record
  that matches its name, not on any blocklist
  ([Deliverability](../how-it-goes-wrong/deliverability.md#building-a-reputation)).

## 2. Run both

- **Set up the new server** with the domains and mailboxes, but do not point the
  MX at it yet. It can be tested by connecting to it directly.
- **Publish both servers' keys and addresses.** Add the new server's DKIM key
  under a **new selector**, beside the old one, so mail signed by either server
  verifies. Add the new server to the SPF record, alongside the old one. Neither
  change affects the mail already flowing.
- **Copy the mail** while users keep working on the old server. Any IMAP-to-IMAP
  tool can do it; [imapsync](https://imapsync.lamiral.info/) is "an IMAP
  transfer tool" whose "purpose [...] is to migrate IMAP accounts". Run it once
  now, and again at the cutover to pick up what arrived since.
- **Test:** send to and from the new server, check DKIM, SPF and DMARC pass
  at a large provider, and sign in from a mail app.

## 3. Cut over

- **Point the MX at the new server.** With a short TTL, most senders follow
  within minutes. Some will still use the old MX for a while: keep the old server
  running and accepting mail, and either have it forward to the new one, or copy
  its late arrivals across with a final sync.
- **Move the mail apps:** update autoconfig and the SRV records
  ([Finding the server](../how-email-works/finding-the-server.md)), and tell users
  the new settings, or use a server name that you move with the MX.
- **Watch:** DMARC aggregate reports and the new server's logs show any service
  still sending through the old path, or failing authentication.

If something is badly wrong, point the MX back. Nothing has been removed yet.

## 4. Retire the old server

Only when nothing has arrived at the old server for several days:

- **Remove the old server from SPF** and from any `mynetworks` or relay setting
  elsewhere that trusted it.
- **Remove the old DKIM selector,** at least a few days after the old server
  stopped sending, so mail signed with it and still in queues verifies.
- **Raise the TTLs** back to normal.
- **Keep the old server's data** until you are sure nothing is missing, then
  shut it down.

## What goes wrong

- **Forgetting a sender:** the website's contact form still relays through the
  old server, which is now gone.
- **Removing the old DKIM key at the cutover,** so mail in transit fails.
- **A long TTL,** so half the internet delivers to the old server for a day.
- **A final sync that is never run,** so mail that arrived at the old server
  during the cutover is lost: [the black hole](../how-it-goes-wrong/the-black-hole.md)
  in its purest form.
- **Turning the old server off first.** Keep it accepting until the very end.

## How REST Mail handles it

- **Running both:** REST Mail speaks SMTP, IMAP and POP3, so it sits beside any
  other server for as long as the migration needs, and can be tested directly
  before the MX changes.
- **The records to publish** for each domain, including its DKIM key under its
  own selector, come from `chore dns:show`, and `chore dns:check` shows what is
  still missing ([DNS for email](../how-email-works/dns-for-email.md#how-rest-mail-handles-it)).
- **Mail apps** find the new settings by autoconfig and Autodiscover; the
  settings are implicit TLS on 465 and 993, so apps set up for 587 or 143 need
  changing.
- **Not yet:** importing accounts with the password hashes they already have,
  and importing mail from IMAP, Maildir or mbox, are
  [#734](https://github.com/rest-mail/rest-mail-server/issues/734). Until then,
  users get new passwords, and mail is copied in over IMAP with a tool such as
  imapsync.

## Sources

- [RFC 1035 §3.2.1](https://www.rfc-editor.org/rfc/rfc1035#section-3.2.1): resource record TTLs.
- [imapsync](https://imapsync.lamiral.info/).
- M3AAWG: [DKIM Key Rotation Best Common Practices](https://www.m3aawg.org/DKIMKeyRotation), for running two selectors at once.
- REST Mail: [`internal/dns/mail_records.go`](../../../internal/dns/mail_records.go),
  [`internal/api/handlers/autoconfig.go`](../../../internal/api/handlers/autoconfig.go).
