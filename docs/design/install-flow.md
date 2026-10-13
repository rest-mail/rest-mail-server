# Install and lifecycle: design

Status: agreed in discussion, not yet built. `docs/installing.md` describes what
exists today; this describes what it is changing into, and why. It covers the
install, and the tasks that come after it — upgrade, uninstall, backup, and the
ones for when something has gone wrong.

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

Nothing writes it to a file, because losing it is not a lockout. While the
admin has not chosen its own password, installing again with a new one
replaces it, ends every session the old one opened, and restarts the 72
hours. That is one command and needs nothing kept. It can also be read back —
`pulumi config get <name>:adminPassword`, or `helm get values <name> -n
<namespace> -o json` — but that is a convenience, not what the design rests
on.

What re-running an install must *not* do is change the password by accident.
Someone moving to a new chart version while the admin is still halfway
through setup would otherwise find the password in their hand had stopped
working. So a generated password is generated once: given none, the task
leaves whatever the install already has, and generates only when there is
nothing there. The chart does the same — `api.bootstrapAdminPassword` falls
back to the Secret an earlier install left — which is also what stops an
upgrade demanding a password that, after setup, no longer does anything.

## The whole lifecycle, not just the install

Installing is one act among several, and the rest are the ones people get
wrong at 3am.

Names are **subject first, variant second**: `install:helm` is the install,
of the Helm kind; `password:reset` is the password, the resetting of it. The
first segment says what the task is about, so the families group themselves
in a task list and you look for a forgotten password under `password:`.

The distinction that matters is not in the name but in whether a task has a
`:helm` / `:pulumi` variant at all. Tasks that depend on how you deployed do.
Tasks that talk to a running server do not, because it does not care.

| Depends on how you deployed | Does not |
| --- | --- |
| `install:helm` · `install:pulumi` | `server:verify` |
| `upgrade:helm` · `upgrade:pulumi` | `backup:create` · `backup:restore` |
| `uninstall:helm` · `uninstall:pulumi` | `dns:show` · `dns:check` |
| | `server:offline` · `server:online` |
| | `password:new` · `password:reset` |

Today's `install:password` is misnamed under that rule — it is not a kind of
install — and becomes `password:new`, which also puts it where someone who
has lost one would look.

### upgrade is narrower than install, and that is the point

Not because the mechanism differs — `helm upgrade --install` and `pulumi up`
both converge — but because the *scope* does. A real Helm upgrade is:

```bash
helm upgrade restmail oci://ghcr.io/rest-mail/charts/restmail \
  --version 0.13.0 --namespace rest-mail \
  --set mailserver.hostname=mail.rest-mail.com \
  --set mailserver.domain=rest-mail.com
```

Forget the release name or the namespace and Helm says "release: not found",
loudly, having changed nothing. Forget `mailserver.hostname` and it silently
reverts to the chart's placeholder `mail.example.com`: the gateways announce
that in HELO, the ingress host moves, and the certificate stops matching —
from an upgrade that meant to change only the version. The arguments you
would notice losing are the safe ones.

So `upgrade:*` takes no arguments but the version. It reads back what the
release or stack was installed with, shows the diff, changes the version, and
**refuses** anything that would alter the server's identity. The refusal is
the feature.

```bash
chore upgrade:helm                   # to the newest chart
chore upgrade:helm --version 0.13.0  # to a named one
```

Which is why the chart version lives in stack config rather than in the
pasted code: `upgrade:pulumi` is then `pulumi config set
restmail:chartVersion`, preview, up — and the installer never edits the
user's source. `install:*` must record the version it used, or an upgrade has
nothing to diff against.

### The ones that are not about deployment

- **`backup:create` / `backup:restore`.** A database dump on its own is not a
  backup: the master key encrypts DKIM private keys, ACME certificate keys
  and every admin's authenticator secret, so a dump restored without it gives
  a server that cannot sign outbound mail, renew its certificate, or let an
  admin past 2FA. A backup is the dump *and* the key, which makes the backup
  file itself a secret. It is also what makes `uninstall` and `upgrade` safe,
  so it comes before both.
- **`dns:show` / `dns:check`.** MX, SPF, DKIM, DMARC and a reverse record. The DKIM
  record can only come from the server, which generated the key. A perfectly
  healthy install delivers nothing to Gmail without these, so this is the
  most likely reason an install that passes `server:verify` still "does not
  work". Two halves: print what to publish, check what is published.
- **`server:offline` / `server:online`.** Replicas to zero, and the first
  thing to reach for when something is wrong: every stolen token stops
  working *now*, and you get to work out what happened knowing nobody can act
  while you do. It contains rather than cures — coming back up without having
  rotated anything hands the access straight back — so what belongs between
  the two is a credential rotation, which should be named for what it is
  rather than hidden inside `offline`.
- **`password:reset`.** The only way back for an admin who has
  finished setup and forgotten their password, since the deployment's
  password is ignored from then on. It cannot be a redeploy: that property is
  what stops whoever can read the Secret in a GitOps repo from taking the
  account. It re-arms the bootstrap admin from inside the namespace — a Job,
  not a Secret edit — so cluster rights are required, and the new password
  then behaves like any other: forced change, forced authenticator, 72 hours.
  `restmail reset-admin` does this today with owner keys; removing those
  without this leaves no way back.

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
restmail:chartVersion  0.4.0
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
    version: mail.require("chartVersion"),
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
   however they like and run `server:verify` themselves.

Preview needs the program to build and the provider to reach a cluster. If it
cannot, say which, and what to do, rather than failing a task that did its job.
Re-running skips what is already done and goes to the preview.

## server:verify

Renamed from `install:check`, and not `server:verify`: the checks wanted
after installing are the ones wanted at 3am two months later, and an
install-time name means nobody runs it then. Proves a running install works, layer by layer,
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

## What the server provides

`RESTMAIL_BOOTSTRAP_ADMIN_PASSWORD` (the chart's `api.bootstrapAdminPassword`)
creates the first admin at startup, as a superadmin in setup: it must choose
its own password and enroll an authenticator before its session reaches
anything (`internal/bootstrap`, `setup_required` on login).

- **Signing in spends nothing.** Login with the bootstrap password answers 200
  with `setup_required: ["password", "two_factor"]`; only `POST
  /auth/password` retires it. So `server:verify` proves the password works by
  signing in, checking that list, and logging out — any number of times.
- **It expires** 72 hours after it is first applied. After that the right
  password answers 401 `bootstrap_expired` (a wrong one, the ordinary 401),
  and any session it opened ends — access tokens and refresh alike. Restarts
  do not extend it.
- **Deploying a new one re-arms it** while the admin has not chosen its own:
  the new password replaces the old, every session from the old one ends, and
  the 72 hours start again. That is the recovery for a password that leaked or
  ran out. Once the admin has chosen a password, the deployment's is ignored
  forever, so whoever can edit the Secret cannot reset the account.

Not done, and belonging to the removal of setup codes rather than to this: a
server with no admin and no bootstrap password still falls back to printing a
setup code, where it should refuse to start.

`server:verify` must read the server's state from sign-in, not from `claimed`
in `GET /api/v1/server`: the bootstrap admin exists from the first start, so
an install nobody has touched already reports `claimed: true`.

## Open

1. Does `install:pulumi` write its file **into the project** (refusing if it
   exists), or somewhere neutral for the user to move? Writing a new file is
   not editing their code, but it is the first time an installer touches a
   Pulumi project's source.
2. Neither volume carries `helm.sh/resource-policy: keep`, while both Secrets
   do — so `helm uninstall` today deletes every mailbox and carefully
   preserves the master key that would have decrypted them. Backwards, and a
   live data-loss bug. Keeping the volumes fixes it but leaves an uninstall
   that does not clean up, so the choice is probably: keep them, and have
   `uninstall:*` say what survives and offer to remove it.
3. Does `install:helm` end in `server:verify` like `install:pulumi`, or stay
   thin?
4. Where does a backup go, and in what format? It has to carry the master key
   alongside the dump, so it is a secret, and "write it to ./restmail.dump"
   is not good enough.
