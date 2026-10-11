# Installing rest-mail

Four steps, whichever way you install:

1. **Pick a method** — Helm, or Pulumi.
2. **Make an admin password.** `chore install:password`, or
   `openssl rand -base64 24`. Keep what it prints.
3. **Install**, giving it that password.
4. **Sign in** with the rest-mail app as `admin`, with that password.

## Why you have to make the password yourself

Step 3 cannot do it for you, and the chart deliberately will not try.

A generated password would be one nobody could get at afterwards. The only
places a chart can put it are a Kubernetes Secret (you would need cluster
access to read it — the thing an admin may not have yet), a pipeline log (the
worst possible place), or the terminal of whoever ran `helm install` (nobody,
when a pipeline ran it). Whoever installs has somewhere to keep a secret. A
chart does not.

So the chart **refuses to install without one**:

```
$ helm install restmail oci://ghcr.io/rest-mail/charts/restmail
Error: api.bootstrapAdminPassword is required: without it the server has no
admin and nobody can sign in. ...
```

That password is the only way into a server nobody has signed in to yet. Using
it forces you to choose your own password and enroll an authenticator, after
which it is of no further use — and it **expires 72 hours after the server
first starts**, so an abandoned install does not sit on the internet waiting to
be claimed by whoever finds the password later.

## Helm

```bash
chore install:password          # keep what it prints

chore install:helm mail.example.com example.com "$PASSWORD"
```

`chore install:helm` checks helm, kubectl and that a cluster is reachable,
then shows you **which cluster** and asks before installing anything. That
question is not about typos in the hostname; it is about installing a mail
server into whatever cluster your kubeconfig happened to be pointing at, which
nobody checks and everybody assumes.

It is a thin wrapper. The command it runs is:

```bash
helm install restmail oci://ghcr.io/rest-mail/charts/restmail \
  --namespace rest-mail --create-namespace \
  --set mailserver.hostname=mail.example.com \
  --set mailserver.domain=example.com \
  --set api.bootstrapAdminPassword="$PASSWORD"
```

Nothing requires chore. Type that instead if you prefer.

## Pulumi

**There is no `chore install:pulumi`, and there cannot be one.** Two reasons,
and the second is the interesting one:

- A chore task runs in *its own* taskfile's directory — this repository. Your
  Pulumi project is somewhere else, so the task would look for your
  `Pulumi.yaml` here and never find it.
- A Pulumi project is a **program**, in one of six languages, in files this
  repository has never seen. Adding a resource to it means editing your source,
  which is not something a task runner can do to a program it has never read.

What *is* portable is the **config key**. Every language reads it the same way,
so this is the contract:

```bash
pulumi config set --secret restmail:bootstrapAdminPassword "$PASSWORD"
```

Then declare the release yourself. In Pulumi YAML:

```yaml
resources:
  restmail:
    type: kubernetes:helm.sh/v3:Release
    properties:
      name: restmail
      chart: oci://ghcr.io/rest-mail/charts/restmail
      namespace: rest-mail
      createNamespace: true
      values:
        mailserver:
          hostname: ${hostname}
          domain: ${domain}
        api:
          bootstrapAdminPassword: ${restmail:bootstrapAdminPassword}
```

In TypeScript, the same config key through `new pulumi.Config("restmail")`:

```ts
const cfg = new pulumi.Config("restmail");
new k8s.helm.v3.Release("restmail", {
  name: "restmail",
  chart: "oci://ghcr.io/rest-mail/charts/restmail",
  namespace: "rest-mail",
  createNamespace: true,
  values: {
    mailserver: { hostname: cfg.require("hostname"), domain: cfg.require("domain") },
    api: { bootstrapAdminPassword: cfg.requireSecret("bootstrapAdminPassword") },
  },
});
```

Three things that bite, all found by actually doing it:

- **An OCI chart goes whole into `chart:`.** Splitting it the way you would an
  HTTP repository — `chart: restmail` with `repositoryOpts.repo:
  oci://ghcr.io/rest-mail/charts` — fails with `is not a valid chart repository
  or cannot be reached`, which reads like a network problem and is not one.

- **Set `name:` explicitly.** Without it Pulumi auto-names the release
  `restmail-df5d9627`, and every resource in it inherits that prefix — so the
  Secret you go looking for is not called what the documentation says.
- **`Release` waits for readiness; `helm install` does not.** Every pod in this
  chart mounts the TLS Secret and will not start until it exists, so a
  `pulumi up` into a cluster where cert-manager has not yet issued one fails
  after five minutes with `context deadline exceeded` — with the release
  created and perfectly correct. Either create the TLS Secret first, or set
  `skipAwait: true` and watch the pods yourself — with it, the install above
  takes five seconds.

## Which cluster, and from where

The password has to reach the install, and where it is kept is your decision,
not the chart's:

| Installing from | Where the password lives |
| --- | --- |
| your own terminal | your password manager |
| a Pulumi stack | `pulumi config set --secret`, encrypted in the stack |
| a GitHub pipeline | `gh secret set RESTMAIL_ADMIN_PASSWORD` |

Never a public repository, and never a log. If it leaks before you have signed
in once, install again with a new one — before the 72 hours are up, that is the
whole of the exposure.
