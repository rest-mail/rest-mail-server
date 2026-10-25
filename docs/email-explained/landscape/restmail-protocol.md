---
title: "RESTMAIL: delivery between servers over HTTPS"
part: The landscape, and where REST Mail fits
summary: How two REST Mail servers find out about each other and deliver over HTTPS instead of SMTP, what that gains, and why it always falls back to ordinary SMTP for everyone else.
diagram: ../diagrams/restmail.sequence.json
---

# RESTMAIL: delivery between servers over HTTPS

Every upgrade to email that has succeeded has had the same shape: better
between the servers that support it, unchanged for those that do not
([Efforts to modernise email](modernising-email.md)). RESTMAIL is REST Mail's
upgrade of that shape for delivery itself. When a REST Mail server delivers to
another REST Mail server, it can skip the SMTP conversation and post the
message over HTTPS. To every other server, it is an ordinary SMTP server.

This is REST Mail's own protocol, not an IETF standard. Its endpoints are part of
REST Mail's API description ([`openapi.yaml`](../../../openapi.yaml)).

![How a REST Mail server delivers to another over HTTPS](../diagrams/restmail.svg)

*The diagram's source is [`restmail.sequence.json`](../diagrams/restmail.sequence.json).*

## Why HTTPS

SMTP works, everywhere, and RESTMAIL never replaces it for anyone who does not
speak RESTMAIL. Between two servers that both do, HTTPS offers what SMTP had to
add later, or never got:

- **Encryption is not optional,** and cannot be stripped: there is no cleartext
  offer to delete ([Encryption in transit](../security/encryption-in-transit.md#the-downgrade)).
- **The server's certificate is always checked,** against a name taken from the
  recipient's MX records, the web's ordinary way, rather than accepted whatever it
  is.
- **The message travels as it is,** in one request, with no dot-stuffing, no line
  length limits, and no 7-bit history.
- **Ordinary web infrastructure:** HTTP libraries, load balancers, logs and
  tools everyone already has.

## How it works

1. **The hint.** REST Mail's SMTP gateway lists an extension called `RESTMAIL`
   in its `EHLO` reply. A sending REST Mail server that sees it on a recipient's
   MX knows the MX may speak RESTMAIL. The hint arrives in cleartext, so it is
   only a hint: any address it carries is ignored.
2. **Verified discovery.** The sender asks the MX host itself, over HTTPS, where
   to deliver: `GET https://<mx-host>/.well-known/restmail?domain=example.org`.
   The certificate must be valid for the MX host name; redirects are refused; the
   answer is capped at 4 KiB. The answer names the endpoint:
   `{"endpoint": "https://mail.example.org/restmail"}`. A `404` or `410` here is a
   verified "not hosted": the sender records that, and uses SMTP.
3. **Delivery.** The sender posts the message, DKIM-signed as always, to
   `POST <endpoint>/messages`, again verifying the certificate.
4. **Acceptance.** The receiver accepts the delivery from a trusted peer network,
   or when the message carries a DKIM signature that verifies and aligns with the
   `From:` domain; it refuses an unauthenticated delivery claiming one of its own
   domains. Then the message goes through the domain's inbound pipeline, exactly
   as one that arrived by SMTP.

The receiver can also answer whether a mailbox exists (`GET
/restmail/mailboxes?address=`), slowing down each answer for a mailbox that does
not, so it cannot be used to collect addresses.

## Safety rules

- **An MTA-STS policy still applies.** Under an `enforce` policy, a sender only
  asks a discovery host the policy names ([MTA-STS](../security/mta-sts-and-dane.md)).
- **Only the MX host can say where the endpoint is.** The cleartext `EHLO`
  advertisement cannot redirect mail anywhere (#291).
- **What a sender remembers** is only whether an MX speaks REST, and only for that
  MX host; the endpoint is asked for again on every delivery.

## What it is not

- **Not a replacement for SMTP.** Most of the world's servers will never speak
  it, and REST Mail delivers to them over SMTP, as it receives from them.
- **Not end-to-end encryption.** Each server can still read the mail it holds,
  as with SMTP.

## How REST Mail handles it

- **Advertising and discovery:** the SMTP gateway advertises `RESTMAIL`
  ([`internal/gateway/smtp/server.go`](../../../internal/gateway/smtp/server.go)),
  and the API answers `/.well-known/restmail`
  ([`internal/api/handlers/restmail_discovery.go`](../../../internal/api/handlers/restmail_discovery.go)).
- **Sending:** the outbound queue probes, discovers and delivers
  ([`internal/gateway/queue/restmail_discovery.go`](../../../internal/gateway/queue/restmail_discovery.go),
  [`worker.go`](../../../internal/gateway/queue/worker.go)).
- **Receiving:** deliveries are authenticated before they are accepted
  ([`internal/api/handlers/restmail_deliver_auth.go`](../../../internal/api/handlers/restmail_deliver_auth.go)).
- **Fallback today:** a RESTMAIL delivery that fails for any reason other than a
  verified "not hosted" is retried over SMTP. The design decided in #292 goes
  further: once a server knows a domain runs REST Mail, it uses only REST for it,
  so an attacker who blocks HTTPS cannot push the mail back to SMTP. That, and
  protecting the very first contact, are parked until the core mail flow is
  complete (#292).

## Sources

- REST Mail: [`openapi.yaml`](../../../openapi.yaml),
  [`internal/gateway/queue/`](../../../internal/gateway/queue/),
  [`internal/api/handlers/`](../../../internal/api/handlers/),
  [`docs/MANUAL.md`](../../MANUAL.md) §4.3.
