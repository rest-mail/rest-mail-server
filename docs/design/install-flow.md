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

A namespace earns its keep two ways, and only one of them is size. It groups
a family big enough to need grouping — or its first segment is the subject
that makes the verb mean anything at all. `sessions:revoke` is a namespace of
one and still right, because `revoke` alone says nothing; the same goes for
`show`, `create`, `reset`. What does *not* earn it is a category label: the
earlier `server:backup` and `server:dns` used `server:` to mean "things to do
with the server", a bucket rather than a subject, and buckets grow until the
prefix is on everything and distinguishes nothing.

`server:` is a subject like any other, not a catch-all. The repo already
names tasks `api:logs`, `postgres:logs`, `webmail:restart` — one segment per
component — and the deployed server is simply the component that is not on
this machine. Which is also why `password:reset` does not take the prefix: a
password is not a component of the deployment the way its logs are.

The subject is what a task **acts on**, never what it touches to get there.
`password:reset` runs as a Job in the server's namespace, but it is still the
password that is being reset, so it does not become `server:password:reset`
— by that argument everything here would take a `server:` prefix, since
verify talks to the server, `dns:check` reads the DKIM key out of it, and
`backup:create` dumps its database. Read the name as "the X, Y-ed" and it
usually settles itself.

The distinction that matters is not in the name but in whether a task has a
`:helm` / `:pulumi` variant at all. Tasks that depend on how you deployed do.
Tasks that talk to a running server do not, because it does not care.

| Depends on how you deployed | Does not |
| --- | --- |
| `install:helm` · `install:pulumi` | `server:verify` |
| `upgrade:helm` · `upgrade:pulumi` | `backup:create` · `backup:database` · `backup:keys` · `backup:restore` |
| `uninstall:helm` · `uninstall:pulumi` | `dns:show` · `dns:check` |
| | `server:offline` · `server:online` |
| | `password:new` · `password:reset` |
| | `server:status` · `server:logs` |
| | `sessions:revoke` |

Today's `install:password` is misnamed under that rule — it is not a kind of
install — and becomes `password:new`.

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

### uninstall is the one that has to be awkward

Both Secrets carry `helm.sh/resource-policy: keep` and neither volume does,
so `helm uninstall` today deletes every mailbox and carefully preserves the
master key that would have decrypted them. Backwards. **The volumes get the
same annotation**, which makes an uninstall survivable: the deployment goes,
the data stays, and a reinstall under the same name finds it again.

That leaves deleting the data as a separate, genuinely irreversible act, and
**that** is where the awkwardness belongs — not on the uninstall itself. A
gate on the reversible step is one people learn to grind through, which
trains them to grind through the one that matters.

So removing the data asks you to type a code it has just generated, and:

- **`--yes` does not answer it.** Whatever answers an ordinary confirm must
  not answer this one, or a blanket `-y` added for some other task walks
  straight through.
- **The answer is read from the terminal**, not from stdin, so `yes |` and
  `echo CODE |` both fail. Printing the code to stderr alone would not be
  enough — `2>&1` captures it in a moment.
- **Automating it stays possible, by name.** Anyone who genuinely needs it
  passes an explicit flag that says so. The point is not that it cannot be
  done, but that it cannot be done *by accident*.

Whatever survives is said plainly at the end, with the command to remove it
later — otherwise the volumes become orphans nobody can account for, and
someone eventually deletes them with `kubectl`, having read none of this.

### The ones that are not about deployment

- **`backup:create` and its subsets.** `backup:create` takes everything
  needed to stand the server up again somewhere else; `backup:database` and
  `backup:keys` take one part each, for when you know what you want. Subsets
  are separate tasks rather than flags on one, because a task list is
  something you can read in an emergency and a `--only` flag is not.

  Four things, and a dump is only the first:

  | | Why losing it hurts |
  | --- | --- |
  | the database | mail, accounts, filters |
  | the master key | it encrypts the DKIM private keys, the ACME certificate keys and every admin's authenticator secret — restore a dump without it and the server cannot sign outbound mail, cannot renew its certificate, and no admin gets past 2FA |
  | the attachments volume | attachments live on their own PVC mounted at `/attachments`, not in the database, so a dump alone restores mail with the attachments missing |
  | the install's own facts | hostname, domain, namespace, chart version — what you need to deploy it again before you can restore into it |

  Carrying the key makes the backup file a secret in its own right, which is
  why "write it to ./restmail.dump" is not good enough. The JWT secret is
  deliberately *not* in the set: losing it only signs everyone out, and
  keeping it would mean a restored server honouring sessions minted before
  whatever went wrong.

  Backup is also what makes `uninstall` and `upgrade` safe, so it comes
  before both.
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
- **`server:status` / `server:logs`.** What `server:verify` does not cover:
  not "does it work" but "what have I got" — chart and product version,
  release revision, replicas, how long it has been up — and then the logs of
  whichever component you are suspicious of, named as the local tasks already
  name them (`api`, `smtp`, `imap`, `pop3`, `postgres`). Status is also what
  tells you which revision an upgrade moved you off.
- **`sessions:revoke`.** Sign everybody out, which is what makes
  `server:online` safe to run after an incident. It needs **both halves**,
  and the server has neither today:

  - *Access tokens.* An admin's is checked against `credential_epoch` on
    every request, so bumping every admin's epoch ends them at once. A
    mailbox user's is not checked against anything — there is no epoch on
    that side — so it stays good until it expires. The only lever that
    reaches every token immediately is rotating `JWT_SECRET`, which breaks
    every signature at once. That needs a deploy, since it is read at
    startup, and the chart carries the old value forward unless a new one is
    set explicitly.
  - *Refresh tokens.* They are rows in the database, so a new `JWT_SECRET`
    does not touch them. Rotate the secret and nothing else, and every
    client quietly refreshes into a valid new access token under the new
    secret — nobody is signed out at all. This is the same shape as the bug
    found in the bootstrap re-arm: ending access without revoking refresh
    ends nothing. `RevokeAllForSubject` exists; the global revoke does not.

  Everything else an incident wants is beyond sessions and not yet designed:
  mailbox app passwords for IMAP and SMTP are separate credentials that a
  session revoke does not touch, a leaked DKIM key needs rotating and
  republishing, and rotating the master key means re-encrypting everything
  it protects.
- **`password:new` is not a small `password:reset`.** It acts on nothing:
  there is no server yet, so it is not resetting anything, it is making a
  random string that the server will later accept. `password:reset` acts on
  a running system and installs the result. They share a namespace because
  they answer the same need at different times — a password for a server
  that does not exist yet, and one for a server that does — but they are not
  the same act and neither should be built out of the other.

  It is thin enough to question, since `openssl rand -base64 24` is one
  line. It earns its place by being the one generator guaranteed to satisfy
  the server's own rules: long enough for the chart's check, and made of
  characters that survive being carried through a shell.
- **`password:reset` cannot be an emailed link.** On a mail server that is
  circular: the usual recovery path runs through the thing that is broken.
  It is also the reason the reset is a Job in the namespace — the recovery
  for a mail server must not need mail, or DNS, or a certificate.

  It is the only way back for an admin who has finished setup and forgotten
  their password, since the deployment's
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
3. ask whether to print the code or write it: *"Write it to a file, or print
   it for you to paste? [write/print]"*. Printing touches nothing. Writing
   asks for the filename, offering the default for the project's runtime
   (`restmail.ts`, `restmail.py`, `restmail.go`), and refuses to overwrite
   something already there
4. either way, print the one line that wires it in — `import "./restmail";`,
   `import restmail`, or a call in Go; YAML pastes into `Pulumi.yaml` — and
   block on *"Added it to your program? [y/N]"*. Writing the file does not
   skip this: adding that line is editing their program, which an installer
   does not do, so the step remains theirs whichever path they took. The
   answer is never trusted, only waited on
5. `pulumi preview`, and match what it will create against what we set: a
   `kubernetes:helm.sh/v3:Release` named `<name>`, reading our hostname. This
   is the only thing that proves the config and the code found each other —
   wrong stack, wrong config namespace, wrong project all show up here instead
   of as a server nobody can sign in to
6. *"Run pulumi up to install? [y/N]"* — yes runs `pulumi up --target` on our
   release only, never the whole stack, then verify. No leaves them to deploy
   however they like and run `server:verify` themselves.

Both paths end at the same preview, which is the point: the file being
written proves nothing about whether it was wired in, and the preview proves
it either way.

Preview needs the program to build and the provider to reach a cluster. If it
cannot, say which, and what to do, rather than failing a task that did its job.
Re-running skips what is already done and goes to the preview.

## server:verify

Renamed from `install:check`, and not `install:verify`. Not only because an
install-time name means nobody thinks to run it two months later, but because
after `upgrade:pulumi` the name would be **false**: it would claim an install
happened when one did not. A task name that lies about what just happened is
worse than one that is merely under-used.

**Every `install:*` and `upgrade:*` ends in it**, whichever tool they used,
and it is equally the thing you run on a Tuesday to see that everything is
still green. That makes it the most-seen output in the whole surface, so it
is worth more care than anything else here.

It works layer by layer, stopping at the first failure, and says **whose**
failure it is:

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

Its facts come from whichever is to hand. Run at the end of an install or an
upgrade, it is **handed** them — the task that called it just set the
password and knows the host, stack and namespace. Run on its own, later, from
any machine, it reads them back from the stack or the release. Resource names come from the
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

## What chore needs first

Two tasks here want input that is not yes-or-no, which `confirm:` cannot
give: a filename for `install:pulumi`, and the typed code that guards
deleting a server's data. So chore needs an `ask:` that captures an arbitrary
string from the terminal, with a default, and — for the deletion gate — one
that `--yes` does not answer and a pipe cannot feed. Neither task can be
built as designed until it exists.

## Open

1. Where does a backup go? It carries the master key, so it is a secret, and
   a default of "the current directory" puts one in a git repo sooner or
   later.
2. When `server:verify` fails at the end of an `upgrade:*`, does the upgrade
   offer to put it back? `helm rollback` makes that one command; Pulumi has
   no equivalent, so it would mean setting `chartVersion` back and running
   up again. Tempting, but an automatic rollback after a failed migration
   can be worse than the failure.
3. `backup:create` is cheap for the database and the keys and potentially
   enormous for the attachments volume. Does the default include it?
