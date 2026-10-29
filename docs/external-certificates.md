# Externally managed TLS certificates

For a standalone Linux installation without cert-manager, the supported integration
is **Certbot with HTTP-01**, its renewal scheduler, and PEM files consumed by REST
Mail. Certbot owns issuance and renewal; REST Mail only loads the resulting files.
Keep `ACME_ENABLED=false` and do not run `instance:certs:issue` for these same names.
Existing cert-manager, acme.sh, or Caddy installations can publish the same file
interface using their own renewal mechanism.

## Issue and publish with Certbot

Install Certbot using the [instructions for your operating system](https://certbot.eff.org/instructions).
The example below uses the standalone HTTP-01 plugin: all requested names must
resolve to this host, public TCP port 80 must reach it, and port 80 must be free
for every issuance/renewal attempt. If a web server already owns port 80, use
Certbot's webroot integration and route `/.well-known/acme-challenge/` to its
webroot instead. REST Mail's mail ports do not answer ACME challenges.

Certbot keeps account keys and certificate history under `/etc/letsencrypt`.
Back up that directory privately. Publish a separate read-only serving directory
so gateway processes need no access to Certbot's account keys. Create a local
`restmail-tls` group, add each gateway service user to it, then create the directory:

```sh
sudo install -d -o root -g restmail-tls -m 0750 /etc/restmail/tls
```

Install this root-owned, executable script as
`/etc/letsencrypt/renewal-hooks/deploy/restmail` (replace the example hostname):

```sh
#!/bin/sh
set -eu
[ "$RENEWED_LINEAGE" = /etc/letsencrypt/live/mail.example.com ] || exit 0
base=/etc/restmail/tls
release=$(mktemp -d "$base/.renewal.XXXXXXXX")
chgrp restmail-tls "$release"
chmod 0750 "$release"
install -m 0644 "$RENEWED_LINEAGE/fullchain.pem" "$release/server.crt"
install -m 0640 -g restmail-tls "$RENEWED_LINEAGE/privkey.pem" "$release/server.key"
ln -s "$(basename "$release")" "$release.link"
mv -Tf "$release.link" "$base/current"
```

The final rename publishes a whole pair on Linux. Retained release directories
contain private keys; include them in your private storage/retention policy.
Issue the initial certificate and invoke the publication hook:

```sh
sudo certbot certonly --standalone --preferred-challenges http \
  --non-interactive --agree-tos --email ops@example.com \
  --cert-name mail.example.com --reuse-key \
  -d mail.example.com -d imap.example.com -d pop.example.com \
  --deploy-hook /etc/letsencrypt/renewal-hooks/deploy/restmail
```

Include every client-facing hostname in the SANs. `--reuse-key` preserves the
public-key hash if you publish DANE TLSA `3 1 1` records; deliberately rotating a
key also requires coordinating those DNS records.

Configure **each** SMTP, IMAP, and POP3 gateway:

```dotenv
ACME_ENABLED=false
TLS_CERT_PATH=/etc/restmail/tls/current/server.crt
TLS_KEY_PATH=/etc/restmail/tls/current/server.key
```

For containers, mount the whole `/etc/restmail/tls` directory read-only at that
path, with a matching numeric group ID. Do not mount individual files: a bind
mount of an old inode will not follow later replacements. For Kubernetes, mount
the whole Secret volume; `subPath` file mounts do not receive Secret updates.

## Schedule renewals and observe failures

Use the renewal timer/cron job installed by your Certbot package. Confirm it with
`systemctl list-timers --all` and the package's documentation. If none is
installed, schedule `certbot renew --quiet` twice daily under root (use the full
path returned by `command -v certbot`). Do not run competing renewal schedulers.
The deploy-hook directory publishes successfully renewed certificates.

Monitor the renewal job's exit status and `/var/log/letsencrypt/letsencrypt.log`.
Alert on failed renewals, failed publication hooks, and the expiration date of the
certificate actually served by every gateway. A hook failure can leave the old
serving pair in place even when issuance succeeded; renewal success alone does
not establish that a gateway has the new certificate. See Certbot's
[renewal and hook behavior](https://eff-certbot.readthedocs.io/en/stable/using.html#renewing-certificates).

## File reload contract

- SMTP (STARTTLS and implicit TLS), IMAP, and POP3 share the same file loader.
  Primary files and per-domain `TLS_CERT_DIR/<domain>.crt` / `<domain>.key` pairs
  are checked on demand, at most once every **five seconds per loaded pair**.
  A new full TLS handshake after that interval picks up changed file identity,
  modification time, or size. Symlink target replacements are followed even if
  directory watcher notifications are absent. No restart or signal is required.
- A missing, malformed, truncated, or mismatched replacement logs
  `tls: certificate reload failed; retaining last usable pair` at error level.
  The previous complete pair remains in memory, and the next eligible handshake
  retries. Repeated failures are logged once until a successful check. A valid
  replacement logs `tls: reloaded certificate`. Bad initial primary files stop
  startup. There is no automatic issuance or expiration extension on load failure.
- Each process also reports failed replacements on its `/metrics` endpoint.
  `restmail_tls_cert_reload_failures_total` counts each failed replacement once,
  until a later check succeeds. `restmail_tls_cert_reload_failing` is the number
  of pairs still serving their previous files because the current files do not
  load; alert while it is above zero. Both update only when a new handshake
  triggers a check, so a listener without traffic reports its last check.
- Existing TLS connections keep their negotiated identity. Resumed TLS sessions
  may reuse their original authentication; verify with a fresh client/session.
- The API's `INTERNAL_MTLS_SERVER_CERT` / `INTERNAL_MTLS_SERVER_KEY` pair and
  gateways' `INTERNAL_MTLS_CLIENT_CERT` / `INTERNAL_MTLS_CLIENT_KEY` pairs use
  this same reload contract. These identities must be issued by your internal
  CA, not the public Certbot certificate above. Internal CA trust pools are loaded
  at startup: changing trust anchors still requires a coordinated restart.
- Public API HTTPS is terminated by your reverse proxy, whose certificate reload
  settings must be configured separately. The public API itself listens on HTTP.

For SNI directories, publish fullchain PEM as `<domain>.crt` and the private key
as `<domain>.key`; symlinks into the publication directory are supported. The
primary pair remains the fallback for clients without SNI and names it covers.

## Validate renewal on a public test host

Public staging validation for issue #123 still needs an appropriate test host;
the repository tests use local certificates and do not establish public ACME
reachability. On that host:

1. Use a separate test hostname and Certbot `--staging` lineage/publication path.
   Issue once, then configure the test gateways to consume its files. Staging
   certificates are untrusted by normal clients; never publish them to production.
2. Record the serial/fingerprint served on each enabled listener. For example:

   ```sh
   openssl s_client -connect mail.example.com:465 -servername mail.example.com </dev/null 2>/dev/null | openssl x509 -noout -serial -fingerprint -enddate
   openssl s_client -starttls smtp -connect mail.example.com:25 -servername mail.example.com </dev/null 2>/dev/null | openssl x509 -noout -serial -fingerprint -enddate
   ```

   Also check IMAP/POP3 implicit TLS and STARTTLS where enabled, and SMTP without
   SNI (`-noservername`). Check internal mTLS with a valid internal client identity
   separately if enabled. Use the appropriate staging CA trust when verifying it.
3. Force **one staging renewal** with `certbot renew --cert-name <test-lineage>
   --force-renewal`. Confirm publication, then repeat fresh handshakes after five
   seconds. Serial/fingerprint must change with no gateway restart. Record host,
   time, job output, reload logs, and before/after identities.
4. On the isolated test publication path, replace only one file or write invalid
   PEM. After five seconds, confirm new handshakes still use the last good pair
   and an error is visible. Restore a complete valid pair and confirm recovery.
5. Test challenge failure (for example, block port 80 on this test host), confirm
   the renewal job reports failure, and confirm it leaves the served pair intact.
   Restore challenge routing and verify a successful scheduled attempt.

`certbot renew --dry-run` tests challenge routing without saving a replacement;
it does **not** prove that REST Mail reloads a renewed certificate.
