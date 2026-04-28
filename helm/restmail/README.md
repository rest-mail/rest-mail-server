# restmail Helm chart

Helm chart for the rest-mail "mail3" stack.

## What this chart deploys

- `postgres` — single-replica StatefulSet running PostgreSQL 16, backing the
  REST API and protocol gateways. Persistent storage via PVC.
- `api` — the Go REST API.
- `js-filter` — the JavaScript filter sidecar consumed by the API.
- `smtp-gateway` — SMTP listener on 25 (inbound), 587 (submission), 465
  (submissions/TLS).
- `imap-gateway` — IMAP listener on 143 (plain) and 993 (TLS).
- `pop3-gateway` — POP3 listener on 110 (plain) and 995 (TLS).
- A ServiceAccount, a ConfigMap for non-secret API config, and an optional
  Ingress for the API.

This chart deploys **only** the rest-mail product surface. It does **not** include:

- Postfix, Dovecot, rspamd, ClamAV, fail2ban (the "traditional mailserver"
  stack — see `rest-mail/reference-mailserver`).
- DNS (dnsmasq), TLS issuance (cert-manager / certgen), mail-internet
  simulation (`rest-mail/testbed`).
- The webmail front-end or the marketing website.

## Infrastructure assumptions

The chart runs on any conformant Kubernetes cluster (1.27+) and assumes:

1. **Real DNS.** Pods use `dnsPolicy: ClusterFirst`. In-cluster Service DNS
   resolves peer services (`api` finds `postgres` by Service name).
2. **External TLS Secret.** A Secret of type `kubernetes.io/tls` named
   `restmail-tls` (override via `tls.secretName`) is mounted into every
   gateway pod at `/certs`. Populate this with cert-manager,
   external-secrets, or any other operator that produces a `kubernetes.io/tls`
   Secret with `tls.crt` + `tls.key` keys.
3. **External credentials Secrets.**
   - `restmail-postgres` — keys `POSTGRES_USER`, `POSTGRES_PASSWORD`,
     `POSTGRES_DB`.
   - `restmail-api` — keys `JWT_SECRET`, `MASTER_KEY`.
4. **LoadBalancer support** for SMTP/IMAP/POP3 Services — typically a cloud
   LB (AWS NLB, GCE LB, MetalLB on bare metal). For kind/k3s without a LB
   driver, override `*.service.type` to `NodePort` (the dev values file
   already does this).
5. **PersistentVolume provisioner.** PostgreSQL data and API attachments use
   PVCs.

## Install (production)

1. Create the required Secrets out-of-band:

   ```sh
   # TLS certificate (cert-manager will manage this in real deployments).
   kubectl create secret tls restmail-tls \
     --cert=path/to/fullchain.pem \
     --key=path/to/privkey.pem

   # PostgreSQL credentials.
   kubectl create secret generic restmail-postgres \
     --from-literal=POSTGRES_USER=restmail \
     --from-literal=POSTGRES_PASSWORD="$(openssl rand -base64 32)" \
     --from-literal=POSTGRES_DB=restmail

   # API credentials.
   kubectl create secret generic restmail-api \
     --from-literal=JWT_SECRET="$(openssl rand -base64 64)" \
     --from-literal=MASTER_KEY="$(openssl rand -base64 32)"
   ```

2. Install:

   ```sh
   helm install restmail helm/restmail \
     --set mailserver.hostname=mx.example.com \
     --set mailserver.domain=example.com
   ```

   `mailserver.hostname` and `mailserver.domain` are required — the chart
   refuses to render without them.

3. (Optional) Enable the API ingress:

   ```sh
   helm upgrade restmail helm/restmail \
     --set mailserver.hostname=mx.example.com \
     --set mailserver.domain=example.com \
     --set api.ingress.enabled=true \
     --set api.ingress.className=nginx \
     --set api.ingress.host=api.example.com \
     --set api.ingress.tls.enabled=true \
     --set api.ingress.tls.secretName=restmail-api-tls
   ```

## Install (development against rest-mail/testbed in kind/k3s)

The dev override file `values-dev.yaml` encodes the testbed-specific
network and credential overlay:

- `dnsPolicy: None` + `dnsConfig.nameservers: [10.99.0.10]` so peers resolve
  through the testbed's dnsmasq.
- `mailserver.hostname: mail3.test` to match the testbed's domain.
- Inline cleartext dev credentials (no external Secret operator required).
- Service type `NodePort` for the gateways (kind/k3s lack a cloud LB).

1. Bring up the testbed first:

   ```sh
   git clone git@github.com:rest-mail/testbed.git
   cd testbed && task up
   ```

2. Fetch the testbed CA and create a TLS Secret for `mail3.test`:

   ```sh
   # The testbed publishes a CA + per-host certs under its certs volume.
   # The exact extraction command depends on the testbed's task interface;
   # at minimum the resulting Secret must hold the mail3.test cert/key.
   task -d /path/to/testbed ca:fetch    # writes ./mail3.test.crt + .key
   kubectl create secret tls testbed-mail3-tls \
     --cert=./mail3.test.crt \
     --key=./mail3.test.key
   ```

3. Install:

   ```sh
   helm install restmail helm/restmail -f helm/restmail/values-dev.yaml
   ```

## Lint and template

```sh
# Lint (production defaults — chart structure check).
helm lint helm/restmail

# Render production values (requires hostname/domain).
helm template restmail helm/restmail \
  --set mailserver.hostname=mx.example.com \
  --set mailserver.domain=example.com

# Render development values (testbed-aware).
helm template restmail helm/restmail -f helm/restmail/values-dev.yaml
```

## Configuration reference

See [values.yaml](values.yaml) for the full set of knobs and inline docs.
The most common knobs at install time:

| Knob | Default | Notes |
|------|---------|-------|
| `mailserver.hostname` | _(required)_ | Public FQDN gateways announce |
| `mailserver.domain` | _(required)_ | Primary mail domain |
| `tls.secretName` | `restmail-tls` | Existing `kubernetes.io/tls` Secret |
| `postgres.existingSecret` | `restmail-postgres` | Set empty to inline creds |
| `api.existingSecret` | `restmail-api` | Set empty to inline creds |
| `api.image.repository` | `ghcr.io/rest-mail/api` | |
| `smtpGateway.service.type` | `LoadBalancer` | `NodePort` for dev |
| `imapGateway.service.type` | `LoadBalancer` | |
| `pop3Gateway.service.type` | `LoadBalancer` | |
| `networking.dnsPolicy` | `ClusterFirst` | Override for testbed |

## What is intentionally not in this chart

- **Webmail front-end.** No `ghcr.io/rest-mail/webmail` upstream image
  exists yet (lives in this repo's `webmail/` directory and ships its own
  `docker-compose.yml`). Once it's published, add a `webmail-deployment.yaml`
  template and a `webmail.*` block in values.
- **Project website.** Tracked separately (Phase 6 of the decomposition).
- **Reference mail daemons.** Postfix/Dovecot/rspamd live in
  `rest-mail/reference-mailserver`. They are not part of the rest-mail
  product surface.
