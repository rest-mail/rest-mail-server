# Installing and running rest-mail

Four steps, whichever way you install:

1. **Pick a method** — Helm, or Pulumi.
2. **Install.** The installer makes the admin password and shows it to you once.
3. **Verify.** Every install ends in `chore server:verify`.
4. **Sign in** with the rest-mail app as `admin@<your mail domain>`, with that password.

After that, the same tasks look after it: upgrade, back up, take it offline when
something is wrong, uninstall. They are all [chore](https://github.com/antimatter-studios/chore)
tasks, and every one of them does what you could have typed yourself — nothing
here is required to run rest-mail.

| Depends on how you deployed | Does not |
| --- | --- |
| `install:helm` · `install:pulumi` | `server:verify` · `server:status` · `server:logs` |
| `upgrade:helm` · `upgrade:pulumi` | `server:offline` · `server:online` |
| `uninstall:helm` · `uninstall:pulumi` | `backup:create` · `backup:database` · `backup:keys` · `backup:attachments` · `backup:restore` |
| | `dns:show` · `dns:check` |
| | `password:new` · `password:reset` · `sessions:revoke` |
| | `data:destroy` |

The right-hand column works on any install, however it was made: a Pulumi
`Release` installs through Helm, so both are a Helm release in the cluster.
They take `--name` (default `restmail`) and `--namespace` (default `rest-mail`),
and act on the cluster your kubeconfig points at.

`-y` answers chore's confirmations in advance. It goes **before** the task
name — `chore -y server:offline` — because after it, it is the task's first
argument.

## The admin password

The installer makes it, unless you bring one:

- **At a terminal, none given:** one is generated, used, and shown once.
- **In a pipeline, none given:** refused. A generated password printed to a
  pipeline's output is a password in a log. Make one where your secrets live
  (`chore password:new`) and pass it: `--password "$RESTMAIL_ADMIN_PASSWORD"`.
- **An earlier install left one** (its Secret is kept when you uninstall): it is
  left alone, so installing again never changes a password someone may be
  halfway through using.

The chart itself refuses to install without one, and will not invent one: a
chart runs where there is nobody to show it to.

**Shown as a QR code, and always as text.** Point your phone's camera at the
QR code and it opens the rest-mail app with the server, the first account's
address (`admin@<your mail domain>`) and the password filled in
(`https://rest-mail.com/setup#server=…&address=…&password=…`; the details ride
after the `#`, which no web server is sent). The link, server, address and
password are printed beneath it every time, so a QR code that
will not scan — a small font, a terminal that mangles it — never leaves you
stuck. The QR code needs the `restmail` CLI (`brew install antimatter-studios/tap/restmail`)
and a terminal at least about 55 columns wide; without either, you get the
text alone. `chore password:reset` shows its new password the same way.

It is the only way into a server nobody has signed in to. Using it makes you
choose your own password and enroll an authenticator, after which it is of no
further use — and it **expires 72 hours after the server first takes it**, so
an abandoned install does not sit on the internet waiting for whoever finds
the password. Lost or expired? `chore password:reset`.

## Owner recovery key

Register a public SSH key during installation so losing the administrator
password does not remove your independent recovery path:

```bash
chore install:helm mail.example.com --owner-key ~/.ssh/restmail_owner.pub
chore install:pulumi ~/infra mail.example.com --owner-key ~/.ssh/restmail_owner.pub
```

At a terminal, omitting `--owner-key` offers to create a dedicated Ed25519
key pair, select an existing public file, or keep managed keys unchanged.
Creation needs `ssh-keygen` and `python3` and asks OpenSSH for a passphrase
directly on your terminal. The default files are `restmail_owner_ed25519`
and `restmail_owner_ed25519.pub` in the directory where you ran chore. It
refuses existing files, including symlinks; the private key is mode `0600`.
Back up that private file securely and keep it out of source control. This
operation runs on your machine; the private key is never uploaded.

Select a key whose private half you control on your own machine. The installer
validates the public file and shows its SHA-256 fingerprint, then registers it
as initial Helm `api.ownerKeys` or Pulumi `<name>:ownerKeys`. The API imports
these public keys into PostgreSQL only while its owner registry is empty.
After enrollment the database is authoritative: recovery works after removing
that configuration or moving the database to another deployment platform.
Generated Pulumi programs pass the initial value to the chart. The installer does not choose a key from your SSH agent
or promote a read-only status key. Pipelines do not prompt or create keys;
use `--owner-key FILE.pub` or managed owner-key configuration. If no
owner key is selected, it says recovery will require cluster administrator
access; existing configured owner keys are preserved.

To recover later, load the corresponding private key into `ssh-agent` and run:

```bash
ssh-add ./restmail_owner_ed25519   # or your existing private-key filename
restmail reset-admin --server https://mail.example.com --address admin@example.com
```

The server verifies a fresh signed challenge against its registered owner
public keys before issuing a short-lived, single-use reset token. Password
changes do not remove this recovery authority. Keep a secure backup of the
private key. If the password and every owner key are lost, the CLI cannot
recover the account without trusted deployment administration access.

An existing deployment upgrading to the database registry imports its current
configured owner keys once. After that, changing initial configuration cannot
replace owners or restore a revoked key. The registry is included in database
backups; private keys remain on the owner machine.

For an existing installation, use the API's trusted operator command with its
normal database configuration, however it runs:

```bash
restmail-api owner-key add < ./new-owner.pub
restmail-api owner-key list
restmail-api owner-key revoke --fingerprint SHA256:...
```

These maintenance commands require control of the installation; no public API
lets an anonymous visitor register an owner. Normal recovery still uses only
the HTTPS API and the owner's private key, never Kubernetes or Docker access.
Revocation is effective on every API replica at the next request. Revoked rows
are retained, so a restart does not silently enroll the old key again. An
explicit `owner-key add` can restore a revoked key. Do not rerun the installer
over a live release.

## Helm

```bash
chore install:helm mail.example.com
```

It shows you which cluster it is about to install into and asks, because the
mistake that matters is not a typo in the hostname but installing a mail
server into whatever cluster the kubeconfig happened to point at. Then it
installs, waits for the pods, and runs `server:verify`.

- `--domain` — the mail domain; worked out from the hostname otherwise
  (`mail.example.com` serves `example.com`)
- `--values FILE` — everything else the chart takes: an ingress, a storage
  class, resources. Upgrades keep it.
- `--version` — a chart version; the newest otherwise
- `--password`, `--owner-key FILE.pub`, `--name`, `--namespace`

It installs once. If rest-mail is already there it says so and points you at
`upgrade:helm` — an install over the top would put back its own defaults.

## Pulumi

For TypeScript projects, the reusable [REST Mail Pulumi component](../packages/pulumi/README.md)
manages the Helm release and an optional APNs Kubernetes Secret. Its `apns.privateKey`
input accepts encrypted Pulumi configuration, keeping the `.p8` key out of Helm values.
The package is currently built from this repository; its README includes the complete
resource declaration, installation commands and namespace/data-retention behavior.
`install:pulumi` now prints that component for TypeScript projects, including
commands to build a local package into the destination project's `vendor/`
directory and install it through npm. It prints a factory that takes the project's
existing Kubernetes provider; in `homelab-server`, pass `cluster`. Go, Python and
YAML projects retain their direct Helm release declarations.

```bash
chore install:pulumi ~/infra mail.example.com
```

Chore owns your stack's config; you own the program. The basic config keys
are under the mail server's name:

```
restmail:hostname           mail.example.com
restmail:domain             example.com
restmail:namespace          rest-mail
restmail:chartVersion       0.14.0
restmail:adminPasswordHash  $2a$10$…
```

So the code is the same for every install and every stack, contains no secret,
and is never touched again: an upgrade is a config change.

For local TypeScript installs, add APNs push credentials with:

```sh
chore install:pulumi ~/infra mail.example.com \
  --apns-key-file /secure/path/AuthKey.p8 \
  --apns-key-id APPLE_KEY_ID --apns-team-id APPLE_TEAM_ID
```

The file is the APNs push-notification key. Its exact contents go through stdin
to `pulumi config set --secret --raw`; they never appear in command arguments,
printed source or Helm values. `--apns-topic` defaults to
`com.antimatterstudios.restmail`. Omitted flags preserve existing encrypted key
config and public identifiers. A plaintext existing `apnsPrivateKey` is refused;
pass the file again to encrypt it. Secret configuration needs the selected
stack's secrets provider to be available. APNs setup for an inaccessible
pipeline-owned stack is deliberately deferred, rather than printing a key.

The generated component reads `createNamespace` as an optional boolean. Set it
to `false` when the destination namespace is managed by another resource or
stack. The component retains a namespace it creates so removal keeps mail data.


**The admin password is in the stack only as a bcrypt hash** (`restmail
password-hash`), an ordinary value that nobody can sign in with. The password
itself is made by the installer, 32 random characters, and shown once — as a
QR code and as text. The password hash needs no stack secret and is fine to
commit; optional APNs configuration does need the stack's secrets provider. It needs `pulumi`, `jq` and the `restmail` CLI 0.7.0 or
newer — and `kubectl` only where it deploys the stack itself.

1. It sets the config — generating the password if you gave none and the stack
   has none, and showing it to you then.
2. It runs `pulumi preview` to see whether your program already declares
   rest-mail. If not, it prints the code for your project's language (from
   `runtime:` in `Pulumi.yaml`) — a file of its own and the one line that
   wires it in — and waits while you add it. It never edits your program.
3. It previews again. This is the only thing that proves the config and the
   code found each other: a wrong stack, config namespace or project shows up
   here instead of as a server nobody can sign in to.
4. It offers `pulumi up` on the REST Mail component and its declared children
   (namespace, APNs Secret and Helm release), with explicit targets that exclude
   unrelated stack resources. Existing direct releases still target only the
   release. It then runs `server:verify`. Or deploy through your normal process
   and run `server:verify` yourself. Secret-redacted Helm values are checked
   through the component's public URL output.

Run it again at any point and it picks up where it was. `--name mail-eu` puts
a second mail server in the same stack under its own config namespace.

**A project a pipeline deploys.** Where the stack's state cannot be read from
the machine you run it on — its backend's credentials live in the pipeline —
it sets nothing in the stack. It prints the config above as lines for
`Pulumi.<stack>.yaml`, shows the password, prints the code, and waits while you
commit both and your pipeline deploys them. Then it checks the server over its
public API: that it answers as rest-mail at `https://<host>`, and that the
admin password signs in.

Every line of the printed code was learnt by installing it:

- **`name` is set**, or Pulumi suffixes the release (`restmail-df5d9627`) and
  every object in it, and nothing is called what the tasks look for.
- **The whole `oci://` reference goes in `chart`.** Split into a chart and a
  repository it fails with a message that reads like a network problem.
- **`skipAwait: true`.** Every pod waits for the TLS Secret, so without it
  `pulumi up` fails after five minutes on an install that is fine.
  `server:verify` is what says whether it is running.
- **A `Release`, not a `helm.v3.Chart`.** A Release uninstalls through Helm,
  which keeps the mail; a Chart is plain Pulumi resources, and removing it
  deletes every one.

## Is it working: server:verify

```
  resources created     ok   ConfigMap, Deployment, NetworkPolicy, ...
  pods ready            ok   3 pods across 3 workloads
  answers in-cluster    ok   rest-mail 0.4.0, via port-forward
  admin password works  ok   sign-in accepted, onboarding pending (password, two_factor)
  answers publicly      ok   https://mail.example.com, certificate valid
```

Layer by layer, stopping at the first failure, and saying whose failure it is.
In-cluster comes before public on purpose: passing in-cluster and failing
publicly means the server is fine and DNS, the ingress or the certificate is
not — and it exits 3 rather than 1 to say so. Signing in spends nothing: it
signs out again, and only choosing a new password retires the install's.

`server:status` is the passive half — versions, revision, replicas — and works
when the server is broken, which is when you need it. Verify prints it first.
`server:logs api -f` follows a component's logs (`api`, `smtp`, `imap`,
`pop3`, `js-filter`, `postgres`).

## Upgrading

```bash
chore upgrade:helm                     # to the newest chart
chore upgrade:helm --version 0.13.0
chore upgrade:pulumi ~/infra
```

It takes nothing but the version. Everything else is read back from the
release or the stack, because the value you forget does not fail: leave
`--set mailserver.hostname` off a hand-typed `helm upgrade` and the server
quietly starts answering as `mail.example.com`.

Before it starts it looks for a backup from the last day and offers to take
one. It does **not** roll back on its own if verify fails afterwards: a
database migration may already have run, and the old version under a migrated
database can be worse. It tells you what failed and the command to go back.

## Uninstalling keeps the mail

```bash
chore uninstall:helm
chore uninstall:pulumi ~/infra
```

Uninstalling removes the software, not the mail — as uninstalling a word
processor leaves your documents. The volumes and the two Secrets are kept, and
installing again under the same name and namespace picks them up: uninstall to
reinstall with different settings and nothing is lost.

`uninstall:pulumi` cannot remove anything itself: a removal your program does
not reflect is undone by the next `pulumi up`. It tells you what to take out
of the program, waits, then checks that the release went and what was kept.

Deleting the data is a separate step with no undo:

```bash
chore data:destroy
```

It refuses while rest-mail is installed, lists what it will delete, and asks
you to type a code it has just made up. `-y` does not answer it, a pipe cannot
feed it, and with no terminal it refuses. `--i-mean-it` is the way past for a
script that genuinely means to.

## Backups

```bash
chore backup:create --to ~/backups
```

One file: the database, the master key, and the install's own facts — what you
need to deploy it again before you can restore into it. The master key matters
as much as the database: it decrypts the DKIM keys, the certificate keys and
every authenticator secret.

Because it holds the master key, the file is a secret, and there is no default
place for it. `--to`, or record a place on the release once (`backupTarget` in
its values). It refuses a place inside a git working tree that is not ignoring
it.

The attachments are not in it. They are a volume, not a dump, and copying it
streams all of it through the cluster, so `backup:create` says how big they
are and leaves them to `chore backup:attachments`. `backup:database` and
`backup:keys` take one part each.

```bash
chore backup:restore --from FILE
```

Says what the file holds and what it does not, then — behind the same typed
code as `data:destroy`, since it replaces what the server holds — takes the
server offline, restores, brings it back and verifies.

## When something is wrong

```bash
chore server:offline        # nothing answers; every stolen token stops working now
chore sessions:revoke       # sign everybody out
chore password:reset        # if an admin's password is suspect, or lost
chore server:online         # put back exactly what offline stopped
```

Offline contains rather than cures: coming back without ending whatever leaked
hands the access straight back. `sessions:revoke` and `password:reset` run in a
pod of their own in the namespace, so they need no mail, no DNS, no
certificate and not even the API running — which is why they work while it is
offline. A reset password is shown once; the account must choose its own and
enroll a new authenticator at the next sign-in.

## DNS

```bash
chore dns:show       # every record each domain needs
chore dns:check      # what a public resolver actually sees
```

MX, SPF, DMARC, DKIM, the mail host's address and its reverse record. A
healthy install delivers nothing to Gmail without them, so this is the likeliest
reason one that passes `server:verify` still "does not work". The DKIM record
can only come from the server, which made the key.

### Recovering through Admin or Webmail

If the installation password is unavailable, the operator can run
`restmail-api mint-token --purpose reset-admin --address admin@example.com`
inside the API container. Give the account owner the token privately; on either
web app, choose **Use an account recovery token**. The owner chooses a password
on the site, adds an authenticator using its setup key, saves the recovery
codes, and confirms a current code. They then sign in with the new password
and authenticator. The token is single use; redemption ends all previous
sessions and revokes the old credentials. Do not put tokens or passwords in
CI logs, issue comments, or repository files.

## Ensure a missing DKIM key

Inside the API container, using its existing configuration and database:

```sh
restmail-api dkim-key ensure --domain example.com
```

Use the exact canonical ASCII name of an existing domain. This deployment-only
command requires a configured `MASTER_KEY` of at least 16 bytes, including in
development. It connects without migrations, RBAC seeds, or account/domain
creation. When both the stored selector and key are absent (NULL or empty), it
atomically installs a fresh encrypted 2048-bit RSA key with the default selector.
Concurrent calls serialize on that domain; lock waits and transactions are bounded.

The single JSON result contains `domain`, `selector`, `created`, and
`publicDKIMTXT`. Publish the TXT value at `<selector>._domainkey.<domain>`.
A retry validates and preserves complete existing material byte for byte and
returns its actual selector and public TXT with `created: false`. Partial,
invalid, or unreadable material fails without repair or rotation. Review such
material separately before taking any further action. Private keys and raw
configuration/database errors are never printed, even with
`RESTMAIL_OPERATOR_DEBUG=1`.
