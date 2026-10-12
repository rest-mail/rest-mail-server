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
which it is of no further use — and it **expires 72 hours after it is first
applied**, so an abandoned install does not sit on the internet waiting to be
claimed by whoever finds the password later. Restarting does not extend it.

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

```bash
chore install:password                    # keep what it prints

chore install:pulumi ~/infra mail.example.com "$PASSWORD"
# paste what it prints into your program, then:
pulumi up

chore install:check restmail --project ~/infra
```

`install:pulumi` takes your project's directory, the hostname and the
password; optionally `--name` (default `restmail`), `--domain` (worked out from
the hostname: `mail.example.com` serves `example.com`) and `--stack`. It shows
you the project, the stack and the domain it worked out, and asks before
touching anything.

Then it does the one thing it can safely do to your project: it puts the
password into the stack's config as an encrypted secret,
`restmail:bootstrapAdminPassword`. Everything else it **prints** — the
resource, in your project's language (read from `runtime:` in `Pulumi.yaml`),
ready to paste. YAML, TypeScript, Python and Go are printed in full; other
runtimes get the properties to set.

It prints rather than edits because a Pulumi project is a program, in a
language and layout this repository has never seen, and editing someone's
source is not something an installer can do safely. The printed resource
**reads** the password from config rather than containing it, because what you
paste gets committed.

What it prints for a YAML project:

```yaml
  restmail:
    type: kubernetes:helm.sh/v3:Release
    properties:
      name: restmail
      chart: oci://ghcr.io/rest-mail/charts/restmail
      namespace: rest-mail
      createNamespace: true
      skipAwait: true
      values:
        mailserver:
          hostname: mail.example.com
          domain: example.com
        api:
          bootstrapAdminPassword: ${restmail:bootstrapAdminPassword}
```

Every line of that was learnt by installing it:

- **`name` is set**, or Pulumi suffixes the release (`restmail-df5d9627`) and
  every object in it.
- **The whole `oci://` reference goes in `chart`.** Splitting it into
  `chart: restmail` plus `repositoryOpts.repo` fails with *"is not a valid
  chart repository or cannot be reached"*, which reads like a network problem
  and is not one.
- **`skipAwait: true`.** Every pod waits for the TLS Secret, so without it a
  `pulumi up` into a cluster where cert-manager has not issued one yet fails
  after five minutes with `context deadline exceeded`, on an install that is
  fine. With it, `pulumi up` takes seconds — and says nothing about whether
  the server is running, which is what `install:check` is for.

## Checking an install

```bash
chore install:check restmail --project ~/infra     # installed with Pulumi
chore install:check restmail                       # installed with Helm
```

Four checks, stopping at the first that fails, each saying what to do next:

```
Checking "restmail" in Pulumi project /home/you/infra, stack prod
  in the stack                ok
  admin password set          ok  (encrypted in the stack)
  helm finished               ok  (deployed)
  server answers              ok  (rest-mail 0.4.0, waiting for its admin)
```

The last is the one that matters. A resource in the stack, or a release Helm
calls `deployed`, proves only that the objects exist; asking the server over
HTTPS by its own name, as every client will, proves someone could sign in.
When it fails it says whether that is DNS, nothing listening, a certificate
still being issued, or something that is not rest-mail answering at that
address. `--url` asks somewhere other than `https://<hostname>`.

It never reads the password: it checks the key is in the stack's config,
which lists secrets without decrypting them, or in the release's values.

## Which cluster, and from where

The password has to reach the install, and where it is kept is your decision,
not the chart's:

| Installing from | Where the password lives |
| --- | --- |
| your own terminal | your password manager |
| a Pulumi stack | `pulumi config set --secret`, encrypted in the stack |
| a GitHub pipeline | `gh secret set RESTMAIL_ADMIN_PASSWORD` |

Never a public repository, and never a log. If it leaks or expires before you
have chosen your own password, deploy again with a new one: the server replaces
the old one, signs out anyone who used it, and gives the new one its own 72
hours. Once you have chosen your own password, the deployment's is ignored for
good — changing it cannot reset your account.
