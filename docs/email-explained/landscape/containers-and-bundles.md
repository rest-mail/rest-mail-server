---
title: "Containers and bundles: what packaging solves, and what stays the same"
part: The landscape, and where REST Mail fits
summary: How docker-mailserver, mailcow, Mailu, Mail-in-a-Box, iRedMail and kubernetes-mail-server package the classic stack, what that makes easier, and what is still inside.
diagram: ../diagrams/bundle.workflow.json
---

# Containers and bundles: what packaging solves, and what stays the same

Building a mail server from a tutorial means installing and connecting a dozen
programs by hand ([Why the security layers are so hard](../how-it-goes-wrong/why-security-is-hard.md)).
A family of projects does that work once, well, and hands you the result: a set
of containers, or a script that turns a fresh machine into a mail server. They
have made running your own mail possible for many people who would otherwise
not have tried, and several of the warnings quoted in this guide come from
their documentation.

REST Mail's author built two of these: a Docker-based mail server, and
[kubernetes-mail-server](https://github.com/kubernetes-mail-server). This
chapter draws on that experience: what packaging solves, and what stays the
same inside the package.

![One message through a packaged mail server](../diagrams/bundle.svg)

*The diagram's source is [`bundle.workflow.json`](../diagrams/bundle.workflow.json).*

## The bundles

Each describes itself, and what it includes, in its own documentation:

- **[docker-mailserver](https://github.com/docker-mailserver/docker-mailserver):**
  "A production-ready fullstack but simple containerized mail server (SMTP,
  IMAP, LDAP, Anti-spam, Anti-virus, etc.)", with "Only configuration files, no
  SQL database". It includes Postfix, Dovecot, Rspamd, Amavis, SpamAssassin,
  ClamAV, OpenDKIM, OpenDMARC, Fail2ban, Postscreen, Postgrey, Fetchmail and
  Getmail6, in one container.
- **[mailcow: dockerized](https://github.com/mailcow/mailcow-dockerized):** a
  suite of containers. Its Compose file defines eighteen services, among them
  Postfix, Dovecot, Rspamd, ClamAV, SOGo, MariaDB, Redis, Nginx, Unbound and an
  ACME client ([docker-compose.yml](https://github.com/mailcow/mailcow-dockerized/blob/master/docker-compose.yml)).
- **[Mailu](https://github.com/Mailu/Mailu):** "a simple yet full-featured mail
  server as a set of Docker images", with images for Postfix, Dovecot, Rspamd,
  Nginx and an admin interface, and optional ones for Fetchmail, Radicale and
  Unbound.
- **[Mail-in-a-Box](https://github.com/mail-in-a-box/mailinabox):** "turns a
  fresh Ubuntu 22.04 LTS 64-bit machine into a working mail server by installing
  and configuring various components": Postfix, Dovecot, Nextcloud, Z-Push,
  Roundcube, SpamAssassin, Postgrey, nsd4, OpenDKIM, Let's Encrypt, duplicity,
  ufw, Fail2ban and Munin. It sets SPF, DKIM, DMARC, DNSSEC, DANE TLSA and
  MTA-STS records automatically, and is deliberately not customisable: "There
  are no user-configurable setup options. It "just works.""
- **[iRedMail](https://www.iredmail.org/):** "a full-featured mail server
  solution" installed onto a supported Linux or BSD system, with accounts in
  OpenLDAP, MySQL, MariaDB or PostgreSQL, and "SpamAssassin, ClamAV, SPF, DKIM,
  greylisting", Roundcube or SOGo for webmail.
- **[kubernetes-mail-server](https://github.com/kubernetes-mail-server):** "A
  dovecot/postfix/opendkim/mysql Email Server which runs on Kubernetes", with one
  repository and image per component: Postfix, Dovecot, OpenDKIM, Rspamd,
  ClamAV, Redis, the database, and a setup job.

## What packaging solves

- **Installation.** A working server in an afternoon instead of a week.
- **Known-good defaults.** The pieces are connected the same way every time,
  by people who have fixed the mistakes before. Many of the tutorial errors in
  [The open relay](../how-it-goes-wrong/open-relay-and-spam-machine.md) cannot
  happen with an up-to-date bundle.
- **Upgrades.** A new image replaces every component at once, tested together.
- **The supporting work:** certificates, DNS records to publish, an admin
  interface, backups, health checks, depending on the bundle.

## What stays the same

Underneath, it is the same classic stack: an MTA, a mailbox server, and a
program for each check, each with its own configuration language. Packaging
changes who writes that configuration, not whether it exists.

- **The configuration is still there.** A bundle generates `main.cf`,
  `master.cf`, `dovecot.conf`, Rspamd's settings and the rest from its own,
  simpler settings. When something goes wrong, or you need something the bundle
  did not foresee, you are back in those files, now with a layer of templates
  between you and them.
- **The bundle can add its own traps.** Containers change how addresses look:
  docker-mailserver warns that trusting the Docker network "can create an open
  relay, for instance if IPv6 is enabled on the host machine"
  ([docker-mailserver: PERMIT_DOCKER](https://docker-mailserver.github.io/docker-mailserver/latest/config/environment/#permit_docker)),
  and Kubernetes hides client addresses by default
  ([The open relay](../how-it-goes-wrong/open-relay-and-spam-machine.md#2-a-gateway-that-makes-every-client-look-local)).
- **Every component still has to be understood to debug it.** A message that
  went missing may have been lost in Postfix, a milter, Rspamd, Dovecot or a
  Sieve script, each with its own log format.
- **The protocols' limits are unchanged.** A bundle cannot make SMTP authenticate
  senders, or make a forward pass SPF; it can only configure the same add-ons
  every server uses.
- **Security fixes still depend on every upstream project,** and on the bundle
  releasing a new image in time.

That is the lesson REST Mail's author took from building kubernetes-mail-server:
packaging makes the stack easier to install and harder to misconfigure, which
is a real improvement. But it wraps the problem rather than removing it. The
many programs, configuration languages and failure modes are still inside.

## Which to choose

If you want a mature, widely used stack today, a bundle is the safest way to
get one: pick the one whose scope (one container or many, a web interface or
none, customisable or not) fits how you work, and follow its documentation
rather than a tutorial. Keep it updated.

## How REST Mail differs

REST Mail is not a bundle. It does not package Postfix and Dovecot; it replaces
them with one program, so there is no generated configuration underneath and no
set of daemons to keep in step
([What REST Mail does differently](what-rest-mail-does-differently.md)):

- **No `mynetworks`, no milters, no `main.cf`.** The behaviours that go wrong in
  those files are not settings in REST Mail at all.
- **One log and one trace** for every message, across every step.
- **One install** with Helm or the installer, and one image per release.

It still speaks SMTP, IMAP and POP3 to everyone else, so it can sit beside a
bundle during a [migration](migrating.md).

## Sources

- [docker-mailserver](https://github.com/docker-mailserver/docker-mailserver),
  [mailcow: dockerized](https://github.com/mailcow/mailcow-dockerized),
  [Mailu](https://github.com/Mailu/Mailu),
  [Mail-in-a-Box](https://github.com/mail-in-a-box/mailinabox),
  [iRedMail](https://www.iredmail.org/),
  [kubernetes-mail-server](https://github.com/kubernetes-mail-server)
  (READMEs and documentation, read 2026-10-23).
- docker-mailserver: [PERMIT_DOCKER](https://docker-mailserver.github.io/docker-mailserver/latest/config/environment/#permit_docker).
