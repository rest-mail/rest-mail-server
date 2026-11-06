---
title: Connecting your mail app
part: Using a REST Mail domain
summary: The settings to put into Thunderbird, Apple Mail or any other mail app, how to sign in with two-factor authentication, and what each error means.
---

# Connecting your mail app

Any mail app that speaks IMAP and SMTP works with REST Mail: Thunderbird, Apple
Mail, Outlook, and the others. You need your email address, your password, and the
settings below.

## The settings

Replace `mail.example.net` with your server's mail host. The person who runs your
server can tell you what it is. It is the value of your domain's MX record, which
you can see with `dig +short MX example.org`.

| | Server | Port | Security |
|---|---|---|---|
| **Incoming (IMAP)** | `mail.example.net` | 993 | SSL/TLS |
| **Outgoing (SMTP)** | `mail.example.net` | 465 | SSL/TLS |
| **Incoming (POP3)**, if you prefer it | `mail.example.net` | 995 | SSL/TLS |

- **Username:** your full email address, `you@example.org`, for both. `you` alone
  is refused.
- **Password:** your account's password, or an [app
  password](#if-you-use-two-factor-authentication).
- **Authentication:** normal password. Some apps call it "password, sent
  securely" or "PLAIN".

Three things catch people out:

- **Choose SSL/TLS, not STARTTLS.** REST Mail encrypts these connections from the
  first byte, which is what the standard recommends for mail apps
  ([RFC 8314](https://www.rfc-editor.org/rfc/rfc8314)).
- **Ports 587, 143 and 110 do not work.** Nothing listens on them. Many apps try
  587 first, so check what yours filled in.
- **Use the mail host, not your domain.** `example.org` is often your website, and
  an app that connects there fails or shows a certificate warning. Do not accept
  the warning: correct the server name.

Choose IMAP unless you have a reason not to. It keeps your mail on the server, so
your phone and laptop stay in step. POP3 suits one device, and the person who runs
your server may have turned it off.

## Setting up your app

**Try the automatic way first.** Many apps ask only for your address and password,
then look up the rest. REST Mail answers those lookups with the settings above
([how they work](../how-email-works/finding-the-server.md)).

They work only if your domain's lookup names reach REST Mail, and the records in
[the first guide](setting-up-your-first-domain.md#step-7-publish-your-dns-records) do
not arrange that. So if your app says it cannot find your settings, or fills in
something different from the table, type them in by hand.

**Thunderbird:** add a mail account with your name, address and password. If it
does not find the right settings, choose the manual option, enter the table above,
and press **Re-test** ([Mozilla](https://support.mozilla.org/en-US/kb/manual-account-configuration)).

**iPhone and iPad:** in **Settings**, open **Apps**, then **Mail**, then **Mail
Accounts**. Press **Add Account**, enter your address, and press **Next**. Press
**Add Other Account**, then **Mail Account**. Enter your details, and when asked,
choose IMAP and enter the server names ([Apple](https://support.apple.com/en-us/102619)).
Apple's configuration profiles are not offered.

**Anything else:** look for "manual setup", "advanced" or "other", and choose IMAP.

## After you connect

**Send yourself a test message,** and then one to an address at another provider.

**You may see each sent message twice in your Sent folder.** REST Mail keeps a copy
of everything you send, and so do most mail apps. Look in your app's account
settings for "place a copy in" or "save sent messages on the server", and turn it
off.

**Your app sees four folders at first:** Inbox, Sent, Drafts and Trash. REST Mail
gives the special meaning to Spam and Archive folders once they exist, so an app
that creates them files mail in the right place
([code](../../../internal/gateway/imap/backend.go)).

**Held mail is not in your app.** A message held for review is only in the webmail,
under `quarantine` ([more](why-mail-goes-to-spam.md#mail-coming-to-you)).

## If you use two-factor authentication

An account with an authenticator cannot sign in to a mail app with its own
password, because a mail app has nowhere to enter a code. It needs an **app
password**: a long password for one app, which you can revoke without changing
anything else. An account you joined by invitation always has an authenticator,
so it always needs one.

There is no screen for this. On a Mac or Linux computer with `curl` and `jq`, paste this into a terminal, and replace the
server name first. It asks for your address, password and a current code, keeps
them out of your shell history, and prints the app password once. Copy it into your
mail app as the password.

```bash
read -rp 'Address: ' ADDRESS; read -rsp 'Password: ' PASSWORD; echo; read -rp 'Code: ' CODE
SERVER=https://mail.example.net
SESSION=$(curl -fsS "$SERVER/api/v1/auth/login" -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg e "$ADDRESS" --arg p "$PASSWORD" --arg c "$CODE" '{email:$e,password:$p,totp_code:$c,client:"native"}')")
TOKEN=$(jq -r .data.access_token <<<"$SESSION"); ID=$(jq -r .data.user.id <<<"$SESSION")
curl -fsS "$SERVER/api/v1/accounts/$ID/credentials" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg p "$PASSWORD" '{name:"My mail app",password:$p}')" | jq -r .data.password
```

If you cannot run this, ask the person who runs your server for help.

**Changing your password revokes every app password.** You will need to make new
ones.

## When something goes wrong

Your app may show these words, or hide them behind its own message. Look for a
"details" or "log" option.

| What you see | What it means | What to do |
|---|---|---|
| `Authentication failed`, or `[AUTHENTICATIONFAILED] Invalid credentials` | The address or password is wrong, or the account needs an app password | Use your full address. If you use two-factor authentication, use an app password |
| `Too many authentication failures` | Five failed sign-ins within 10 minutes. The account is locked for 30 minutes, even with the right password and from your other devices | Correct the saved password on **every** device, then wait |
| Connection refused, or it times out | Wrong port, or a network blocking it | Use 993 and 465 |
| A certificate warning, or a secure connection that fails at once | The server name is not the mail host | Correct the server name |
| `Sender address not authorized for this account` or `From header address not authorized for this account` | You are sending from an address that is not yours | Check the "From" address your app uses, and ask for access if you should have it |
| `Sending temporarily held; retain this message and retry later` | You passed the sending limit, by default 20 messages a minute or 100 an hour. A message headed `REST Mail sending limit reached` tells you when it resets | That message was not sent. Send it again after the reset |
| `Temporary authentication failure, try again later` | The server could not check your password just now | Wait a minute and try again |

## Sources

- [RFC 8314](https://www.rfc-editor.org/rfc/rfc8314): TLS for mail submission and access.
- Mozilla: [Manual account configuration](https://support.mozilla.org/en-US/kb/manual-account-configuration).
- Apple: [Add an email account to your iPhone or iPad](https://support.apple.com/en-us/102619).
- REST Mail: the settings apps are given, in
  [`autoconfig.go`](../../../internal/api/handlers/autoconfig.go); what SMTP refuses,
  in [`session.go`](../../../internal/gateway/smtp/session.go); the sign-in limit, in
  [`limiter.go`](../../../internal/gateway/connlimiter/limiter.go); the sending limit,
  in [`submission.go`](../../../internal/ratelimit/submission.go) and
  [`service.go`](../../../internal/sendlimits/service.go); app passwords, in
  [`credentials.go`](../../../internal/api/handlers/credentials.go).
