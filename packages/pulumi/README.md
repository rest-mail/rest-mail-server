# REST Mail for Pulumi

`RestMail` is a TypeScript Pulumi component that installs the REST Mail Helm
chart. It can also create the Kubernetes Secret holding an APNs `.p8` key.
It uses the Kubernetes provider supplied by the calling project, so the same
component can target a homelab or another Kubernetes cluster.

## Install the package

This package lives in `rest-mail-server/packages/pulumi`. It is not yet
published to npm. With Node.js 22 or newer, build a local package:

```sh
cd /path/to/rest-mail-server/packages/pulumi
npm ci --ignore-scripts
npm pack
```

Then install that artifact in your Pulumi project:

```sh
npm install /path/to/rest-mail-server/packages/pulumi/rest-mail-pulumi-0.1.0.tgz
```

The project also needs `@pulumi/pulumi` 3.x and `@pulumi/kubernetes` 4.x.
No new Pulumi provider plugin is needed beyond the Kubernetes provider.

## Configure a deployment

For local setup, `chore install:pulumi /path/to/project mail.example.com
--apns-key-file /secure/path/AuthKey.p8 --apns-key-id APPLE_KEY_ID
--apns-team-id APPLE_TEAM_ID` sets the encrypted key and prints the complete
component factory plus its provider wiring. It also prints commands to build
a local package into the project's `vendor/` directory, so no npm publication
is required. The vendor artifact and npm manifests belong with the destination
program. Deployment-pipeline integration is a separate step.


Set the public configuration in the destination project's selected stack.
Choose a published chart version explicitly; the package version and chart
version are independent.

```sh
pulumi config set restmail:hostname mail.example.com
pulumi config set restmail:domain example.com
pulumi config set restmail:namespace rest-mail
pulumi config set restmail:chartVersion CHART_VERSION
pulumi config set restmail:adminPasswordHash BCRYPT_HASH
pulumi config set restmail:ownerKeys 'ssh-ed25519 PUBLIC_OWNER_KEY'
pulumi config set restmail:apnsKeyId APPLE_KEY_ID
pulumi config set restmail:apnsTeamId APPLE_TEAM_ID
pulumi config set --secret --raw restmail:apnsPrivateKey < /secure/path/AuthKey.p8
```

The `.p8` here is the **APNs push-notification key**, not an App Store Connect
upload key. Keep the original file outside the repository. `--secret` encrypts
its contents using the stack's configured secrets provider; `--raw` preserves
the file's trailing newline. Do not put it in an ordinary config value or in
the chart's `values`.

The complete resource declaration is:

```ts
import * as pulumi from "@pulumi/pulumi";
import * as k8s from "@pulumi/kubernetes";
import { RestMail } from "@rest-mail/pulumi";

const config = new pulumi.Config("restmail");
const cluster = new k8s.Provider("mail-cluster", {
    kubeconfig: config.requireSecret("kubeconfig"),
});

const mail = new RestMail("restmail", {
    hostname: config.require("hostname"),
    domain: config.require("domain"),
    namespace: config.require("namespace"),
    chartVersion: config.require("chartVersion"),
    adminPasswordHash: config.require("adminPasswordHash"),
    ownerKeys: config.require("ownerKeys"),
    apns: {
        privateKey: config.requireSecret("apnsPrivateKey"),
        keyId: config.require("apnsKeyId"),
        teamId: config.require("apnsTeamId"),
        topic: "com.antimatterstudios.restmail",
    },
}, { providers: { kubernetes: cluster } });

export const mailUrl = mail.url;
```

Use the provider already declared by a project such as `homelab-server` in
place of creating `mail-cluster`; no kubeconfig copy is needed in that case.
For the standalone example above, set `restmail:kubeconfig` with `pulumi config
set --secret --raw restmail:kubeconfig < /secure/path/kubeconfig`.

Run your project's normal `pulumi preview` and deployment pipeline. Adding
this package to a project does not deploy anything by itself. This repository
does not add a REST Mail declaration to `homelab-server` automatically.

## Resource and secret ownership

- The component creates a namespace by default. `createNamespace: false` uses
  a namespace managed elsewhere. A namespace created here is retained when
  the component is removed, so deleting it cannot defeat Helm's retained-mail
  PVC policy. Namespace and stored-mail destruction remain separate actions.
- The Helm release has a fixed name, an explicit chart version, an optional
  `chart` reference override for existing installer configurations, and
  `skipAwait: true`, matching the existing installer. A successful Pulumi
  update does not prove TLS, DNS or mail readiness: run `chore server:verify`
  from the server repository after deployment.
- Omit `apns` to leave push disabled. With `apns.privateKey`, the component
  forces Pulumi secret handling even if a caller accidentally supplies an
  ordinary string. The key goes only into the Kubernetes Secret's
  `stringData["AuthKey.p8"]`. Helm receives its name, key ID, team ID and topic.
- Rotating a managed key changes the Secret name and the chart's volume
  reference, rolling the API so it reads the new key. The name includes a
  truncated SHA-256 digest, never the key itself. Derived secret values stay
  marked secret in Pulumi. The old Secret is removed after its release
  dependency is updated; use normal rollout/readiness checks after deployment.
- To keep credentials managed elsewhere, pass `apns.existingSecret` and
  optionally `apns.secretKey` instead of `privateKey`. That Secret must exist
  in the same namespace. Rotation and API restart are then its owner's job.
- `values` accepts additional chart settings, such as ingress, storage and
  database configuration. Explicit hostname, domain, bootstrap hash, owner
  keys and APNs arguments take precedence over matching entries in `values`.

Pulumi encryption protects config and state. A Kubernetes Secret is not, by
itself, encryption at rest in the cluster; cluster encryption and access
control still apply. Neither the key nor a readable copy is exported by the
component.

For an existing Helm installation, import its release into Pulumi and adopt
its namespace deliberately before switching ownership. Do not create a
second release over existing mail resources. Helm upgrades, DNS, certificates,
backups and the product's readiness checks retain their normal requirements.

## Validation

```sh
npm ci --ignore-scripts
npm test
npm pack --dry-run
```

The tests use Pulumi's mock monitor, with synthetic credentials and no
cluster access. They check chart settings, provider inheritance, secret
propagation, external credential ownership and key-rotation references.
They do not claim a live install, Apple acceptance or cluster encryption.
