---
title: Setting up DKIM, SPF and DMARC
part: Using a REST Mail domain
summary: Publish the three records that prove your mail is really from you, check they work, and tighten DMARC and MTA-STS once your reports look clean.
---

# Setting up DKIM, SPF and DMARC

Anyone can put your address on a message. These three records let other mail
servers tell real mail from forgeries, and many will not trust mail without them:

- **SPF** lists which servers may send mail for your domain.
- **DKIM** puts a signature on each message, which proves your domain sent it and
  that nobody changed it on the way.
- **DMARC** tells other servers what to do when a message fails those checks, and
  asks them to send you reports.

If you want to know how they work, read [SPF](../security/spf.md),
[DKIM](../security/dkim.md) and [DMARC](../security/dmarc.md). This guide only sets
them up.

Replace `example.org` with your domain and `mail.example.net` with your mail host.

## Add the DKIM key

REST Mail signs your mail with a private key, and other servers check the signature
against a public key in your DNS. You make the pair yourself, on your own computer.

**1. Make the key.** This creates a 2048-bit key in the file `dkim.private`:

```sh
openssl genrsa -out dkim.private 2048
```

REST Mail refuses a smaller RSA key
([code](../../../internal/outbound/dkimkey.go)).

**2. Give the private key to REST Mail.**

1. In the admin app, open **Settings**, and press **Configure** on **DKIM Keys**.
2. Press **Add DKIM Key**, and choose your domain.
3. Leave **Selector** as `mail`. It decides the record's name.
4. Paste the whole contents of `dkim.private` into **Private Key (PEM format)**.
5. Press **Create DKIM Key**.

From now on, your mail is signed. REST Mail keeps the key encrypted, and never shows
it again. Delete `dkim.private`, or keep it somewhere as safe as your passwords:
anyone holding it can sign mail as you.

**3. Work out the DNS record.** The screen does not show it, so make it from the
key. This prints the value to publish:

```sh
echo "v=DKIM1; k=rsa; p=$(openssl rsa -in dkim.private -pubout -outform der 2>/dev/null | base64 | tr -d '\n')"
```

**4. Publish it** as a TXT record named `mail._domainkey.example.org`.

The value is about 400 characters long. A single DNS text string holds 255, so
some DNS providers split it for you, and some ask you to split it into quoted
pieces. Either is fine: the pieces are joined back together
([RFC 6376 §3.6.2.2](https://www.rfc-editor.org/rfc/rfc6376#section-3.6.2.2)). Do not
add spaces or line breaks inside the value.

## Publish SPF

Add one TXT record on `example.org` itself:

```text
v=spf1 mx -all
```

That says: "the servers in my MX record may send my mail, and nobody else may". It
stays right if your server's address changes, because only the mail host's A record
needs to change. `-all` is already the strict setting, so there is nothing to tighten
later.

Three things can go wrong:

- **Only one SPF record.** If you already have one, merge them. Two make SPF fail
  ([RFC 7208 §3.2](https://www.rfc-editor.org/rfc/rfc7208#section-3.2)).
- **Other services that send as you,** such as a newsletter tool, must be added as
  each one's instructions say, for example `v=spf1 mx include:mailer.example -all`.
  A record may need at most 10 DNS lookups, and each `include` uses at least one
  ([RFC 7208 §4.6.4](https://www.rfc-editor.org/rfc/rfc7208#section-4.6.4),
  [more](../security/spf.md#the-10-lookup-limit)).
- **Mail must leave from the MX's address.** If your server sends from a different
  address than the one your mail host resolves to, as some cloud networks do, SPF
  fails. Ask the person who runs your server. If it does, they can add it, for
  example `v=spf1 mx ip4:203.0.113.26 -all`.

## Publish DMARC

Add a TXT record named `_dmarc.example.org`:

```text
v=DMARC1; p=none; rua=mailto:postmaster@example.org
```

- `p=none` means: "check my mail and send me reports, but do not treat it any
  differently". This is the right place to start, and every new domain on REST Mail
  starts here.
- `rua=` is where reports go. **Create the mailbox `postmaster@example.org`,** because
  REST Mail does not create it for you. Without it, reports are refused.

## Check they work

**Check the records** with **Refresh** on your domain's page, and then compare the
values, because **Verified** only means a record was found:

```sh
dig +short TXT example.org
dig +short TXT mail._domainkey.example.org
dig +short TXT _dmarc.example.org
```

**Check a real message.** Send one to an account at another provider, and look at its
headers. In Gmail, open the message's menu and choose **Show original**
([Google](https://support.google.com/mail/answer/29436)). You want to see:

```text
SPF:   PASS
DKIM:  PASS with domain example.org
DMARC: PASS
```

If one fails:

| Result | Likely cause |
|---|---|
| SPF `FAIL` | The address your mail left from is not your MX's address. See [above](#publish-spf) |
| SPF `PERMERROR` | Two SPF records, or too many lookups |
| DKIM missing | No key added, or the domain's key was deleted |
| DKIM `FAIL` | The published key does not match. Check for a typing error, or a space or line break in the value |
| DMARC `FAIL` | SPF and DKIM both failed, or neither matches your `From` domain |

## After a month: tighten DMARC

`p=none` protects nobody: a forged message from your domain still gets delivered.
Nothing in REST Mail reminds you to move on, so put it in your calendar.

The standard's advice is to publish `p=none` "for at least a month, followed by
publishing "p=quarantine" for an equally long period of time", and to compare
results at each step ([RFC 9989 §7.4](https://www.rfc-editor.org/rfc/rfc9989#section-7.4)).

### Step 1: Read your reports

Providers send reports to `postmaster@example.org`, usually one a day, as compressed
files. REST Mail reads them and stores them for you. There is no screen for them yet,
so read them with the API ([`$TOKEN` is explained below](#step-2-set-the-new-policy)):

```sh
curl -fsS "$SERVER/api/v1/admin/dmarc-reports" -H "Authorization: Bearer $TOKEN" | jq '.data[] | {reporter, start_date, end_date,
  records: [.records[].row | {source_ip, count, dkim: .policy_evaluated.dkim, spf: .policy_evaluated.spf}]}'
```

Each record says which address sent how many messages, and whether DKIM and SPF
passed:

- **Your server's address, passing:** good.
- **Another service you use, failing:** fix it first. Add it to SPF, or set up DKIM
  in that service. If you tighten now, that service's mail will suffer.
- **Addresses you do not know, failing:** probably forgers. This is what DMARC stops.

You are ready when everything you recognise passes.

### Step 2: Set the new policy

**This takes two changes,** and the first does not change what other servers see.

First, tell REST Mail, so that the list `chore dns:show` prints stays correct. There
is no screen for this either. An administrator of the domain can run this, with the
domain's number from the address bar of its page:

```bash
SERVER=https://mail.example.net
read -rp 'Address: ' ADDRESS; read -rsp 'Password: ' PASSWORD; echo; read -rp 'Code: ' CODE
TOKEN=$(curl -fsS "$SERVER/api/v1/auth/login" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg e "$ADDRESS" --arg p "$PASSWORD" --arg c "$CODE" '{email:$e,password:$p,totp_code:$c,client:"native"}')" | jq -r .data.access_token)
curl -fsS -X PATCH "$SERVER/api/v1/admin/domains/$DOMAIN_ID" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"dmarc_policy":"quarantine"}' | jq .data.dmarc_policy
```

If your account has no authenticator, press Enter when asked for the code.

Second, **change the TXT record** at your DNS provider, which is what other servers
actually read:

```text
v=DMARC1; p=quarantine; rua=mailto:postmaster@example.org
```

Failing mail now usually goes to the recipient's spam folder.

### Step 3: Move to reject, if it suits you

After another month of clean reports, do the same with `reject` and `p=reject`.
Failing mail is then refused outright.

**Do not do this if your people write to mailing lists.** A list resends your message,
and that breaks the checks, so subscribers' servers may refuse it. The standard says
domains with such users "SHOULD NOT publish" `p=reject`
([RFC 9989 §7.4](https://www.rfc-editor.org/rfc/rfc9989#section-7.4),
[more](../security/dmarc.md#why-preject-and-mailing-lists-do-not-mix)). Stay at
`quarantine`.

If something goes wrong, go back: set the record to `p=none` again.

## Later: MTA-STS

SPF, DKIM and DMARC protect your address. **MTA-STS** protects your mail on its way to
you: it tells other servers that they must use an encrypted connection to reach you
([how it works](../security/mta-sts-and-dane.md#mta-sts-a-policy-over-https)). It is
optional, and you can leave it for later. Until you add it, the DNS check shows
**MTA-STS** as **Missing**, and that is fine.

It needs the person who runs your server, because `mta-sts.example.org` must have an
HTTPS route and a certificate ([their steps](../../installing.md#mta-sts-policy-hosts-with-cert-manager)).

1. In the admin app, open **Settings**, press **Configure** on **MTA-STS**, choose your
   domain, and press **Load Policy**.
2. Choose **Testing**. A new policy starts there. In **MX Hosts**, enter your mail host.
3. Press **Save Policy**.
4. Ask the person who runs your server to run `chore dns:show`, which prints your
   `_mta-sts` record and the `mta-sts` CNAME. Publish the CNAME, wait until they confirm
   HTTPS works, and then publish the `_mta-sts` record.

**Testing changes nothing about delivery.** Other servers report problems, but still
deliver. Read those reports under **Settings**, on **TLS Reports**. The `_smtp._tls`
record is what asks for them.

**When the reports look clean, tighten it.** Nothing reminds you, so put this in your
calendar too. The policy's `testing` mode does not protect your mail. Choose **Enforce**,
save, and publish the new `_mta-sts` record: its `id` changes whenever the policy does,
and other servers fetch the policy again only when the `id` changes
([RFC 8461 §3.1](https://www.rfc-editor.org/rfc/rfc8461#section-3.1)).

**Enforce can stop your mail arriving** if your certificate lapses or your mail host
changes, because other servers will then refuse to deliver. Make sure certificates are
renewed automatically first.

## Sources

- Standards: [RFC 6376](https://www.rfc-editor.org/rfc/rfc6376) (DKIM),
  [RFC 7208](https://www.rfc-editor.org/rfc/rfc7208) (SPF),
  [RFC 9989](https://www.rfc-editor.org/rfc/rfc9989) (DMARC),
  [RFC 8461](https://www.rfc-editor.org/rfc/rfc8461) (MTA-STS).
- Google: [Trace an email with its full headers](https://support.google.com/mail/answer/29436).
- REST Mail: the record values, in
  [`mail_records.go`](../../../internal/dns/mail_records.go); the starting policy, in
  [`domains.go`](../../../internal/api/handlers/domains.go); DKIM keys, in
  [`dkimkey.go`](../../../internal/outbound/dkimkey.go) and
  [`dkim.go`](../../../internal/api/handlers/dkim.go); DMARC reports, in
  [`dmarc_reports.go`](../../../internal/api/handlers/dmarc_reports.go) and
  [`dmarc_reports_received.go`](../../../internal/pipeline/filters/dmarc_reports_received.go);
  MTA-STS, in [`mtasts.go`](../../../internal/api/handlers/mtasts.go); the screens, in
  [`admin/src/routes/settings/`](../../../admin/src/routes/settings/).
