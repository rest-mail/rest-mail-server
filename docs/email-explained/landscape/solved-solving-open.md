---
title: Solved, solving, still open
part: The landscape, and where REST Mail fits
summary: Every problem in this guide in one table, how far email as a whole has got with it, and where REST Mail stands, with the issue for each gap.
---

# Solved, solving, still open

This is the guide in one place: each problem it describes, whether the email
system as a whole has a standard answer, and where REST Mail stands. "Solved"
here means a standard exists and is widely deployed, not that every server uses
it. Each row links to the chapter that explains it.

## Delivery

| Problem | Email as a whole | REST Mail |
|---|---|---|
| [Open relays](../how-it-goes-wrong/open-relay-and-spam-machine.md) | Solved by default in modern MTAs; brought back by configuration mistakes | No trusted networks; relaying only after sign-in |
| [Stolen logins sending spam](../how-it-goes-wrong/open-relay-and-spam-machine.md#4-a-stolen-password-and-no-limits) | Still open: limits per account are not a default in the classic stack | Per-account limits on by default; complaints not yet processed ([#733](https://github.com/rest-mail/rest-mail-server/issues/733)) |
| [Finding the MX](../how-email-works/dns-for-email.md) | Solved | Implicit and null MX handled; a domain that does not exist is retried instead of failing ([#713](https://github.com/rest-mail/rest-mail-server/issues/713)) |
| [Retrying for long enough](../how-email-works/bounces-queues-retries.md) | Solved (RFC 5321: 4–5 days) | Gives up after 3 days ([#711](https://github.com/rest-mail/rest-mail-server/issues/711)); one timeout for the whole attempt ([#714](https://github.com/rest-mail/rest-mail-server/issues/714)) |
| [Backscatter](../how-email-works/bounces-queues-retries.md#refuse-dont-bounce) | Solved where servers refuse during the conversation | Refuses during the conversation |
| [Silently lost mail](../how-it-goes-wrong/the-black-hole.md) | Still open: depends on every server's behaviour | Traces every outcome; failed forwards bounced to the original sender ([#715](https://github.com/rest-mail/rest-mail-server/issues/715)); vacation replies to `<>` ([#716](https://github.com/rest-mail/rest-mail-server/issues/716)) |
| [SMTP smuggling](../how-email-works/smtp-conversation.md#what-goes-wrong) | Being solved: Postfix, for example, shipped fixes in 2023–2024 ([Postfix](https://www.postfix.org/smtp-smuggling.html)) | Only CRLF ends a line |

## Encryption

| Problem | Email as a whole | REST Mail |
|---|---|---|
| [Mail readable on the wire](../security/encryption-in-transit.md) | Mostly solved: 98–99% of Gmail's outbound mail is encrypted | STARTTLS on 25; implicit TLS everywhere else |
| [Delivery to servers with bad certificates](../security/encryption-in-transit.md#certificates-nobody-checks) | Solved by opportunistic TLS | Opportunistic: encrypts, and records the certificate as unverified (#723) |
| [STARTTLS downgrade](../security/encryption-in-transit.md#the-downgrade) | Being solved: MTA-STS and DANE, adopted slowly | MTA-STS enforced when sending; the `_mta-sts` record printed with an `id` that follows the policy, the `mta-sts.` host and its certificate not yet ([#727](https://github.com/rest-mail/rest-mail-server/issues/727)); no DANE ([#725](https://github.com/rest-mail/rest-mail-server/issues/725)) |
| [Hearing about TLS failures](../security/mta-sts-and-dane.md#tls-rpt-being-told-what-went-wrong) | Solved by TLS-RPT, where published | Receives reports over HTTPS; does not send them ([#726](https://github.com/rest-mail/rest-mail-server/issues/726)) |
| [Encryption required per message](../security/encryption-in-transit.md#asking-for-encryption-per-message) | Standard exists (REQUIRETLS), little deployed | Not supported ([#724](https://github.com/rest-mail/rest-mail-server/issues/724)) |
| [End-to-end encryption](modernising-email.md#end-to-end-encryption) | Still open: S/MIME and OpenPGP exist; key discovery remains hard | Not addressed |

## Authentication

| Problem | Email as a whole | REST Mail |
|---|---|---|
| [Who may send for a domain](../security/spf.md) | Solved by SPF | Checked on HELO and MAIL FROM; `v=spf1 mx -all` recommended |
| [Proving a message's origin](../security/dkim.md) | Solved by DKIM | Every message signed; keys not yet rotated automatically ([#728](https://github.com/rest-mail/rest-mail-server/issues/728)) |
| [Forged From: lines](../security/dmarc.md) | Solved by DMARC, now an IETF standard (RFC 9989) | Applied, to RFC 9989; reports sent to RFC 9990, not yet read ([#730](https://github.com/rest-mail/rest-mail-server/issues/730)); a new domain starts at `p=none` |
| [Forwarding and mailing lists](../security/arc.md) | Being solved: ARC, experimental, used by large providers | Verifies ARC, and seals the mail it forwards ([#731](https://github.com/rest-mail/rest-mail-server/issues/731)) |
| [Passwords in mail apps](../security/who-may-send.md) | Being solved: app passwords and OAuth | App passwords; no OAuth |

## Reading and setup

| Problem | Email as a whole | REST Mail |
|---|---|---|
| [Mail on every device](../how-email-works/reading-mail.md) | Solved by IMAP; JMAP for mobile and web | IMAP4rev1 and its key extensions; IMAP4rev2 ([#718](https://github.com/rest-mail/rest-mail-server/issues/718)), SPECIAL-USE ([#717](https://github.com/rest-mail/rest-mail-server/issues/717)), JMAP ([#720](https://github.com/rest-mail/rest-mail-server/issues/720)) not yet |
| [Server-side rules](../how-email-works/reading-mail.md#sieve-rules-that-run-on-the-server) | Solved by Sieve and ManageSieve | Sieve; no ManageSieve ([#719](https://github.com/rest-mail/rest-mail-server/issues/719)) |
| [Mail app setup](../how-email-works/finding-the-server.md) | Being solved: autoconfig is an IETF draft; SRV records | Autoconfig and Autodiscover, with two bugs ([#721](https://github.com/rest-mail/rest-mail-server/issues/721), [#722](https://github.com/rest-mail/rest-mail-server/issues/722)); SRV records ([#712](https://github.com/rest-mail/rest-mail-server/issues/712)) |

## Operating a server

| Problem | Email as a whole | REST Mail |
|---|---|---|
| [Too many separate layers](../how-it-goes-wrong/why-security-is-hard.md) | Still open: each layer is configured separately | One program; records generated and checked |
| [New servers distrusted](../how-it-goes-wrong/deliverability.md) | Still open: reputation has to be earned | Authentication from day one; blocklist monitor for its own addresses |
| [Bulk mail rules](../security/bulk-mail.md) | Solved, and enforced since 2024–2025 | One-click unsubscribe, signed; bulk and transactional kept apart |
| [Moving between servers](migrating.md) | Possible with care; no standard | No import yet ([#734](https://github.com/rest-mail/rest-mail-server/issues/734)) |

## Keeping this table honest

The REST Mail column is only true at the commit this guide was built from.
Each linked issue says what "done" means; when one closes, the row changes.
If you find a problem this guide does not cover, or a claim it gets wrong,
please [open an issue](https://github.com/rest-mail/rest-mail-server/issues).
