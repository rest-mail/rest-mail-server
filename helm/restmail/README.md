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
6. **NetworkPolicy-enforcing CNI** (Calico, Cilium, Weave, …). The chart ships
   default-deny NetworkPolicies. On a cluster whose CNI does not enforce them,
   set `networkPolicy.enabled=false` (the policies are then simply inert, but
   disabling them documents the intent).

## Security posture

The chart is hardened by default and is deployable in its declared
`environment: production` mode out of the box:

- **Production boot validator satisfied.** The API and gateways run the
  secure-by-construction boot checks. The chart wires their acknowledgement
  inputs: `API_TLS_TERMINATED_BY_PROXY=true` (front the API with a
  TLS-terminating proxy/ingress) and a secure `DB_SSLMODE` (default `require`).
  The bundled PostgreSQL serves TLS (`postgres.tls.enabled`, on by default) so
  `require` connects end-to-end. Point at an external managed database with
  `postgres.enabled=false` and `db.sslMode=verify-full` for the strongest
  posture, or acknowledge an in-cluster cleartext link with
  `db.sslMode=disable` + `db.allowInsecure=true`.
- **Per-container securityContext.** Every workload drops all Linux
  capabilities, sets `seccompProfile: RuntimeDefault`, and disables privilege
  escalation. The API, js-filter and PostgreSQL run as non-root with read-only
  root filesystems where feasible. The SMTP/IMAP/POP3 gateways bind privileged
  ports (<1024); their images ship no file capabilities, so they keep a root
  UID with **only** `NET_BIND_SERVICE` re-added — everything else is dropped.
- **No ServiceAccount token.** `automountServiceAccountToken: false` on the
  ServiceAccount and every pod (no workload uses the Kubernetes API).
- **Default-deny NetworkPolicies.** Ingress and egress are denied by default,
  then re-opened least-privilege: DNS, API↔DB, gateway↔API/DB, public
  mail/API ingress, and SMTP outbound delivery (25/465/587) + MTA-STS (443).

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
| `db.sslMode` | `require` | libpq sslmode in the DSN; `verify-full` for external DB |
| `db.allowInsecure` | `false` | Acknowledge a cleartext DB link (insecure sslmode only) |
| `postgres.tls.enabled` | `true` | Bundled Postgres serves TLS so `require` connects |
| `api.tlsTerminatedByProxy` | `true` | Acknowledge a TLS-terminating front proxy |
| `networkPolicy.enabled` | `true` | Default-deny + least-privilege policies |
| `serviceAccount.automountServiceAccountToken` | `false` | No workload uses the K8s API |

To verify the chart renders and stays hardened, run the infra test:

```sh
helm/restmail/tests/render-test.sh
```

## What is intentionally not in this chart

- **Webmail front-end.** No `ghcr.io/rest-mail/webmail` upstream image
  exists yet (lives in this repo's `webmail/` directory and ships its own
  `docker-compose.yml`). Once it's published, add a `webmail-deployment.yaml`
  template and a `webmail.*` block in values.
- **Project website.** Tracked separately (Phase 6 of the decomposition).
- **Reference mail daemons.** Postfix/Dovecot/rspamd live in
  `rest-mail/reference-mailserver`. They are not part of the rest-mail
  product surface.
