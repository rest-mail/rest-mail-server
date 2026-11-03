# Certificates are about to expire

Two kinds of certificate matter: the public HTTPS one in front of the API
(terminated by the ingress or proxy, not by REST Mail), and the ones the mail
gateways serve on SMTP, IMAP and POP3. One of three things renews them:
cert-manager in a cluster, Certbot (or another manager) writing files
([Externally managed TLS certificates](../external-certificates.md)), or the
built-in ACME client (`ACME_ENABLED=true`). Whichever it is, it should be the
only one for a given name.

## Symptoms

- Mail clients or remote servers warn about an expiring or expired
  certificate; remote servers with MTA-STS or DANE refuse to deliver
  (`tls` refusals in their reports, `/settings/tls-reports`).
- The admin webmail's Certificates page (`/settings/certificates`) shows a
  banner: certificates expiring in the next 30 days.
- The gateway logs `tls: certificate reload failed`.

The `CertificateExpiringSoon` alert depends on `restmail_cert_expiry_days`,
which nothing sets today, so it does not fire (#1630). Check expiry by the
steps below until that is fixed.

## Confirm

1. **What the database holds.** Admin webmail: Settings → Certificates
   (`/settings/certificates`), with each certificate's domain, issuer and
   expiry. API (`domains:read`):

   ```bash
   curl -sS -H "Authorization: Bearer $RESTMAIL_TOKEN" "$API/api/v1/admin/certificates"
   ```

   Each entry has `not_after` and `auto_renew`.
2. **What each listener actually serves**, which is what matters to clients:

   ```bash
   openssl s_client -connect mail.example.com:465 -servername mail.example.com </dev/null 2>/dev/null | openssl x509 -noout -enddate
   openssl s_client -connect mail.example.com:993 -servername mail.example.com </dev/null 2>/dev/null | openssl x509 -noout -enddate
   openssl s_client -connect mail.example.com:25 -starttls smtp -servername mail.example.com </dev/null 2>/dev/null | openssl x509 -noout -enddate
   openssl s_client -connect mail.example.com:443 -servername mail.example.com </dev/null 2>/dev/null | openssl x509 -noout -enddate
   ```

   Repeat for 143 (`-starttls imap`), 110 (`-starttls pop3`) and 995 if you
   offer them.
3. **Whether reloads are failing.** Each gateway's `/metrics`:
   - `restmail_tls_cert_reload_failing` above 0: a renewed pair was rejected
     and the old one is still being served.
   - `restmail_tls_cert_reload_failures_total` rising: the same, counted.
   - `restmail_tls_cert_missing_total` rising (alert
     `DomainCertificateMissing`): a domain has no certificate at all.

   `chore server:logs smtp --since 24h` (and `imap`, `pop3`) show the reload
   error.

## Act

### The renewal did not happen

- **cert-manager**: `kubectl describe certificate -n rest-mail` and the
  cert-manager logs say why. `cmctl renew -n rest-mail <certificate>` asks for
  a renewal now.
- **Certbot**: `certbot renew` (add `--force-renewal` for a certificate not
  yet in its window), then check the deploy hook ran
  ([Schedule renewals and observe failures](../external-certificates.md#schedule-renewals-and-observe-failures)).
- **Built-in ACME**: it checks every 12 hours and renews certificates with
  `auto_renew` set that expire within 30 days. Read `chore server:logs api
  --since 24h` for the ACME error (often port 80 not reaching the API for the
  HTTP-01 challenge). Renewing one now: not available yet (#1628). The
  workaround is to fix the cause and restart the API, which checks again a
  minute after it starts.
- **A certificate you uploaded** (`auto_renew` false): upload the new one in
  `/settings/certificates`, or `POST /api/v1/admin/certificates`.

### The renewal happened but the old certificate is still served

Every mail listener reloads its certificate files without a restart; this is
proven for each SMTP, IMAP and POP3 listener, including what happens when a
renewed pair is broken (PR #1555). A new connection gets the new certificate
within about five seconds
([File reload contract](../external-certificates.md#file-reload-contract)).
If it does not:

1. `restmail_tls_cert_reload_failing` above 0 means the new files were
   rejected: the key does not match the certificate, a file is half written,
   or the gateway cannot read it. The log line says which.
2. Fix the files (write the key, then the certificate, each renamed into
   place), and the next handshake picks them up. The gauge drops back when
   the reload succeeds.

## Verify

- The `openssl s_client` checks above show the new `notAfter` on every port.
- `restmail_tls_cert_reload_failing` is back to 0 on each gateway.
- `/settings/certificates` shows the new expiry.
- `chore server:verify` reports "certificate valid" for the public address.

## Escalate

- cert-manager or the ingress is not yours: to whoever runs the cluster.
- The CA refuses issuance (rate limits, CAA records): wait for the limit, or
  fix the domain's CAA record with its DNS provider.
- The files are right and the gauge stays up: open an issue with the reload
  log lines (no key material).
