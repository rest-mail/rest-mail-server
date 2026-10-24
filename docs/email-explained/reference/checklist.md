---
title: "Check your own server: a checklist"
part: Reference
summary: The checks to run on any mail server, in order, with the commands to run them and the chapter that explains each.
sources: linked
---

# Check your own server: a checklist

Run these from a machine **outside** your network, unless a step says
otherwise. Replace `example.org` with your domain, `mx.example.org` with your
MX host, and `203.0.113.25` with its address. Each step links to the chapter
that explains what a good answer looks like.

## DNS

- [ ] **One MX, or several, each a host name with an A or AAAA record, and no
  CNAME.** `dig +short MX example.org`, then `dig +short A mx.example.org`.
  ([DNS for email](../how-email-works/dns-for-email.md#mx-where-mail-for-a-domain-goes))
- [ ] **Reverse DNS matches.** `dig +short -x 203.0.113.25` names the MX host,
  and that name's A record is the same address.
  ([DNS for email](../how-email-works/dns-for-email.md#a-aaaa-and-reverse-dns))
- [ ] **Exactly one SPF record, within 10 lookups, ending in `-all` or `~all`.**
  `dig +short TXT example.org | grep spf1`. ([SPF](../security/spf.md#the-10-lookup-limit))
- [ ] **A DKIM key for every selector you sign with.**
  `dig +short TXT <selector>._domainkey.example.org`. ([DKIM](../security/dkim.md))
- [ ] **A DMARC record, with `rua` pointing somewhere you read.**
  `dig +short TXT _dmarc.example.org`. Start at `p=none`.
  ([DMARC](../security/dmarc.md#the-three-policies))
- [ ] **Domains that send no mail say so:** `v=spf1 -all`, and a null MX if they
  receive none. ([DNS for email](../how-email-works/dns-for-email.md#null-mx-this-domain-takes-no-mail))

## Not an open relay

- [ ] **Relaying for a stranger is refused.**
  `swaks --server mx.example.org --from test@example.net --to someone@gmail.com`
  is refused at `RCPT TO` with `454` or `554`, not accepted with `250`.
  ([The open relay](../how-it-goes-wrong/open-relay-and-spam-machine.md#how-to-check-your-own-server))
- [ ] **`AUTH` is not offered on port 25.** The `EHLO` reply on port 25 has no
  `AUTH` line. ([Who may send](../security/who-may-send.md#what-goes-wrong))
- [ ] **Submission requires sign-in, over TLS.** On 465 (or 587 after
  `STARTTLS`), `MAIL FROM` before `AUTH` is refused with `530`.
  ([Who may send](../security/who-may-send.md))

## Encryption

- [ ] **Port 25 offers STARTTLS, with a valid certificate for the MX name.**
  `openssl s_client -starttls smtp -connect mx.example.org:25 -servername mx.example.org -brief`.
  ([Encryption in transit](../security/encryption-in-transit.md#check-your-own-server))
- [ ] **Mail apps connect with implicit TLS,** on 465, 993 and 995, TLS 1.2 or later.
  `openssl s_client -connect mx.example.org:993 -brief`.
- [ ] **No password is accepted in the clear** on 587, 143 or 110 before
  `STARTTLS`.
- [ ] **MTA-STS, if published, matches reality.**
  `curl -s https://mta-sts.example.org/.well-known/mta-sts.txt` lists every MX,
  and the `_mta-sts` TXT record's `id` changed when the policy last did.
  ([MTA-STS and DANE](../security/mta-sts-and-dane.md#check-your-own-domain))

## Sending

- [ ] **A message to an account at a large provider passes everything.** Open
  "Show original" and read `Authentication-Results`: `spf=pass`, `dkim=pass`,
  `dmarc=pass`. ([DMARC](../security/dmarc.md#check-your-own-domain))
- [ ] **The sending address is on no blocklist,** and not on a list of home
  addresses. ([Deliverability](../how-it-goes-wrong/deliverability.md#why-small-servers-start-behind))
- [ ] **Signed-in users can only send as themselves,** and have a sending limit.
  ([Who may send](../security/who-may-send.md#after-sign-in-what-a-user-may-do))
- [ ] **Bulk mail has one-click unsubscribe,** DKIM-signed.
  ([Bulk mail](../security/bulk-mail.md#one-click))

## Receiving

- [ ] **Mail to an address that does not exist is refused during the
  conversation** with `550 5.1.1`, not accepted and bounced later.
  ([Bounces](../how-email-works/bounces-queues-retries.md#refuse-dont-bounce))
- [ ] **Mail that is accepted is delivered or bounced, never dropped.** Know
  where each filter's discards and quarantines go.
  ([The black hole](../how-it-goes-wrong/the-black-hole.md))
- [ ] **Bounces and auto-replies are sent from `<>`,** and never sent in reply to
  `<>`. ([Bounces](../how-email-works/bounces-queues-retries.md#automatic-replies))

## Keeping it working

- [ ] **The queue is not growing.** `postqueue -p` or your server's equivalent;
  read the errors. ([Bounces](../how-email-works/bounces-queues-retries.md#look-at-your-own-queue))
- [ ] **Certificates renew,** for the MX, submission, IMAP, `mta-sts.` and
  `autoconfig.`.
- [ ] **DKIM keys rotate,** at least every six months.
  ([DKIM](../security/dkim.md#selectors-and-keys))
- [ ] **Someone reads the DMARC and TLS-RPT reports,** or software does.
  ([Why the security layers are so hard](../how-it-goes-wrong/why-security-is-hard.md#mistakes-are-quiet))

## With REST Mail

`chore dns:show` prints every record a domain needs, and `chore dns:check`
compares them with what is published. The relay, sign-in, encryption, sender
and refusal checks above are REST Mail's defaults, not settings; the list in
[Solved, solving, still open](../landscape/solved-solving-open.md) shows the
gaps that remain.
