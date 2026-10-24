---
title: "Finding the server: autoconfig, Autodiscover and SRV"
part: How email works
summary: How a mail app turns an email address into server names, ports and security settings, and why a wrong answer here can send passwords in the clear.
diagram: ../diagrams/discovery.workflow.json
---

# Finding the server: autoconfig, Autodiscover and SRV

A new mail app asks for an address and a password, and a few seconds later the
mail appears. In between, it has worked out which servers to use for reading
and sending, which ports, whether to encrypt from the first byte or upgrade
with `STARTTLS`, and what username to sign in with. Nothing in an email address
says any of that.

There is no single standard for it. Mail apps try several methods in turn, and
use the first good answer. Each method is a small protocol of its own.

![How a mail app finds its servers](../diagrams/discovery.svg)

*The diagram's source is [`discovery.workflow.json`](../diagrams/discovery.workflow.json).*

## Why it matters

Whatever the app finds is what it saves, and keeps using for years. A good
answer means encrypted connections to the right servers. A bad one means
support calls, or worse: an answer that says "port 143, no encryption" makes
the app send the user's password in the clear. That is why the methods below
are tried in order of how much they can be trusted, and why
[RFC 8314](https://www.rfc-editor.org/rfc/rfc8314#section-5.1) tells apps to
ignore any advertised setting that does not use TLS.

## Autoconfig: ask the provider

**Autoconfig** began in Thunderbird, and is now being standardised at the IETF
as [draft-ietf-mailmaint-autoconfig](https://datatracker.ietf.org/doc/draft-ietf-mailmaint-autoconfig/)
(an Internet-Draft, last revised in June 2026; not yet an RFC). The app asks
the mail provider for an XML file describing its servers, at these addresses,
in order (§4.1):

1. `https://autoconfig.example.org/mail/config-v1.1.xml?emailaddress=bob@example.org`
2. `https://example.org/.well-known/autoconfig/mail/config-v1.1.xml`
3. `http://autoconfig.example.org/mail/config-v1.1.xml` (plain HTTP, for older
   setups)

The answer looks like this:

```xml
<clientConfig version="1.1">
  <emailProvider id="example.org">
    <domain>example.org</domain>
    <incomingServer type="imap">
      <hostname>mail.example.org</hostname>
      <port>993</port>
      <socketType>SSL</socketType>
      <username>%EMAILADDRESS%</username>
      <authentication>password-cleartext</authentication>
    </incomingServer>
    <outgoingServer type="smtp">
      <hostname>mail.example.org</hostname>
      <port>465</port>
      <socketType>SSL</socketType>
      <username>%EMAILADDRESS%</username>
      <authentication>password-cleartext</authentication>
    </outgoingServer>
  </emailProvider>
</clientConfig>
```

`socketType` `SSL` means TLS from the first byte; `STARTTLS` means connect in
the clear and upgrade. `password-cleartext` means the password is sent as it
is, which is safe only inside that TLS connection.

If the provider has no file, the app tries the **ISPDB**, a central database
of settings for large providers. The draft says it covers "most mail providers
with a market share larger than 0.1%" (§4.2). Then it looks up the domain's
**MX record**, and asks the company that runs the MX, because many domains'
mail is hosted by someone else (§4.3).

The draft also says what a server should not do: reveal which addresses exist.
"Autoconfig servers SHOULD return a real configuration, even if the email
address sent as URL parameter does not exist" (§4.1.1).

## Autodiscover: Microsoft's way

Outlook and Exchange use **Autodiscover**. Outlook sends an XML request to
`https://example.org/autodiscover/autodiscover.xml` or
`https://autodiscover.example.org/autodiscover/autodiscover.xml`, and gets the
settings back in Microsoft's format
([Microsoft: Autodiscover service](https://learn.microsoft.com/en-us/exchange/architecture/client-access/autodiscover)).
It was built for Exchange, and for Microsoft 365 it also finds Exchange's own
protocols, but it can describe IMAP and SMTP servers too.

## SRV records: ask the DNS

[RFC 6186](https://www.rfc-editor.org/rfc/rfc6186) puts the same information in
the DNS, as SRV records:

```
_submissions._tcp.example.org.  SRV  0 1 465 mail.example.org.
_imaps._tcp.example.org.        SRV  0 1 993 mail.example.org.
_pop3s._tcp.example.org.        SRV  0 1 995 mail.example.org.
_submission._tcp.example.org.   SRV  0 0 0   .
_imap._tcp.example.org.         SRV  0 0 0   .
_pop3._tcp.example.org.         SRV  0 0 0   .
```

A target of `.` means "not offered here": with those last three lines, an app
knows not to try the unencrypted ports at all
([RFC 6186 §3.4](https://www.rfc-editor.org/rfc/rfc6186#section-3.4)).
`_submissions` (with an *s*), for submission on 465, was added by
[RFC 8314 §5.1](https://www.rfc-editor.org/rfc/rfc8314#section-5.1).

SRV records have a weakness: without DNSSEC, an attacker who can answer DNS
queries can point an app anywhere. RFC 6186 says an app must check the server's
certificate against the domain, not just against the host name the SRV record
gave ([§6](https://www.rfc-editor.org/rfc/rfc6186#section-6)).

## Apple: profiles

Apple's Mail can be set up from a **configuration profile**: a `.mobileconfig`
file, which a provider offers for download, holding the account's servers and
settings ([Apple: Mail payload settings](https://support.apple.com/guide/deployment/mail-payload-settings-dep6ac80a17/web)).

## Guessing, and asking

When nothing works, many apps guess common names (`imap.example.org`,
`mail.example.org`) and try them. The autoconfig draft asks apps to rank
guessing below the secure methods, and to show the user the domain names they
found before saving them, so a lookalike domain is noticed (§4.5, §5.1). Last
of all, the user types the settings in by hand.

## What goes wrong

- **No autoconfig, no SRV records:** every app guesses, and some guess
  `STARTTLS` on 143 or 587, or no encryption at all.
- **An autoconfig file that advertises cleartext** or `STARTTLS` when the server
  offers implicit TLS.
- **SRV records for ports nobody listens on,** or missing `.` records for the
  services that are not offered.
- **A certificate that does not cover `autoconfig.` or `autodiscover.`,** so the
  lookup fails over HTTPS and the app falls back to weaker methods.
- **Usernames that are not the email address,** which autoconfig can only
  express awkwardly; the draft asks servers to accept the address as the login
  name (§4.1.1).

## How REST Mail handles it

- **Autoconfig** is served at both HTTPS addresses, and **Autodiscover** at
  `/autodiscover/autodiscover.xml`. Both describe IMAP on 993 and SMTP on 465,
  TLS from the first byte, with the email address as the login name. They never
  advertise a cleartext or `STARTTLS` port, because REST Mail does not listen on
  one ([`internal/api/handlers/autoconfig.go`](../../../internal/api/handlers/autoconfig.go)).
- **The server they name is the mail host**, `GATEWAY_HOSTNAME`, the name the
  MX record points to and the certificate covers, never the email domain
  itself, which is usually a website (#722).
- **The address is optional**, as the draft has it: without `?emailaddress=`,
  the domain is the host the request was made to, `autoconfig.<domain>` or,
  at the `.well-known` address, the domain itself (#721).
- **SRV records** are in the set REST Mail prints for a domain (`chore
  dns:show`) and checks (`chore dns:check`): the six records above, the
  implicit-TLS services on the mail host and "." for the cleartext ones, with
  ports read from the listeners' own settings
  ([`internal/dns/mail_records.go`](../../../internal/dns/mail_records.go), #712).
- **Apple profiles** are not offered.

## Sources

- Autoconfig: [draft-ietf-mailmaint-autoconfig-06](https://datatracker.ietf.org/doc/draft-ietf-mailmaint-autoconfig/)
  (Internet-Draft, June 2026), [Mozilla: Thunderbird autoconfiguration](https://wiki.mozilla.org/Thunderbird:Autoconfiguration).
- Microsoft: [Autodiscover service in Exchange Server](https://learn.microsoft.com/en-us/exchange/architecture/client-access/autodiscover).
- SRV: [RFC 6186](https://www.rfc-editor.org/rfc/rfc6186),
  [RFC 8314 §5.1](https://www.rfc-editor.org/rfc/rfc8314#section-5.1),
  [RFC 2782](https://www.rfc-editor.org/rfc/rfc2782).
- Apple: [Mail payload settings](https://support.apple.com/guide/deployment/mail-payload-settings-dep6ac80a17/web).
- REST Mail: [`internal/api/handlers/autoconfig.go`](../../../internal/api/handlers/autoconfig.go),
  [`internal/dns/`](../../../internal/dns/).
