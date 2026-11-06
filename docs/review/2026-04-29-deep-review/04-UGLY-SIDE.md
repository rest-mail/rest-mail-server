# 04 — The Ugly Side of Mail Servers

This document is about what happens after the dev-substrate works and you're sending real mail to real people. The mail-server world has 30 years of accumulated weirdness; most of it is invisible until your bounce rate spikes or your mail starts going to spam. Below is what to expect, in roughly the order you'll encounter it.

> This describes how mail behaves in the wild, not what REST Mail does today. The open tasks it listed moved to the issue tracker; [#1563](https://github.com/rest-mail/rest-mail-server/issues/1563) leads to where each one went.

## 1. Reputation is a moving target you don't control

You don't get a reputation score from the receivers. They don't tell you. You guess at it from indirect signals.

The signals you *can* observe:

- **DMARC aggregate reports (RUA)**: receivers send these to the address you publish in your DMARC record. Format: XML or JSON-ish. Tells you "we received N messages claiming to be from your domain, M passed DKIM, K passed SPF."
- **TLS-RPT reports**: receivers send these when they couldn't establish TLS to you. Format: JSON gzipped.
- **Bounce rate**: trivially measurable from your queue, but only useful as a signal if you know the *baseline*.
- **Google Postmaster Tools API**: gives you spam rate, IP reputation, domain reputation, authentication pass rate, encryption rate, and feedback loop counts for messages sent to Gmail. **Free and underused.**
- **Microsoft SNDS (Smart Network Data Services)**: similar for Outlook/Hotmail. Per-IP, gives you complaint rate, trap hits, RCPT rejections.
- **Yahoo / Verizon Media** bounce-back signals: harder to access, but the receiver's bounce text contains hints.
- **Spamhaus and similar blocklist queries**: you can DNSBL-query yourself periodically and alert if you're listed.

## 2. The first time you get blocklisted will be bewildering

It happens to every mail provider. A customer sends a campaign you didn't catch in time, or your IP gets reassigned to you with someone else's bad history (cloud providers especially), and suddenly Spamhaus has you on PBL or SBL.

The recovery process:

1. **Detect**: a self-querying job hits the major blocklists every 15 minutes.
2. **Triage**: which blocklist, which IP, which list-specific reason.
3. **Act**: each blocklist has its own delisting form. Spamhaus has SBL/PBL/CSS/etc., each with different criteria. Some are automatic (PBL), some require a human at the receiving end (SBL).
4. **Prevent**: figure out *which customer or which message pattern* triggered it. This is where bounce categorization pays off.

## 3. Senders forge your domain (this is normal, not a bug)

Spammers see your domain has a public MX. They send mail "from" your domain to other people. Their mail bounces, but the bounces come back to *you* (because the From: claims to be you). This is **backscatter**.

Mitigations:

- **DMARC `p=reject` + DKIM signing on every legitimate outbound**: receivers will drop forged messages instead of bouncing them.
- **SPF `-all`**: same effect, harder.
- **Backscatter filter on inbound**: if a message looks like a DSN for a message we never sent, drop it.

Two settings decide how much of this you get:

1. DMARC `p=none` is monitor mode. You have to move to `p=reject` consciously, after you've verified your aggregate reports look clean.
2. SPF `-all` (hard fail) is the modern recommended default. Some MTAs treat `~all` (soft fail) as "this is fine."

## 4. List-Unsubscribe is mandatory now

Since 2024, Gmail and Yahoo require senders of >5000 messages/day to have a working List-Unsubscribe header (RFC 8058 one-click unsubscribe). If you don't, your messages go to spam at high rates.

A transactional sender doesn't need the header. A bulk sender needs it on every message.

## 5. The dance of "did the message arrive?" is fundamentally lossy

A user sends a message. The receiving server accepts it (250 OK). The message ends up in the recipient's spam folder, or silent-discarded by an aggressive filter, or quarantined for human review at a corporate gateway. Your customer reports "Bob never got my email."

You can prove the SMTP transaction happened:

- Your queue worker has the 250 OK response
- The remote MTA's banner is logged
- Optionally, the timing

You cannot prove the message was *read* or *delivered to the inbox*. (Read receipts are a fiction; the user sets that flag manually.)

What a mail product can do:

- Show the SMTP transaction details in the UI: "Delivered to mx.gmail.com at 14:32:12 (250 OK)"
- For domains the user controls (their own customers), the receiving side's RESTMAIL sees the message arrive — cross-system visibility
- For domains we don't control (Gmail recipients), surface "this is a Gmail address; we delivered to their MX, but spam-folder status is opaque"

## 6. The "sending IP" problem

Cloud providers (AWS, GCP, DO, Hetzner) have IP ranges that are rate-limited or outright blocked by major receivers because so many spammers used them. If you spin up a mail server on AWS and send to Gmail, your delivery rate will be 10–60% even with perfect DKIM/SPF/DMARC.

Solutions:

1. **Use a paid SMTP relay**: Mailgun, SendGrid, Postmark, Amazon SES. They have warm IPs, dedicated IPs you can pay for, suppression lists, and reputation management. You pay per-message but you don't fight the IP reputation battle.
2. **Use a "warm" IP**: rent IP space from a provider with a clean history.
3. **Self-host on a residential / small-ISP IP**: theoretically clean, in practice often blocked because residential IPs are presumed to be infected.

That's why outbound transport needs to be a per-domain choice: one domain delivers directly to the remote MX, and another goes through a relay.

## 7. Submission port quirks (587 vs 465 vs 25)

Three SMTP ports, three different histories:

- **25** — server-to-server. Should not accept submissions from end users (you'd be a relay, which is bad).
- **587** — STARTTLS submission. The "modern" submission port. Authentication required.
- **465** — implicit TLS submission. Once deprecated, then un-deprecated by RFC 8314 (2018). Same as 587 but TLS from the get-go.

Common bugs:

- Server accepts mail on port 25 from clients with auth (rather than only relay-from-other-MTAs). Spammers will exploit this.
- Server requires STARTTLS on 25. **Don't.** Some sender MTAs are old and will fall back to a plain connection on rejection. Better to accept plain on 25, log it, and let your downstream reputation/scoring layer decide.
- Submission port doesn't enforce auth before MAIL FROM. (Some clients try MAIL FROM as a probe.)

## 8. The `*.test` and `mail3.test` problem

You're using `.test` as the dev-substrate TLD. That's right per RFC 6761 — `.test` is reserved for testing, will never be delegated, won't ever conflict.

**But:** real customers use real domains. A server has to handle `acme.example` (still test-safe) and `acme.com` (a real domain, real receivers, real DNS).

A test install uses `.test` and the testbed's dnsmasq. A real install uses public DNS and Let's Encrypt. The cert and DNS setup differs between them, and the application code stays the same.

## 9. Ratware (the "spam from yourself" attack)

Spammers spoof `you@yourdomain → you@yourdomain` in From and To. Receivers see it as "internal mail" and apply lenient filtering. This is one of the oldest tricks.

Mitigations baked into the spec:

- **DMARC**: rejects mail claiming to be from your domain that didn't come from you.
- **Internal IP whitelisting**: don't trust SMTP connections from `you@yourdomain` claims unless the source IP is an authenticated submission.

What a gateway should do:

1. On port 25 (incoming relay), if `MAIL FROM:<user@yourdomain>` and the connection isn't authenticated, treat it the same as any external sender (DMARC-check it).
2. On port 587 (submission), require auth before accepting any MAIL FROM.

## 10. Mail loops are a quiet killer

A mail loop: A's autoresponder fires when B sends; B's autoresponder fires on A's autoresponder; etc. RFC 3834 specifies how to break the loop (the `Auto-Submitted: auto-replied` header), but cooperation is voluntary.

Defenses to have:

- **Vacation autoresponder rate-limits per sender**: don't reply to the same sender more than once per N days.
- **Per-message-id dedupe**: if the same message-id arrives twice (loop), don't process it again.
- **Loop detection**: count `Received:` headers; if there are more than 50, drop the message with a 5xx.
- **Header check**: if `Auto-Submitted` or `Precedence: bulk` or `List-Id` are set, vacation does not reply.

## 11. The `Resent-*` headers no one reads

When a user forwards a message, the right way is to add `Resent-From`, `Resent-To`, `Resent-Date` headers and *keep the original headers*. Most webmail UIs just create a new message. The result: the recipient sees an arbitrary new message, not "Bob forwarded Alice's message."

This isn't a bug, it's a UX choice that's now industry-wide. But if you want to differentiate, supporting the resent-* path correctly is one tiny way to.

## 12. Greylisting (mostly dead, but)

Worth knowing:

- Greylisting was hot in 2005. It's mostly noise now (real spammers retry; legit bulk senders get delayed and complain).
- Modern equivalent: **rate-limit by sender reputation** — if SPF passes and DKIM passes, accept fast; if they fail, defer for 30 minutes.
- Having it on by default hurts new-domain deliverability.

## 13. The ARC mess

Authenticated Received Chain (ARC) is supposed to fix the "mailing list breaks DKIM" problem. In practice:

- ARC verification requires you to trust the previous hop's ARC signing.
- "Trusted ARC signers" lists are not standardized. Microsoft has one. Google has one. They differ.
- Many small mail forwarders don't ARC-sign at all. So you still need fallback DKIM-relaxed handling.

## 14. Catchall recipient handling

A "catch-all" address (e.g. `*@yourdomain` → `inbox`) is a feature your customers will demand. Risks:

- Spammers will dictionary-attack the domain looking for valid addresses; with a catch-all, every attempt is "valid".
- Receivers' rate limits (yours and remote) will hate you.

Mitigations:

- The catch-all is a *fallback*, not a rule. RCPT TO for non-existent users still 5xx-rejects unless catch-all is explicitly enabled.
- When catch-all is enabled, the dictionary attack rate is throttled per-sender-IP.
- The sender of dictionary attacks gets fail2-banned.

## 15. The DSN / NDR you generate is your reputation

When *you* bounce a message back to a sender, the format and content of that bounce is read by other systems' fraud filters. Common mistakes:

- Sending a 5xx bounce that contains the original message body — leaks the original message back to the *forged* sender (who isn't the real sender).
- Sending a bounce with `From: postmaster@yourdomain.com` to a forged address — backscatter spam from your IP, killing your reputation.
- Bounce body contains the recipient address you're rejecting — useful for spammers to confirm a valid address.

Best practices for bounces *you* send:

- Don't include the message body, just headers.
- Don't bounce to addresses with no DKIM/SPF — silent-drop instead. RFC 5321 lets you.
- Rate-limit your bounce sending per-sender-IP. If someone tried to send 1000 messages with bad RCPT TOs, don't send 1000 bounces back.

## 16. Things that are usually wrong about mail-server tutorials

If you're going to publish docs for the product, here's what mail tutorials get wrong:

- "Just open ports 25/587/465/143/993/110/995 in the firewall." → correct port list, missing the important caveat: **outbound port 25 is blocked by most cloud providers' default policies and most residential ISPs**. New customers will discover this at delivery time.
- "Use Let's Encrypt for everything" → great for hostnames you control, but the TLS-required STARTTLS interactions on port 25 don't always trust LE certs at the receiving end. (Nine out of ten do; one in ten does old root validation.)
- "Set up rDNS / PTR." → necessary but receivers also check that PTR matches A. PTR `mail.acme.example` → A `acme.example` ≠ A `mail.acme.example`. The whole chain has to align.
- "Use DKIM with 1024-bit keys." → use 2048. Some receivers reject 1024 in 2026.

## 17. The "unknown sender" cold-start problem

A new domain with a clean DKIM/SPF/DMARC setup will *still* have low deliverability for the first 2–4 weeks. Receivers don't know you. Mitigations:

- **Warm-up**: ramp send volume slowly. 50 messages day 1, 100 day 2, 200 day 3, etc.
- **Monitor delivery** during warm-up. If a particular receiver (Gmail) starts spam-foldering, throttle harder.
- **Use a paid relay** for the first month, then transition.

## 18. The IPv6 question

You can run mail-server on IPv4 + IPv6, or just IPv4. Reasons to be careful with IPv6:

- Many sender MTAs prefer IPv6 if you advertise an AAAA on your MX. Some receivers (older corporate filters) blocklist all IPv6 mail. Your delivery rate over IPv6 may be lower than IPv4.
- Spamhaus has IPv6 list infrastructure that's less mature than IPv4. False positives are more common.
- Reverse DNS for IPv6 is more often broken/missing than for IPv4.

The pragmatic default for now: serve IPv4 on the MX, support IPv6 as a fallback, monitor delivery rates per-protocol.

## Summary

Mail is the original distributed system, with all the fragility and accumulated weirdness that implies. The hard part is operational visibility: knowing when things are degrading, knowing why, and knowing what to do about it.
