# Preparing SMTP TLSA records

REST Mail can prepare TLSA publication data for its MX's public certificate.
The queue already authenticates outbound SMTP through a configured loopback
DNSSEC resolver, defers bogus DNS or TLSA mismatches, and reports DANE results
through TLS-RPT. The chart can supply that resolver with `smtp.dane.enabled`
(off by default); see the [chart documentation](../helm/restmail/README.md#optional-outbound-dane-resolver).

Use the public certificate actually served on port 25 for the MX hostname,
including when the sender supplies that hostname as SNI. A PEM chain must
start with the serving certificate. A private key is unnecessary.

```sh
restmail tlsa-records --mail-host mx.example.com --cert smtp-current.pem
```

The command prints `_25._tcp.mx.example.com. 3600 IN TLSA 3 1 1 <digest>`.
`3 1 1` means DANE-EE, SubjectPublicKeyInfo, SHA-256. It hashes the complete
DER SubjectPublicKeyInfo, so a new certificate with the same public key
produces the same record. `--json` returns the records together with
`dnssec_required: true`, `automatic_rollover: false` and rollover instructions.

To include these publication candidates in the installed server's DNS list:

```sh
chore dns:show --tlsa-cert smtp-current.pem
```

This requires an updated `restmail` CLI locally. The file is local to the
operator's machine; the helper does not guess a certificate from another
component's mount. Without this option, the DNS listing behaves as before.
The helper prepares records; it does not publish them or establish that the
zone has working DNSSEC. `chore dns:check` does not validate these optional
TLSA candidates or their DNSSEC signatures yet.

Publish TLSA only after the zone containing the MX hostname has a signed,
validated DNSSEC chain of trust. A TLSA record in an unsigned zone does not
establish authenticated DANE policy. This can be a different zone from the
recipient domain. Also check that the recipient domain's MX routing has the
DNSSEC protection required by [RFC 7672 §2](https://www.rfc-editor.org/rfc/rfc7672#section-2).

## A planned key rollover

[RFC 7672 §4](https://www.rfc-editor.org/rfc/rfc7672#section-4) requires an
overlap before a new key starts serving. Prepare the next public certificate
without activating it, then generate both records:

```sh
restmail tlsa-records --mail-host mx.example.com \
  --cert smtp-current.pem --next-cert smtp-staged.pem
# Or, in the deployment's listing:
chore dns:show --tlsa-cert smtp-current.pem --tlsa-next-cert smtp-staged.pem
```

Publish both records in the signed zone. Wait until resolvers can no longer
have the previous TLSA RRset and related signature RRsets cached, then
activate the staged certificate and key on every SMTP instance. Keep records
for every key still serving, including instances finishing a rollout; retire
the old record once the old key is no longer served. The helper deduplicates
identical public keys while preserving current-before-staged order.

## ACME renewal preserves the published key

Both the API's database-backed ACME renewal and `instance acme-issue` reuse
an existing serving key. The renewed certificate's SubjectPublicKeyInfo stays
the same, so its `3 1 1` TLSA digest stays valid. The ACME account key remains
a separate identity. First issuance generates a serving key; renewal can reuse
ECDSA, RSA or Ed25519 keys, including a key whose old certificate has expired.

A damaged, missing, mismatched or undecryptable existing serving pair stops
renewal before a new order/store replaces it. Restore the key or plan a
rotation with the overlap above. `-renew-before=0` forces a new certificate
order under the same serving key, not an unannounced key rotation. Existing
instance publication locking and interrupted-write recovery still apply.

The TLSA helper does not publish DNS, stage a deliberate key rotation, or
coordinate external certificate managers. For externally managed certificates,
configure key reuse in that manager or use the staged rollover above. Keep
records for every public key still serving while instances finish a rollout.
