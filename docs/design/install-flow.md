# Install flow: design

Status: agreed in discussion, not yet built. `docs/installing.md` describes what
exists today; this describes what it is changing into, and why.

The install is four steps, whichever way you install:

1. pick a method — Helm, or Pulumi
2. install
3. verify
4. sign in with the rest-mail app as `admin`, with the bootstrap password

## The password is made by the installer, not by you

The chart refuses to install without `api.bootstrapAdminPassword` and will not
invent one: a chart runs in a cluster with no human and nowhere to put a
secret anyone could read back. That stays.

The *installer* is different — it is a person at a terminal. So:

- **No password given, a terminal attached:** the install task generates one,
  uses it, and prints it once, with how to get it back.
- **No password given, no terminal** (a pipeline): refuse. A generated password
  printed to stdout is a password in a log.
- **`--password`:** use theirs, for anyone whose password comes from a manager
  or a policy.

It is never passed as a positional argument, so it never lands in shell
history. `install:password` stays only for flows with no install to hang it
off — `gh secret set`, or handing a password to whoever installs later. It is
no longer a step.

The password is always retrievable from where the install put it, so nothing
writes it to a file:

| Installed with | Kept in | Read back with |
| --- | --- | --- |
| Pulumi | the stack config, encrypted | `pulumi config get <name>:adminPassword` |
| Helm | the release's values | `helm get values <name> -n <namespace> -o json` |

## install:helm does the install; install:pulumi prepares a project

They share the mail server's facts and nothing about where it goes.

| Facts (both) | `install:helm` | `install:pulumi` |
| --- | --- | --- |
| `host` — `mail.example.com` | the kubeconfig's cluster | `project` — a directory |
| `--domain` — derived from host | `--namespace` | `--stack` |
| `--name` — default `restmail` | | |
| `--password` — generated if omitted | | |

`install:helm` becomes `helm upgrade --install`, so running it twice changes
the install instead of failing with "cannot re-use a name".

## install:pulumi: chore owns the config, you own the code

The only contract between the two halves is a set of config keys.

`install:pulumi` puts every per-install fact into the stack's config, under the
mail server's name as the config namespace:

```
restmail:hostname      mail.example.com
restmail:domain        example.com
restmail:namespace     rest-mail
restmail:adminPassword [secret]
```

So the code is **the same for every install**, can be published in the docs,
and differs between `dev` and `prod` only through `Pulumi.<stack>.yaml`, like
everything else in a Pulumi project. `--name mail-eu` sets `mail-eu:hostname`
and the code reads `new pulumi.Config("mail-eu")`, so two mail servers in one
stack cannot collide.

```ts
const mail = new pulumi.Config("restmail");

new k8s.helm.v3.Release("restmail", {
    name: "restmail",
    chart: "oci://ghcr.io/rest-mail/charts/restmail",
    version: "0.4.0",
    namespace: mail.require("namespace"),
    createNamespace: true,
    skipAwait: true,
    values: {
        mailserver: { hostname: mail.require("hostname"), domain: mail.require("domain") },
        api:        { bootstrapAdminPassword: mail.requireSecret("adminPassword") },
    },
});
```

The flow:

1. preconditions; a confirm showing project, stack, host and the domain it
   worked out
2. set the config, generating the password if none was given
3. write the code as a new file, and print the one line that wires it in
   (`import "./restmail";`, `import restmail`, or a call in Go; YAML pastes
   into `Pulumi.yaml`)
4. block: *"Added it to your program? [y/N]"* — the answer is never trusted,
   only waited on
5. `pulumi preview`, and match what it will create against what we set: a
   `kubernetes:helm.sh/v3:Release` named `<name>`, reading our hostname. This
   is the only thing that proves the config and the code found each other —
   wrong stack, wrong config namespace, wrong project all show up here instead
   of as a server nobody can sign in to
6. *"Run pulumi up to install? [y/N]"* — yes runs `pulumi up --target` on our
   release only, never the whole stack, then verify. No leaves them to deploy
   however they like and run `install:verify` themselves.

Preview needs the program to build and the provider to reach a cluster. If it
cannot, say which, and what to do, rather than failing a task that did its job.
Re-running skips what is already done and goes to the preview.

## install:verify

Renamed from `install:check`. Proves a running install works, layer by layer,
stopping at the first failure, and saying **whose** failure it is:

```
  resources created     ok   Deployment, Service, Secret, Ingress
  pods ready            ok   2/2
  answers in-cluster    ok   via port-forward
  admin password works  ok   sign-in accepted, onboarding pending
  answers publicly      ok   https://mail.example.com, certificate valid
```

In-cluster comes before public on purpose: passing in-cluster and failing in
public means "the server is fine; DNS, ingress or the certificate is not", and
must read that way — a check that goes red for someone else's DNS gets ignored.

Its facts come from whichever is to hand. Run by an install task at the end,
it is **handed** them — the installer just set the password and knows the
host, stack and namespace. Run on its own, later, from any machine, it reads
them back from the stack or the release. Resource names come from the
release's own outputs rather than being assumed.

## What this needs from the server

The `admin password works` layer cannot pass until the server handles
`BOOTSTRAP_ADMIN_PASSWORD`. That work must include:

- **A way to prove the bootstrap password is correct without spending it.**
  Verify signs in with it, and may run any number of times; if that started
  onboarding, forced the password change, or counted as first use, verifying
  an install would break it. The answer wanted is *"that is the bootstrap
  credential, and onboarding is still pending"*, with nothing consumed.
- the forced password change and authenticator enrollment on real first use
- expiry 72 hours after first start
- refusing to start with no admin and no bootstrap password

## Open

1. Does `install:pulumi` write its file **into the project** (refusing if it
   exists), or somewhere neutral for the user to move? Writing a new file is
   not editing their code, but it is the first time an installer touches a
   Pulumi project's source.
2. Chart version in the code (a reviewed diff) or in config (a `pulumi config
   set`)? Leaning code: a mail server's version is not an environment setting.
3. Does `install:helm` verify at the end like `install:pulumi`, or stay thin?
