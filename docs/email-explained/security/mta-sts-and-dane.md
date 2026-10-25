---
title: MTA-STS, TLS-RPT, DANE and DNSSEC
part: The security layers, one at a time
summary: How a domain tells senders "always use TLS, and only with these servers", the two standards that do it, and the reports that say when it fails.
diagram: ../diagrams/mta-sts.workflow.json
---

# MTA-STS, TLS-RPT, DANE and DNSSEC

[Encryption in transit](encryption-in-transit.md) ended with two gaps in
opportunistic STARTTLS: an attacker can delete the offer to encrypt, and the
sender does not check the certificate, so an attacker who redirects the
connection can answer with their own. Both gaps have the same cause. The
sender has no trustworthy way to know, before it connects, that this domain
always uses TLS, and which servers are really its MX.

Two standards give it that knowledge, in different ways:

- **MTA-STS** publishes a policy over HTTPS, protected by the same certificate
  authorities as the web.
- **DANE** publishes the servers' keys in the DNS, protected by DNSSEC.

A third, **TLS-RPT**, sends the domain a daily report of what went wrong, so it
finds out before its mail does.

![How a sending server applies DANE and MTA-STS](../diagrams/mta-sts.svg)

*The diagram's source is [`mta-sts.workflow.json`](../diagrams/mta-sts.workflow.json).*

## MTA-STS: a policy over HTTPS

MTA-STS ([RFC 8461](https://www.rfc-editor.org/rfc/rfc8461), 2018) needs two
things from the receiving domain. A TXT record says a policy exists, and gives
its version:

```
_mta-sts.example.org.  TXT  "v=STSv1; id=20261023T120000"
```

And a web server at `mta-sts.example.org` serves the policy at a fixed address,
`https://mta-sts.example.org/.well-known/mta-sts.txt`:

```
version: STSv1
mode: enforce
mx: mx1.example.org
mx: *.backup.example.org
max_age: 604800
```

- **`mode`** is `enforce` (refuse to deliver without valid TLS), `testing`
  (deliver anyway, but report failures), or `none` (withdraw the policy).
- **`mx`** lists the only names allowed to receive mail for the domain. An MX
  record pointing anywhere else is ignored.
- **`max_age`** is how long, in seconds, a sender keeps the policy: here, a
  week. RFC 8461 allows up to a year.

A sender that knows the policy delivers only to a listed MX, over TLS, with a
certificate valid for that MX's name. If it cannot, it keeps the message and
retries, rather than sending it in the clear.

The policy is fetched over HTTPS, so an attacker would need a valid certificate
for `mta-sts.example.org` to fake it. The weakness is the first contact: a sender
that has never seen the policy can be fooled by an attacker who blocks the TXT
lookup. After that, the cached policy protects every delivery until it expires,
which is why senders "SHOULD proactively refresh cached policies before they
expire" ([§3.3](https://www.rfc-editor.org/rfc/rfc8461#section-3.3)).

The `id` is how senders notice a change: "To see if an updated policy is
available [...], the sender need only check the TXT record's version "id"
against the cached value" ([§3](https://www.rfc-editor.org/rfc/rfc8461#section-3)).
Change the policy without changing the `id`, and senders keep the old one.

Gmail was the first large provider to apply MTA-STS policies, in 2019
([Google Security Blog, 2019](https://security.googleblog.com/2019/04/gmail-making-email-more-secure-with-mta.html)).

## DANE: keys in signed DNS

DANE for SMTP ([RFC 7672](https://www.rfc-editor.org/rfc/rfc7672), 2015) takes a
different route. The domain signs its DNS with **DNSSEC**
([RFC 4033](https://www.rfc-editor.org/rfc/rfc4033)), so that answers can be
checked, and publishes a **TLSA** record for each MX
([RFC 6698](https://www.rfc-editor.org/rfc/rfc6698)):

```
_25._tcp.mx1.example.org.  TLSA  3 1 1  2bb1...e9f0
```

`3 1 1` means: the server's own key (3), its public key rather than the whole
certificate (1), hashed with SHA-256 (1). A sender that finds a signed TLSA
record must use TLS, and must check the server's key against it. The offer to
encrypt can no longer be stripped, and no certificate authority is involved.

DANE is stronger than MTA-STS, with no first-contact gap, but it needs DNSSEC on
both sides: on the receiving domain's zone, and in the sending server's
resolver. When both apply, DANE wins: senders "MUST NOT allow MTA-STS Policy
validation to override a failing DANE validation"
([RFC 8461 §2](https://www.rfc-editor.org/rfc/rfc8461#section-2)). Microsoft's
Exchange Online has accepted mail with inbound DANE since October 2024
([BleepingComputer, 2024](https://www.bleepingcomputer.com/news/microsoft/exchange-online-adds-inbound-dane-with-dnssec-for-everyone)).

The hard part is key rotation. A TLSA record that names a key must change
before the key does: "Two TLSA records MUST be published before employing a new
EE or TA public key or certificate"
([RFC 7672 §4](https://www.rfc-editor.org/rfc/rfc7672#section-4)). A server
whose certificate is renewed with a new key every 60 days, and whose TLSA record
is not updated first, stops receiving mail from DANE senders.

## TLS-RPT: being told what went wrong

TLS-RPT ([RFC 8460](https://www.rfc-editor.org/rfc/rfc8460)) gives a domain a
place to receive reports:

```
_smtp._tls.example.org.  TXT  "v=TLSRPTv1; rua=mailto:tls-reports@example.org"
```

Sending servers that support it send one JSON report a day, listing how many
sessions succeeded and why others failed: no STARTTLS, a certificate that did not
match, an expired certificate, a policy that could not be fetched. Without it, a
broken MTA-STS policy shows up only as mail that never arrives.

## Mixed adoption

A 2023 study of email security found that "MTA-STS is gaining traction
slowly", supported by six of the 47 providers it tested, and that DANE records
were looked up but "neither is validated or enforced by all providers"
([Blechschmidt and Stock, USENIX Security 2023](https://www.usenix.org/conference/usenixsecurity23/presentation/blechschmidt)).
The protections only work between senders and receivers that both take part.

## What goes wrong

- **`mode: enforce` with an MX not in the `mx` list,** after adding a new mail
  server. Senders that cached the policy stop delivering.
- **A certificate on `mta-sts.` that expires,** so the policy cannot be fetched
  or refreshed.
- **Changing the policy without changing the `id`,** so senders keep the old
  one until `max_age` runs out.
- **A new key without a new TLSA record first,** cutting off DANE senders.
- **DNSSEC that breaks:** an expired signature makes the whole zone fail to
  resolve for validating resolvers, mail included.
- **No TLS-RPT,** so none of the above is noticed.

Start with `mode: testing` and TLS-RPT, read the reports for a few weeks, then
switch to `enforce`.

## Check your own domain

```sh
dig +short TXT _mta-sts.example.org
curl -s https://mta-sts.example.org/.well-known/mta-sts.txt
dig +short TXT _smtp._tls.example.org
dig +dnssec +short TLSA _25._tcp.mx1.example.org
```

## How REST Mail handles it

- **Sending:** REST Mail fetches each recipient domain's MTA-STS policy before
  choosing how to deliver, and an `enforce` policy is applied to every path,
  SMTP and RESTMAIL alike: no listed MX with valid TLS, no delivery
  ([`internal/gateway/queue/worker.go`](../../../internal/gateway/queue/worker.go),
  [`rest-mail/go-mtasts`](https://github.com/rest-mail/go-mtasts); `MTASTS_ENFORCE`,
  on by default).
- **Receiving:** an administrator sets a domain's policy through the admin API,
  and REST Mail serves it at `/.well-known/mta-sts.txt` for
  `mta-sts.<domain>`, internationalised domains included
  ([`internal/api/handlers/mtasts.go`](../../../internal/api/handlers/mtasts.go)).
  `chore dns:show` prints the domain's `_mta-sts` record, `v=STSv1; id=<id>`,
  with an `id` derived from the policy file itself (a SHA-256 of what senders
  fetch), so it changes exactly when the policy does and the operator never
  picks one; `chore dns:check` reports a published `id` the policy has moved on
  from ([`internal/db/models/mtasts.go`](../../../internal/db/models/mtasts.go)).
  The `mta-sts.<domain>` host record, and a certificate covering that name, are
  not printed or obtained yet:
  [#727](https://github.com/rest-mail/rest-mail-server/issues/727).
- **TLS-RPT:** REST Mail accepts reports sent to it over HTTPS, at
  `/.well-known/smtp-tlsrpt`, with limits on size and rate
  ([`tlsrpt.go`](../../../internal/api/handlers/tlsrpt.go)), and `chore
  dns:show` prints each domain's `_smtp._tls` record pointing there, on the
  mail host. It sends them too: the queue counts what each outbound SMTP
  session's TLS came to, and once a day every recipient domain that publishes
  an `_smtp._tls` record gets the day before's report, by HTTPS or by mail
  signed with the DKIM key of REST Mail's own domain
  ([`tlsrpt_report.go`](../../../internal/gateway/queue/tlsrpt_report.go)).
  An MTA-STS policy it could not fetch, authenticate or read is reported as
  that failure. It also accepts reports by email at an existing mailbox configured
  with `TLSRPT_REPORT_ADDRESS` (`api.tlsReportAddress` in the chart). Publish a
  `mailto:` URI for that mailbox in the domain's `_smtp._tls` record to use it.
  Reports must be `multipart/report; report-type=tlsrpt`, contain a JSON or gzip
  report, and carry a valid full-body DKIM signature from the reporting domain
  ([RFC 8460 §§3 and 5.3](https://www.rfc-editor.org/rfc/rfc8460.html#section-5.3)).
  Recognised reports are consumed into the same table as HTTPS reports; unsigned
  or invalid reports are ignored. Ordinary mail still reaches the mailbox.
  Both the compressed and inflated report are capped at 512 KiB, the email at
  1 MiB, and the report at 100 policy entries. Email ingestion is disabled when
  the address is empty. DNS output continues to advertise the HTTPS endpoint.
- **DANE publication preparation:** `restmail tlsa-records` computes `3 1 1`
  records from the SMTP public certificate, with an optional staged key for
  rollover. `chore dns:show --tlsa-cert current.pem --tlsa-next-cert staged.pem`
  includes those candidates. See [preparing TLSA records](../../dane-tlsa.md)
  for the DNSSEC prerequisites and rollover sequence. Automatic safe
  certificate/key renewal remains in
  [#725](https://github.com/rest-mail/rest-mail-server/issues/725); the current
  ACME renewal rotates keys and must be coordinated before publishing TLSA.
- **DANE when sending:** with `DANE_RESOLVER` set to a DNSSEC-validating
  resolver on the same host (a loopback address), the queue applies DANE for
  SMTP ([RFC 7672](https://www.rfc-editor.org/rfc/rfc7672)) to recipient
  domains whose MX records are DNSSEC-secure. An MX host with usable TLSA
  records gets the message only over TLS its records authenticate, and those
  records, not MTA-STS, decide that host's TLS. A bogus answer, a resolver that
  does not answer, a missing STARTTLS or a certificate no record matches
  defers the message; it is never sent to that host in the clear. Unset, DANE
  is off ([`internal/dane`](../../../internal/dane),
  [`dane.go`](../../../internal/gateway/queue/dane.go)). DANE sessions are in
  the recipient domain's TLS-RPT report under policy type `tlsa`, with the
  host's TLSA records as the policy strings: a certificate no record matches is
  `tlsa-invalid`, TLSA records that fail DNSSEC validation `dnssec-invalid`.

## Sources

- [RFC 8461](https://www.rfc-editor.org/rfc/rfc8461): SMTP MTA Strict Transport Security (MTA-STS).
- [RFC 8460](https://www.rfc-editor.org/rfc/rfc8460): SMTP TLS Reporting.
- [RFC 7672](https://www.rfc-editor.org/rfc/rfc7672): SMTP Security via Opportunistic DANE TLS.
  [RFC 6698](https://www.rfc-editor.org/rfc/rfc6698): the TLSA record.
  [RFC 4033](https://www.rfc-editor.org/rfc/rfc4033): DNSSEC.
- Google Security Blog: [Gmail making email more secure with MTA-STS (2019)](https://security.googleblog.com/2019/04/gmail-making-email-more-secure-with-mta.html).
- BleepingComputer: [Exchange Online adds inbound DANE with DNSSEC for everyone (2024)](https://www.bleepingcomputer.com/news/microsoft/exchange-online-adds-inbound-dane-with-dnssec-for-everyone).
- Blechschmidt and Stock: [Extended Hell(o), USENIX Security 2023](https://www.usenix.org/conference/usenixsecurity23/presentation/blechschmidt).
- REST Mail: [`internal/api/handlers/mtasts.go`](../../../internal/api/handlers/mtasts.go),
  [`internal/api/handlers/tlsrpt.go`](../../../internal/api/handlers/tlsrpt.go),
  [`rest-mail/go-mtasts`](https://github.com/rest-mail/go-mtasts).
