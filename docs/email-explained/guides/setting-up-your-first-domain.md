---
title: Setting up your first domain
part: Using a REST Mail domain
summary: Add your domain to REST Mail, publish its DNS records, create your mailboxes and send your first message, step by step.
---

# Setting up your first domain

This guide takes a domain you own, such as `example.org`, from nothing to
sending and receiving mail on a REST Mail server. You do not need to be a mail
administrator. Allow time for your DNS changes to spread.

Throughout, replace:

- `example.org` with your domain;
- `mail.example.net` with your server's **mail host**, the name it answers mail
  on;
- `203.0.113.25` with that mail host's address.

## Before you start

You need four things:

- **A real domain, and access to its DNS.** Usually this is a control panel at
  the company you bought the domain from. A made-up name will not do: `.test`,
  for example, is reserved and never works on the public internet
  ([RFC 6761 §6.2](https://www.rfc-editor.org/rfc/rfc6761#section-6.2)).
- **The mail host's name and address.** The person who runs your server can give
  you both.
- **A way to sign in to the admin app.** It is usually at
  `https://admin.<your server's domain>`
  ([chart](../../../helm/restmail/README.md)).
- **A certificate for your domain.** [Step 3](#step-3-add-a-certificate) says
  where to get one.

### Who can do what

What you can do depends on the role your account holds. An account can be an
administrator of the whole server, or of one domain.

| Step | Whole server | One domain |
|---|---|---|
| Add a domain | Yes | No |
| Add a DKIM key | Yes | Yes, but the screen shows `Insufficient permissions` and does not list the key |
| Upload a certificate | Yes | No |
| Make the domain active | Yes | Yes |
| Create mailboxes | Yes | Yes, once signed in with two-factor authentication |
| See the DNS check | Yes | Yes |
| See the delivery queue | Yes | No |

If you administer only one domain, ask the person who runs your server to do the
steps you cannot.

### If you run the server yourself

Check these two before you go further, because neither can be fixed from REST
Mail, and each one stops your mail from being delivered.

**Your server must be able to reach other servers on port 25.** Many hosting
providers and home internet services block it by default, and you only find out
when your mail fails to arrive:

- Amazon EC2 "allows outbound traffic over port 25 only to private IPv4
  addresses" by default
  ([AWS](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-resource-limits.html)).
- On Google Cloud, connections to port 25 "are blocked when the destination is
  external to your VPC network"
  ([Google Cloud](https://cloud.google.com/compute/docs/tutorials/sending-mail)).
- Azure blocks it for most subscription types
  ([Microsoft](https://learn.microsoft.com/en-us/troubleshoot/azure/virtual-network/troubleshoot-outbound-smtp-connectivity)).
- DigitalOcean: "SMTP ports 25, 465, and 587 are blocked on Droplets"
  ([DigitalOcean](https://docs.digitalocean.com/support/why-is-smtp-blocked/)).
- Comcast "does not support port 25 for the transmission of email by our
  residential Internet customers"
  ([Xfinity](https://www.xfinity.com/support/articles/email-port-25-no-longer-supported)).

To check, run this on the server. It should say the connection succeeded:

```sh
nc -vz -w 5 gmail-smtp-in.l.google.com 25
```

If it is blocked, ask your provider to lift the block, or send through a relay
([how](troubleshooting-bouncing-mail.md#if-your-server-cannot-reach-anyone)).

**Reverse DNS must agree both ways.** The address must name a host (the PTR
record), and that host's A record must point back to the same address. Gmail
requires this of every sender
([Google](https://support.google.com/a/answer/81126)). You set the PTR record
with whoever owns the address, usually your hosting provider, not in your
domain's DNS. To check:

```sh
dig +short -x 203.0.113.25      # should print mail.example.net.
dig +short A mail.example.net   # should print 203.0.113.25
```

## Step 1: Sign in

Open the admin app and sign in with your full email address.

The first time an account signs in with a password someone else chose, the page
is headed **Finish account setup**. Choose your own password of at least 12
characters and press **Change password**. Then press **Set up authenticator**,
scan the QR code with an authenticator app, and save the recovery codes. Tick
**I have saved my recovery codes**, enter a code from the app, and press
**Verify and continue**. Then sign in again.

## Step 2: Add your domain

1. Open **Domains** and press **Add Domain**.
2. In **Domain Name**, type your domain: `example.org`.
3. Leave **Domain Type** on **Traditional**. You cannot change the name or type
   later.
4. Press **Create Domain**.

Your domain starts **Inactive**, and it stays that way until it has a
certificate. Its page lists which DNS records it can find, but not the values to
publish. [Step 7](#step-7-publish-your-dns-records) gives you those.

## Step 3: Add a certificate

REST Mail will not make a domain active until it holds a current certificate for
it. Other mail servers and your mail app talk to the mail host, whose own
certificate the person running the server looks after. This one is for your
domain's own name.

If you do not have one, you can get a free certificate from Let's Encrypt by
proving you control the domain's DNS. On your own computer, with
[Certbot](https://eff-certbot.readthedocs.io/en/stable/using.html) installed,
run the command below. It asks you to create a TXT record at
`_acme-challenge.example.org`
([Let's Encrypt](https://letsencrypt.org/docs/challenge-types/)):

```sh
sudo certbot certonly --manual --preferred-challenges=dns -d example.org
```

It saves `fullchain.pem` and `privkey.pem` in `/etc/letsencrypt/live/example.org/`.

Then, in the admin app:

1. Open **Settings**, and press **Configure** on **Certificates**.
2. Press **Upload Certificate** and choose your domain.
3. Paste `fullchain.pem` into **Certificate (PEM format)** and `privkey.pem`
   into **Private Key (PEM format)**.
4. Press **Upload Certificate**.

Check you pasted the right pair, for the right domain. The screen only checks that
the certificate can be read, so a mistake shows up later as failed connections.

**Renewing it is up to you.** Certbot says certificates from `--manual` "do not
support automatic renewal". The **Enable auto-renewal** box does not renew a certificate
you uploaded, as REST Mail's renewal only picks up certificates it issued itself
([code](../../../internal/acme/client.go)). The **Certificates** screen warns you
when one has 30 days or fewer left. Run the command again and upload the new pair.

## Step 4: Add a DKIM key

A DKIM key lets REST Mail sign your mail, so other servers can tell it is really
from you. A domain without one sends unsigned mail.

Follow [Add the DKIM key](setting-up-dkim-spf-dmarc.md#add-the-dkim-key), then
come back here. Keep the record it gives you for step 7.

## Step 5: Make the domain active

1. Open **Domains**, and press **View** on your domain.
2. Press **Edit**, tick **Domain is active**, and press **Save Changes**.

If you have no current certificate, you will see: `This domain has no currently
valid certificate, so it cannot go live. Add one first.`

## Step 6: Create your mailboxes

Do this before you publish DNS. As soon as your MX record changes, mail starts
arriving, and mail to an address that does not exist yet is refused.

1. Open **Mailboxes** and press **Create Mailbox**.
2. Enter the **Email Address**.
3. Either enter a **Password** of at least 12 characters, or leave it empty and
   invite the owner instead (below).
4. Choose a **Quota**. It starts at 1 GB.
5. Press **Create Mailbox**.

**Create `postmaster@example.org` first.** REST Mail does not make it for you.
Your DMARC record sends reports there, and until the mailbox exists those reports
are refused with `550 5.1.1 No such user`. It can have no password: an account you
created appears under your own in the webmail.

**To invite someone,** open their mailbox with **View**, enter your webmail's
address under **Invite the account owner**, press **Create invitation**, and send
them the link privately. It expires after 15 minutes. They choose their own
password and must set up an authenticator, so their mail app will need an
[app password](connecting-your-mail-app.md#if-you-use-two-factor-authentication).

## Step 7: Publish your DNS records

Sign in to your DNS provider and add these records. Some control panels want only
the part before your domain (`_dmarc`, not `_dmarc.example.org`). Use a TTL of
3600 if you are asked for one.

| Type | Name | Value |
|---|---|---|
| MX | `example.org` | `mail.example.net`, priority 10 |
| TXT | `example.org` | `v=spf1 mx -all` |
| TXT | `_dmarc.example.org` | `v=DMARC1; p=none; rua=mailto:postmaster@example.org` |
| TXT | `mail._domainkey.example.org` | The DKIM record from step 4 |
| TXT | `_smtp._tls.example.org` | `v=TLSRPTv1; rua=https://mail.example.net/.well-known/smtp-tlsrpt` |
| SRV | `_submissions._tcp.example.org` | `0 1 465 mail.example.net` |
| SRV | `_imaps._tcp.example.org` | `0 1 993 mail.example.net` |
| SRV | `_pop3s._tcp.example.org` | `0 1 995 mail.example.net` |
| SRV | `_submission._tcp.example.org` | `0 0 0 .` |
| SRV | `_imap._tcp.example.org` | `0 0 0 .` |
| SRV | `_pop3._tcp.example.org` | `0 0 0 .` |

The SRV records tell mail apps where to connect. The last three say that REST Mail
does not offer those unencrypted services
([RFC 6186 §3.4](https://www.rfc-editor.org/rfc/rfc6186#section-3.4)).

- **If the domain already has an MX or SPF record,** replace it. A domain must
  have only one SPF record
  ([RFC 7208 §3.2](https://www.rfc-editor.org/rfc/rfc7208#section-3.2)), and an old
  MX record sends mail to your old provider. If you already use the domain for
  mail, read [Migrating step by step](../landscape/migrating.md) first.
- **The person who runs your server** can print this exact list, your real DKIM
  value included, with `chore dns:show`, and compare it with what is published
  with `chore dns:check` ([how](../../installing.md#dns)). That list also has the
  mail host's A record and its PTR record, which are theirs to publish.

## Step 8: Check your records

Open **Domains**, press **View** on your domain, and press **Refresh** under **DNS
Configuration**. After a while, **MX**, **SPF**, **DMARC**, **DKIM** and **TLS-RPT**
should show **Verified**. New records can take time to be seen.

**MTA-STS** stays **Missing** for now, and that is fine: it is an optional later
step ([what it is](setting-up-dkim-spf-dmarc.md#later-mta-sts)).

**Verified means a record was found, not that it is right.** An MX record pointing
at your old provider shows **Verified**, and so does any SPF record. The page also
does not check SRV records. So compare the values yourself, from your own computer:

```sh
dig +short MX example.org                      # 10 mail.example.net.
dig +short TXT example.org                     # "v=spf1 mx -all"
dig +short TXT _dmarc.example.org              # "v=DMARC1; p=none; rua=..."
dig +short TXT mail._domainkey.example.org     # "v=DKIM1; k=rsa; p=..."
dig +short SRV _imaps._tcp.example.org         # 0 1 993 mail.example.net.
```

## Step 9: Send your first message

[Connect a mail app](connecting-your-mail-app.md), or sign in to the webmail. Then:

1. **Send** a message to an address you have at another provider, such as Gmail.
2. **Reply** to it from there.

The reply may take several minutes. REST Mail delays mail from a sender it has not
seen before, and a real mail server tries again by itself
([greylisting](../security/spam-defences.md)).

If your message does not arrive, read [Troubleshooting bouncing
mail](troubleshooting-bouncing-mail.md). If it lands in the spam folder, read [Why
mail goes to spam](why-mail-goes-to-spam.md).

## What to tighten later

Two settings start in a gentle mode, and nothing reminds you to tighten them:

- **DMARC** starts at `p=none`: receivers report on your mail, and change nothing.
- **MTA-STS**, if you turn it on, starts in `testing`.

[Tighten them](setting-up-dkim-spf-dmarc.md#after-a-month-tighten-dmarc) once your
reports look clean. Put a reminder in your calendar for a month from now.

## Sources

- Standards: [RFC 6761](https://www.rfc-editor.org/rfc/rfc6761) (reserved names),
  [RFC 6186](https://www.rfc-editor.org/rfc/rfc6186) (SRV records),
  [RFC 7208](https://www.rfc-editor.org/rfc/rfc7208) (SPF).
- Port 25: [AWS](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-resource-limits.html),
  [Google Cloud](https://cloud.google.com/compute/docs/tutorials/sending-mail),
  [Microsoft](https://learn.microsoft.com/en-us/troubleshoot/azure/virtual-network/troubleshoot-outbound-smtp-connectivity),
  [DigitalOcean](https://docs.digitalocean.com/support/why-is-smtp-blocked/),
  [Xfinity](https://www.xfinity.com/support/articles/email-port-25-no-longer-supported).
- Reverse DNS: [Google, Email sender guidelines](https://support.google.com/a/answer/81126).
- Certificates: [Let's Encrypt, challenge types](https://letsencrypt.org/docs/challenge-types/),
  [Certbot user guide](https://eff-certbot.readthedocs.io/en/stable/using.html).
- REST Mail: the screens in [`admin/src/routes/`](../../../admin/src/routes/); new
  domains in [`domains.go`](../../../internal/api/handlers/domains.go); the record
  list in [`mail_records.go`](../../../internal/dns/mail_records.go); the DNS check
  in [`domains.go`](../../../internal/api/handlers/domains.go), `dnsCheck`.
