# Install and lifecycle: design

Status: built — `tasks/*.yml` and `tasks/lifecycle/`, run end to end against a
cluster through Helm and through Pulumi. `docs/installing.md` is the user's
guide; this is why each task is shaped the way it is. Where the build added to
or differs from what was agreed, it says so under "Built, and where it differs". It covers the
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
| `uninstall:helm` · `uninstall:pulumi` | `data:destroy` |
| | `dns:show` · `dns:check` |
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

It does **not** roll back on its own when `server:verify` fails afterwards.
A schema migration may already have run, and putting the old code back under
a migrated database can be worse than the failure it is reacting to. The
task cannot know whether that migration was reversible; the person can. So
it says what failed, prints the exact command to go back — `helm rollback`,
or `chartVersion` set back and up again — and says whether a backup exists
and how old it is. Deciding is theirs, and by then they are in territory
that needs them to be.

Which is why the useful automation sits *before* the upgrade, not after it:
the moment "you should have backups" is actionable is while nothing has
happened yet. `upgrade:*` looks for a recent backup first and offers
`backup:create` if there is none. Afterwards, that advice is only a reproach.

Which is why the chart version lives in stack config rather than in the
pasted code: `upgrade:pulumi` is then `pulumi config set
restmail:chartVersion`, preview, up — and the installer never edits the
user's source. `install:*` must record the version it used, or an upgrade has
nothing to diff against.

### uninstall never deletes data

Uninstalling a program does not delete what you made with it — removing
Office leaves your documents where they were. Uninstalling rest-mail is the
same: it removes the software, and the mail stays.

Today it does not. Both Secrets carry `helm.sh/resource-policy: keep` and
neither volume does, so `helm uninstall` deletes every mailbox and carefully
preserves the master key that would have decrypted them. Backwards. **The
volumes get the same annotation**: the deployment goes, the data stays, and
a reinstall under the same name finds it again.

Deleting the data is then a separate, genuinely irreversible act, and
**that** is where the awkwardness belongs — never on the uninstall. A gate
on the reversible step is one people learn to grind through, which trains
them to grind through the one that matters.

It is a separate **task**, `data:destroy`, and never a stage or an option
of either uninstall. What survives an uninstall is the same however it was
uninstalled — volumes and Secrets carrying `keep`, in a namespace — and
removing them is the same `kubectl` work either way, so it does not depend
on how you deployed and belongs in the right-hand column. The gate then
lives in exactly one place, and neither `uninstall:*` has one.

`data:destroy` asks you to type a code it has just generated, and:

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

**`uninstall:pulumi` cannot uninstall anything; it can only confirm that
you did.** The program is the source of truth, and any removal that is not
reflected in the program is undone by the next `pulumi up` — for an
unrelated change, by someone who has forgotten. That rules out
`pulumi destroy --target <urn>`, and it rules out plain `pulumi destroy`
too, even when rest-mail is the whole stack: the program still declares it,
so it comes back. Throwing away the whole project is `pulumi destroy` and
`pulumi stack rm`, which are Pulumi's commands and need nothing from
rest-mail to run.

So it is `install:pulumi` in reverse:

1. print what to remove — the `Release`, and **not** the namespace if the
   program declares one as a resource, because deleting a namespace deletes
   everything in it, `keep` or no `keep`
2. wait for the person to say they have removed it and run `pulumi up`
3. check: the release is gone from the stack and the cluster, and say
   plainly what survived — the volumes and Secrets that `keep` held back —
   with `data:destroy` as the command for them

Step 3 is also the task's one real safeguard. The `keep` annotation is
honoured because `kubernetes:helm.sh/v3:Release` uninstalls through Helm;
a program that used `helm.v3.Chart` instead renders the chart into plain
Pulumi resources and deletes every one, annotation or not. Checking what
survived is how that shows up as a loud warning rather than as an empty
volume list nobody looks at.

Which leaves the two Pulumi tasks honestly different from their Helm
counterparts: Helm's release is the unit of installation, so chore can act
on it; Pulumi's is the program, so chore can only describe and check.

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

  Carrying the key makes the backup file a secret in its own right, so there
  is **no default destination**. `--to` says where; otherwise the install's
  own config does, through a `backupTarget` the stack or the release can
  declare once — a stack that already has somewhere to put things says so
  there, and nobody types it again. With neither, it refuses and names the
  flag, the way the install refuses to generate a password into a pipeline.

  A default of "the current directory" would put a master key in a git repo
  eventually, so it also refuses a destination inside a git working tree
  unless git is already ignoring it. That stops the accident without
  stopping anyone who means it. The JWT secret is
  deliberately *not* in the set: losing it only signs everyone out, and
  keeping it would mean a restored server honouring sessions minted before
  whatever went wrong.

  Backup is also what makes `data:destroy` and `upgrade` safe, so it comes
  before both.

  **The default leaves the attachments out**, and says so every time. Not
  because of the size alone but because the mechanism differs: the database
  is a `pg_dump`, the key is a secret read, the install's facts are a few
  strings — all of them seconds, all of them a file you can hold. The
  attachments are a PVC that the chart sizes at 20Gi and the cluster's
  default storage class backs, which on k3s is `local-path`, so there is no
  volume snapshot to take; copying them means streaming the lot through the
  cluster. A backup whose contents change with how full the disk is would be
  a trap, so the default is fixed and the cost is paid by naming it: the
  task measures the volume, prints what it is declining to take and how
  large it is, and prints the command that would take it.

  That makes `backup:restore`'s job the matching one — it reads a manifest
  saying what the file actually contains and refuses to restore quietly over
  a gap. Restoring a database-only backup gives a working server with mail
  whose attachments are gone, which is recoverable if you knew and a very
  bad surprise if you did not.
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
- **`server:status` / `server:logs`.** Status and verify look like the same
  task and are not, for one reason: **status has to work when the server is
  broken.** Verify signs in, port-forwards and fetches the public URL, so a
  server that is down makes it go red without saying anything you did not
  already know. Status is a passive read, so it still prints "2 replicas, 0
  ready, revision 4, upgraded eleven minutes ago" — which is what you need at
  exactly that moment. One says *that* it is broken; the other says *what you
  have*. Verify also fails on purpose, being the gate at the end of every
  install and upgrade, and a gate that always passes is not one.

  So that nobody has to run both, **`server:verify` prints the status block
  first**, then runs its layers. Verify is everything; status is the cheap
  part you can still get when things are on fire.

  What status covers that verify does not:
  not "does it work" but "what have I got" — chart and product version,
  release revision, replicas, how long it has been up — and then the logs of
  whichever component you are suspicious of, named as the local tasks already
  name them (`api`, `smtp`, `imap`, `pop3`, `postgres`). Status is also what
  tells you which revision an upgrade moved you off.
- **`sessions:revoke`.** Sign everybody out, which is what makes
  `server:online` safe to run after an incident. Two token types have to end,
  and they end differently:

  - *Access tokens.* Fifteen minutes by default (`JWT_ACCESS_EXPIRY`), and
    stateless. An admin's is additionally checked against `credential_epoch`
    on every request, so bumping every admin's epoch ends those at once. A
    mailbox user's is checked against nothing but its signature and expiry,
    so it stays good for the rest of its fifteen minutes. That is the floor
    on how fast any revocation can bite, and it is a floor rather than a
    problem.
  - *Refresh tokens.* Seven days by default (`JWT_REFRESH_EXPIRY`), and
    doubly held: each one is a JWT signed with the same secret **and** a row
    in `refresh_tokens`. Both halves have to pass, so either half can end a
    session. Revoking the row ends it at the next refresh; rotating
    `JWT_SECRET` ends it immediately, because the refresh token's own
    signature stops verifying — the secret rotation is a complete sign-out,
    not a partial one. It needs a deploy, though, since the secret is read
    at startup and the chart carries the old value forward unless a new one
    is set explicitly, so the task's normal path is the database:
    `RevokeAllForSubject` exists, a global revoke across all subjects does
    not, and that plus an epoch bump signs out every admin at once and every
    mailbox within the access window.

  The two steps are ordered, and the order is not a style choice. Revoke the
  refresh rows **first**, then bump the epochs. Dead rows cannot mint
  anything, so nothing can be issued behind the sweep. The other way round
  leaves a gap: bumping the epoch kills an admin's access token, their
  client sees the 401 and refreshes, and the refresh re-mints at the
  *current* epoch rather than checking the one it was presented with — so
  they land back inside with a fresh pair before the sweep reaches them.
  That is the same failure as ending access without ending refresh, only
  with a smaller window.

  Shortening the refresh window does not substitute for any of this. Refresh
  is already **single-use**: every refresh rotates the row `active→rotated`
  and issues a replacement, so a token is spent in minutes of use, not days.
  The seven days is the *idle* bound — how long you may be away and still
  come back — and shortening it only signs out people who were not doing
  anything. What it would not fix is that the window **slides**: each
  rotation mints a fresh seven days, so an active session never ends on its
  own. Bounding that takes an absolute cap carried forward through rotation,
  which is a different field and a separate decision from the idle window.

  There is a cheaper win in the same place. Single-use rotation means a
  stolen refresh token is *detected*: whoever presents it second loses, and
  the loser may well be the real owner. Today that is a 401 and nothing
  else — the loser signs in again and the thief keeps the session. Standard
  practice is to treat a spent token presented again as proof of theft and
  revoke the whole subject, which turns an existing signal into a response
  and is a handful of lines in the path that already distinguishes the case.

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
3. print the code for the project's language, and where it goes — a new
   file plus the one line that wires it in (`import "./restmail";`, `import
   restmail`, a call in Go), or straight into `Pulumi.yaml` for YAML
4. block on *"Added it to your program? [y/N]"*. The answer is never
   trusted, only waited on
5. `pulumi preview`, and match what it will create against what we set: a
   `kubernetes:helm.sh/v3:Release` named `<name>`, reading our hostname. This
   is the only thing that proves the config and the code found each other —
   wrong stack, wrong config namespace, wrong project all show up here instead
   of as a server nobody can sign in to
6. *"Run pulumi up to install? [y/N]"* — yes runs `pulumi up --target` on our
   release only, never the whole stack, then verify. No leaves them to deploy
   however they like and run `server:verify` themselves.

**Nothing of theirs is written to.** An earlier version of this offered to
write the file and have them add only the wiring line. It was dropped: the
wiring line is editing their program either way, which is the step an
installer has no business taking, so writing the file removed no manual work
— it only bought a little typing in exchange for a filename prompt, an
overwrite rule, and a second path to get wrong. If you are editing your
program anyway, you may as well paste the whole thing.

Which leaves the preview as the only thing that proves anything, and it
proves it regardless of how the code got there.

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

Setup codes are gone (2026-10-15). A server with no admin and no bootstrap
password refuses to start in production, where it used to print a setup code;
nothing claims a server, so `GET /api/v1/server` no longer says whether one is
claimed.

`server:verify` reads the server's state from sign-in: `GET /api/v1/server`
gives only the product and version, and the admin password is checked by
signing in with it.

## What chore needs first

Nothing. The typed code that guards deleting a server's data is not a file
write — it is reading back a string — and chore can already do that. A task
marked `interactive: true` gets the terminal and shares chore's process
group, so its script can `read -r code < /dev/tty`, and that read has every
property the gate needs without chore knowing it exists:

- `--yes` answers chore's own `confirm:` prompts and knows nothing about a
  `read` inside a script, so it cannot answer this one.
- `/dev/tty` is the terminal, not stdin, so `yes |` and `echo CODE |` feed
  nothing to it.
- With no terminal at all — a pipeline — opening `/dev/tty` fails, so the
  gate fails closed rather than open.
- The deliberate-automation flag is an ordinary task argument that skips the
  read.

A built-in `ask:` would still be nicer — one prompt style, `--dry` aware —
but it is polish, and nothing here waits on it.

## Built, and where it differs

Additions the building needed, each small enough to keep or drop:

- **`backup:attachments`.** `backup:create` leaves the attachments out and
  prints "the command that would take it"; there had to be one.
- **`install:helm --values FILE`.** A real cluster needs more than a hostname —
  an ingress, a storage class, resources — and without it the only way to give
  them was to stop using the task. It becomes part of the release, so upgrades
  keep it.
- **`backup:restore` is behind the typed code too.** It replaces what the server
  holds, which is destroying data as much as `data:destroy` is.
- **Where "the last backup" is recorded:** an annotation on the API's
  Deployment, `rest-mail.com/last-backup`, written by `backup:create`. The
  cluster knows when it was, whichever machine took it; `upgrade:*` reads it.
- **`backupTarget` is a value on the release**, for Pulumi installs too (in
  the Release's values). The day-2 tasks read the release, not the stack.
- **`server:verify` exits 3** when everything passes but the public layer, so an
  upgrade can tell "the server is broken" (offer the rollback) from "DNS is
  not there yet" (an upgrade did not cause it).
- **`password:reset` and `sessions:revoke` run in a pod, not a Job.** A pod
  built from the API's own pod template — same image, configuration, Secrets
  and network policy — minus the attachments volume and the liveness probe.
  Input reaches it through a Secret that lives as long as the pod does.
- **A reset password does not expire.** Resetting clears the bootstrap expiry,
  so the deployment's old password can never re-arm the account over the one
  just handed out; the cost is that the reset one has no 72 hours. It is shown
  once, to whoever ran the reset, and must be replaced at the next sign-in.
- **`sessions:revoke` while online** can let a client that refreshes between the
  two halves back in. It says so and recommends `server:offline` first, where
  nothing can refresh at all.

Found on the way, not part of this:

- **The published chart crashes its own gateways.** Since #279 the gateways
  refuse `SMTP_PORT_SUBMISSION`, `IMAP_PORT` and `POP3_PORT` in production, and
  the chart still sets all three, so chart 0.12.0 deploys three gateways in
  `CrashLoopBackOff`. Testing here ran with the gateways off.
- **Operator commands printed the database driver's connection errors on
  stdout** when the database was slow to answer, ahead of the JSON result.
  Fixed here: they install their quiet logger before connecting.

## Open

Nothing outstanding.
