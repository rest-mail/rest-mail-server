# restmail Helm chart

Helm chart for the rest-mail "mail3" stack.

## What this chart deploys

- `postgres` — single-replica StatefulSet running PostgreSQL 16, backing the
  REST API and protocol gateways. Persistent storage via PVC.
- `api` — the Go REST API.
- `js-filter` — the JavaScript filter sidecar consumed by the API. It runs
  each script in its own QuickJS WebAssembly sandbox, only for the API (by
  NetworkPolicy, and with `JS_FILTER_TOKEN` from the API Secret).
- `smtp-gateway` — SMTP listener on 25 (inbound, STARTTLS) and 465
  (submission, implicit TLS).
- `imap-gateway` — IMAP listener on 993 (implicit TLS).
- `pop3-gateway` — POP3 listener on 995 (implicit TLS).
- `webmail` — the mail client in the browser, at `webmail.<mailserver.domain>`.
- `admin` — the admin webmail, at `admin.<mailserver.domain>`.

The gateways are how any ordinary mail client reads and sends mail. There are
no cleartext client ports: 587, 143 and 110 do not exist, and a gateway refuses
to start in production if asked for one.
- A ServiceAccount, a ConfigMap for non-secret API config, an Ingress for each
  web app, and an optional Ingress for the API.

This chart deploys **only** the rest-mail product surface. It does **not** include:

- Postfix, Dovecot, rspamd, ClamAV, fail2ban (the "traditional mailserver"
  stack — see `rest-mail/reference-mailserver`).
- DNS (dnsmasq), TLS issuance (cert-manager / certgen), mail-internet
  simulation (`rest-mail/testbed`).
- The marketing website.

## Deploying the API without the gateways

The REST API is a complete product on its own. The SMTP, IMAP and POP3 gateways
are how traditional clients reach it, and each wants a `LoadBalancer` Service on
a privileged port — which a bare-metal cluster has nothing to hand out for, so
the Services sit pending and nothing explains why.

```sh
helm install restmail helm/restmail \
  --set smtpGateway.enabled=false \
  --set imapGateway.enabled=false \
  --set pop3Gateway.enabled=false
```

That leaves the API, the JS filter and Postgres, and drops each disabled
gateway's workload, Service and NetworkPolicy. All three default to `true`,
so an existing install renders exactly as it did before.

## A gateway endpoint on every eligible node

Each gateway defaults to `workloadKind: Deployment`, with `replicaCount: 1`.
Opt in independently to `DaemonSet` for one pod per eligible node:

```yaml
smtpGateway:
  workloadKind: DaemonSet
imapGateway:
  workloadKind: DaemonSet
pop3Gateway:
  workloadKind: DaemonSet
```

Only `Deployment` and `DaemonSet` are accepted. `replicaCount` applies only to
Deployments. Both kinds reuse the global `nodeSelector`, `tolerations` and
`affinity`, the same hardened pod configuration, and the same Service selectors
and ports. Disabling a gateway removes its workload in either mode.

The gateway Services keep `externalTrafficPolicy: Local` by default, preserving
the original inbound SMTP client IP for SPF and abuse checks. Every node
advertised by the mail hostname's round-robin DNS must have a ready local
endpoint for each advertised protocol, and the load balancer/firewall must
forward its public ports there. Scheduling constraints, taints, capacity and
rollouts can leave a node without a ready endpoint even in DaemonSet mode;
verify coverage before advertising a node. Client submission uses 465, IMAP
993 and POP3 995; port 25 accepts inbound server mail. There is no listener on
587. Changing workload kind replaces the controller and can interrupt gateway
availability during the transition; plan the rollout and verify readiness on
each advertised node. This option does not configure DNS or the load balancer.

## The web apps

The webmail and the admin webmail are each served at the root of a host of
their own, `webmail.<mailserver.domain>` and `admin.<mailserver.domain>` unless
`webmail.ingress.host` / `admin.ingress.host` say otherwise. Each host's Ingress
sends `/` to the app and `/api` to the API. That is not a convenience: the apps
sign in with `SameSite=Strict` cookies and their CSP allows `connect-src 'self'`
only, so the API has to be on the app's own origin. There is no CORS to set up,
and nothing in either image names a host.

A cookie belongs to the host that set it, so signing in on `admin.` does not
sign you in on `webmail.`. That is intended.

TLS is set per app, the same way as the API's (`webmail.ingress.tls.enabled`,
`.secretName`, default `<release>-webmail-tls`), with the ingress class and
annotations alongside. One certificate can cover every host: name the same
Secret in all three.

```sh
helm upgrade restmail helm/restmail --reuse-values \
  --set webmail.ingress.className=traefik \
  --set webmail.ingress.tls.enabled=true \
  --set webmail.ingress.tls.secretName=restmail-tls \
  --set admin.ingress.className=traefik \
  --set admin.ingress.tls.enabled=true \
  --set admin.ingress.tls.secretName=restmail-tls
```

Both are on by default. `webmail.enabled=false` or `admin.enabled=false` drops
that app's Deployment, Service, Ingress and NetworkPolicy, and nothing else.

To let only some addresses reach `admin.`, use your ingress controller's own
mechanism through `admin.ingress.annotations` (a Traefik `ipAllowList`
middleware, ingress-nginx's `whitelist-source-range`). The chart has no
controller-neutral way to say it, and the API is reachable on its own host
either way: the admin API is guarded by sign-in and capabilities, not by
where the request comes from.

The pods run the unprivileged nginx image as uid 101 on a read-only root
filesystem, with all capabilities dropped. They talk to nothing; the browser
reaches the API through the Ingress.

## The first admin

The first admin is the account `admin@<mailserver.domain>`, made from the
password you install with, `api.bootstrapAdminPassword` (16 characters or
more; the chart will not render without one). It is a mailbox like any other,
holding the admin role, and superadmin to switch on when needed.
`chore install:helm` and `chore install:pulumi` make the password and show it
once. Sign in to the rest-mail app as `admin@<mailserver.domain>` with it: you
choose your own password and enroll an authenticator straight away, after
which it stops working. It also expires 72 hours after the server first takes
it.

A server with no admin and no bootstrap password refuses to start: nobody could
ever sign in to it.

`restmail reset-admin` is the way back in for an admin who has lost every other,
and how a server is handed over. It proves you own the server with a key listed
in `api.ownerKeys`.

`restmail status --key` confirms a deploy from outside: the version, commit, and
when the server was built and started. It signs with a key listed in
`api.statusKeys`, which can read that and nothing else, or in `api.ownerKeys`.

## Push notifications

Off by default. To have the server tell the REST Mail iPhone app that mail has
arrived, put the APNs auth key from the Apple Developer account that ships the
app in a Secret, and name it with the key's ids:

```sh
kubectl create secret generic restmail-apns --from-file=AuthKey.p8=AuthKey_ABC123DEFG.p8
helm upgrade restmail ... \
  --set api.push.apns.enabled=true \
  --set api.push.apns.existingSecret=restmail-apns \
  --set api.push.apns.keyId=ABC123DEFG \
  --set api.push.apns.teamId=43UMKXZ8P4
```

The key is mounted from the Secret and never goes in values. A push carries
only the account and its unread count; the app fetches the mail itself.

Or, without the CLI: add a
`BOOTSTRAP_ADMIN_PASSWORD` key (16+ bytes, generated at deploy time) to the
Secret named by `api.existingSecret`, and the API creates a superadmin from it
while no admin exists. That admin must set a new password and enroll a TOTP
authenticator before it can do anything else. The key is optional: without it,
nothing is created. The whole procedure is in
[docs/MANUAL.md §7.9](../../docs/MANUAL.md#79-first-start-of-a-production-install).

## Infrastructure assumptions

The chart runs on any conformant Kubernetes cluster (1.27+) and assumes:

1. **Real DNS.** Pods use `dnsPolicy: ClusterFirst`. In-cluster Service DNS
   resolves peer services (`api` finds `postgres` by Service name).
2. **External TLS Secret.** A Secret of type `kubernetes.io/tls` named
   `restmail-tls` (override via `tls.secretName`), whose `tls.crt` and
   `tls.key` every gateway pod mounts at `/certs`. Populate this with
   cert-manager, external-secrets, or any other operator that produces a
   `kubernetes.io/tls` Secret with `tls.crt` + `tls.key` keys. See
   [Certificates and keys](#certificates-and-keys) for what goes where.
3. **External credentials Secrets.**
   - `restmail-postgres` — keys `POSTGRES_USER`, `POSTGRES_PASSWORD`,
     `POSTGRES_DB`.
   - `restmail-api` — keys `JWT_SECRET`, `MASTER_KEY`, `JS_FILTER_TOKEN`
     (what the API authenticates to the JS filter sidecar with).
4. **LoadBalancer support** for SMTP/IMAP/POP3 Services — typically a cloud
   LB (AWS NLB, GCE LB, MetalLB on bare metal). For kind/k3s without a LB
   driver, override `*.service.type` to `NodePort` (the dev values file
   already does this).
5. **PersistentVolume provisioner.** PostgreSQL uses a PVC. API attachments use
   a PVC by default, or a private S3-compatible bucket with
   `api.attachments.storageType=s3`. See [attachment storage](../../docs/attachment-storage.md)
   before disabling an existing attachment volume.
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
- **Per-container securityContext.** Every workload runs as a non-root user,
  drops all Linux capabilities and adds none back, sets `seccompProfile:
  RuntimeDefault`, and disables privilege escalation, with a read-only root
  filesystem where feasible. The SMTP/IMAP/POP3 gateways run as uid 10001 on a
  read-only root filesystem. They listen on unprivileged ports inside the pod
  (`*.containerPorts`: 10025, 10465, 10993, 10995), and each gateway's Service
  maps the public mail ports (25, 465, 993, 995) onto them, so binding needs no
  capability.
- **No CA private key in any pod.** See
  [Certificates and keys](#certificates-and-keys).
- **No ServiceAccount token.** `automountServiceAccountToken: false` on the
  ServiceAccount and every pod (no workload uses the Kubernetes API).
- **Default-deny NetworkPolicies.** Ingress and egress are denied by default,
  then re-opened least-privilege: DNS, API↔DB, gateway↔API/DB, public
  mail/API/web app ingress, and SMTP outbound delivery (25/465/587) + MTA-STS
  (443). The web apps get no egress beyond DNS.

## Certificates and keys

No pod mounts a CA's private key, and each pod mounts only the keys that are
its own. A pod that could read a CA key could sign certificates its peers
trust: a gateway holding the internal CA's key could make a client
certificate the API's internal mTLS listener accepts, which is the one thing
that listener exists to prevent.

| Pod | Mounts | From |
|-----|--------|------|
| each gateway | `/certs/tls.crt`, `/certs/tls.key` | `tls.secretName`: the mail hosts' certificate, which it serves |
| each gateway | `ca.crt`, `client.crt`, `client.key` under `internalMtls.mountPath` | the internal mTLS Secret: the CA certificate, to verify the API, and the gateways' client keypair |
| API | `ca.crt`, `server.crt`, `server.key` under `internalMtls.mountPath` | the internal mTLS Secret: the CA certificate, to verify gateways, and the API's server keypair |
| API | `/certs/ca.crt`, if the TLS Secret has one | `tls.secretName`: a CA certificate the API trusts on top of the system roots; it gets no mail key |
| PostgreSQL | `tls.crt`, `tls.key` | `postgres.tls.secretName`, or `tls.secretName` |

Every one of those is projected key by key, never as a whole Secret, so a key
your Secret also holds stays out of the pod (cert-manager, for one, keeps a CA
issuer's own key in a Secret).

**The internal mTLS CA.** Unless you bring your own
(`internalMtls.existingSecret`), the chart makes a CA when it is first
installed, signs the API's server certificate and the gateways' client
certificate with it, and keeps only those and the CA's certificate in the
Secret. The CA's private key exists only while the chart renders. Nothing in
the cluster can sign another certificate the API would accept; the price is
that a new certificate means a new CA. The certificates last ten years. To
replace them all, delete the Secret (`<release>-internal-mtls`) and run
`helm upgrade`, then restart the API and the gateways.

If you bring your own, the Secret needs `ca.crt`, `server.crt`, `server.key`,
`client.crt` and `client.key`, and keep the CA's key somewhere else: the chart
never mounts it, but anything that can read the Secret could.

**The mail certificate** comes from you, through `tls.secretName`; the chart
issues nothing for it.

**Development (`chore`).** The testbed issues certificates into its
`testbed_certs` volume, where its CA key lives, and `chore instance:mtls:issue`
issues the internal ones into a volume of their own, where the internal CA key
lives. Only those one-off issuing containers mount either volume. Each service
gets a volume of its own, filled with copies of the files it needs right before
it starts, and owned by the user it runs as; no service mounts either issuing
volume.

## Where the chart is published

Every change under `helm/` on `main` publishes the chart to ghcr, so something
deploying rest-mail names a version rather than keeping a copy that drifts:

```sh
helm install restmail oci://ghcr.io/rest-mail/charts/restmail --version 0.4.0
```

A published version is final — the workflow refuses to overwrite one, because
moving an OCI tag would change what a version means for anyone who already
installed it. Publishing again means bumping `version` in `Chart.yaml`.

`version` and `appVersion` move independently: the first describes these
templates, the second names the images they pull. A chart-only fix bumps the
first alone.

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
     --from-literal=MASTER_KEY="$(openssl rand -base64 32)" \
     --from-literal=JS_FILTER_TOKEN="$(openssl rand -base64 32)"
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
| `*Gateway.containerPorts` | `10025`, `10465`, `10993`, `10995` | What each gateway listens on in its pod; its Service maps the public port onto it |
| `networking.dnsPolicy` | `ClusterFirst` | Override for testbed |
| `db.sslMode` | `require` | libpq sslmode in the DSN; `verify-full` for external DB |
| `db.allowInsecure` | `false` | Acknowledge a cleartext DB link (insecure sslmode only) |
| `postgres.tls.enabled` | `true` | Bundled Postgres serves TLS so `require` connects |
| `api.tlsTerminatedByProxy` | `true` | Acknowledge a TLS-terminating front proxy |
| `networkPolicy.enabled` | `true` | Default-deny + least-privilege policies |
| `webmail.enabled` | `true` | The webmail, with its Ingress |
| `webmail.ingress.host` | `webmail.<mailserver.domain>` | `/` is the app, `/api` the API |
| `webmail.ingress.tls.enabled` | `false` | Secret `<release>-webmail-tls` unless named |
| `admin.enabled` | `true` | The admin webmail, with its Ingress |
| `admin.ingress.host` | `admin.<mailserver.domain>` | `/` is the app, `/api` the API |
| `admin.ingress.tls.enabled` | `false` | Secret `<release>-admin-tls` unless named |
| `serviceAccount.automountServiceAccountToken` | `false` | No workload uses the K8s API |
| `api.push.apns.enabled` | `false` | Push notifications to the iPhone app; needs the three below |
| `api.push.apns.existingSecret` | _(empty)_ | Secret holding the APNs `.p8` key, under `api.push.apns.secretKey` (`AuthKey.p8`) |
| `api.push.apns.keyId` / `teamId` | _(empty)_ | The key's id and its developer team, from the Apple Developer portal |
| `api.push.apns.topic` | `com.antimatterstudios.restmail` | The app's bundle id |

To verify the chart renders and stays hardened, run the infra test:

```sh
helm/restmail/tests/render-test.sh
```

## What is intentionally not in this chart

- **Project website.** Tracked separately (Phase 6 of the decomposition).
- **Reference mail daemons.** Postfix/Dovecot/rspamd live in
  `rest-mail/reference-mailserver`. They are not part of the rest-mail
  product surface.
