---
title: Troubleshooting bouncing mail
part: Using a REST Mail domain
summary: What a bounce message is telling you, what to do about each kind, what happens to mail that is still being retried, and what to check when other people cannot reach you.
---

# Troubleshooting bouncing mail

A **bounce** is a message telling you that something you sent was not delivered.
This guide helps you read one, fix the cause, and find out what happened to a
message when no bounce has arrived.

## Read the bounce

A bounce from REST Mail arrives in your inbox from **Mail Delivery System**, with
the subject `Undelivered Mail Returned to Sender <the address>`. It looks like this:

```text
This is the mail delivery system at mail.example.net.

Your message could not be delivered to the following recipient:

    someone@example.com

The recipient's mailbox does not exist.

The delivery has been attempted 1 time(s).

Error: smtp; 550 The email account that you tried to reach does not exist
```

Two lines matter:

- **The sentence after the address** is REST Mail's own summary of the problem.
- **The `Error:` line** is what the other server actually said. It often tells you
  more, such as a link to a page explaining a block. Read it, and search for any
  code or link in it.

## What each bounce means

Each sentence below is exactly what REST Mail writes.

### Failed at once

The other server refused for good, so REST Mail did not retry.

| The bounce says | What it means | What to do |
|---|---|---|
| `The recipient's mailbox does not exist.` | The address is wrong, or the mailbox has closed | Check the spelling. Ask them for a current address |
| `The recipient's domain does not exist or accepts no mail.` | The part after the `@` is wrong, or that domain takes no mail | Check the spelling of the domain |
| `The recipient's server refused the message by its policy, for example as spam or for failing sender authentication.` | They do not trust the message or your server | Read the `Error:` line, then go to [Why mail goes to spam](why-mail-goes-to-spam.md#mail-you-send) |
| `The recipient's server refused the message.` | They refused and gave no usual reason | Read the `Error:` line. If it does not help, ask the recipient's administrator |

### Failed after five days of trying

These problems can clear up, so REST Mail keeps retrying, sooner at first and then
at most every few hours. It gives up after five days and then sends the bounce
([code](../../../internal/gateway/queue/retry.go)).

| The bounce says | What it means | What to do |
|---|---|---|
| `The recipient's mailbox is full.` | They have run out of space | Tell them another way |
| `The recipient's server kept asking for the message to be sent again later.` | They use greylisting, and it never let the message through | Ask the recipient's administrator |
| `The recipient's server is limiting how much mail it accepts from this server.` | You, or others on your server, sent a lot, or your reputation is low | Send less for a while. See [Why mail goes to spam](why-mail-goes-to-spam.md#mail-you-send) |
| `The recipient's server could not be reached.` | That server was down, or your server cannot reach anyone | See [below](#if-your-server-cannot-reach-anyone) |
| `A secure connection to the recipient's server could not be made.` | They require encryption, and it failed. Often their certificate is the problem | Ask the recipient's administrator |
| `The recipient's domain could not be looked up.` | Their DNS was failing | Check the domain, and try again later |
| `The recipient's server could not accept the message.` | Something temporary, with no usual reason | Read the `Error:` line |

### Sent to an address that bounced before

```text
Error: smtp; not sent: an earlier message to this address bounced as undeliverable, so mail from example.org to it is suppressed until 1 February 2027 18:17 UTC
```

When an address bounces because it does not exist, REST Mail stops mailing it from
your whole domain for 90 days. Repeatedly mailing dead addresses harms your
reputation ([more](../how-email-works/bounces-queues-retries.md)).

If the address works again, an administrator of the domain can clear it. There is
no screen for this yet. In a terminal, first find its `id`, then remove it. [This
step](setting-up-dkim-spf-dmarc.md#step-2-set-the-new-policy) shows how to set
`$SERVER` and `$TOKEN`, and `$DOMAIN_ID` is the number in the address bar of your
domain's page:

```sh
curl -fsS "$SERVER/api/v1/admin/domains/$DOMAIN_ID/suppressions" -H "Authorization: Bearer $TOKEN" | jq '.data[] | {id, address}'
curl -fsS -X DELETE "$SERVER/api/v1/admin/domains/$DOMAIN_ID/suppressions/$ID" -H "Authorization: Bearer $TOKEN"
```

## No bounce, and no delivery

**REST Mail does not tell you that a message is delayed.** While it is retrying,
you hear nothing, for up to five days. Being in your Sent folder only means your
message was accepted for sending. Showing senders what happened to each message is
planned ([#1568](https://github.com/rest-mail/rest-mail-server/issues/1568)).

So if a message seems lost, ask someone with whole-server admin access to look:

1. Open **Queue** in the admin app. It shows each message's recipient, sender,
   status, attempts and next attempt.
2. Find the recipient, and open the entry.
3. Read its status:

| Status | What it means |
|---|---|
| `PENDING` or `DELIVERING` | It is waiting, or being sent right now |
| `DEFERRED` | The last try failed, and it will try again. **Error Details** says why, and **NEXT ATTEMPT SCHEDULED** says when |
| `DELIVERED` | The other server accepted it. If the recipient cannot find it, it is on their side: ask them to check their spam folder |
| `BOUNCED` | It failed, and you should have a bounce |

**Retry Delivery** tries again straight away, which helps once you have fixed the
cause.

Bounces are limited too, so that a forged sender cannot flood a mailbox. If a great
many messages fail at once, you may not get a bounce for every one
([more](../how-email-works/bounces-queues-retries.md)).

## If your server cannot reach anyone

If mail to **everyone** stays `DEFERRED`, with errors like `all MX hosts failed:
connect to ...: dial tcp ...` in **Error Details**, your server probably cannot
reach other servers on port 25. Many providers block it by default.

[Check port 25](setting-up-your-first-domain.md#if-you-run-the-server-yourself) on
the server, then either ask your provider to lift the block or send through a
**relay**: a mail service that accepts your mail on another port and delivers it for
you.

A relay is set per domain, by a whole-server admin, and there is no screen for it
yet. `tls_mode` is `starttls` or `implicit`, and the rest comes from your relay
provider. To go back, send `{"transport":"direct"}`.

```sh
curl -fsS -X PUT "$SERVER/api/v1/admin/domains/$DOMAIN_ID/relay" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"transport":"relay","host":"smtp.relay.example","port":587,"tls_mode":"starttls","username":"you","password":"..."}'
```

**A relay changes your SPF record.** Mail then leaves from the relay's servers, not
yours, so add them as your provider tells you, in the form
`v=spf1 mx include:relay.example -all`. Otherwise your mail fails SPF.

## When other people cannot reach you

Ask them for the bounce they received. The `Error:` line will include what your
server replied.

| Your server replied | What it means | What to do |
|---|---|---|
| `550 5.1.1 No such user` | That address is not a mailbox here | Create it, or add an alias. It is also the reply for an account that does not receive mail |
| `451 4.3.0 Try again later` | Greylisting: REST Mail asks a new sender to retry | Nothing. A real mail server retries, and it gets through after 5 minutes. The other person sees only a short delay |
| `550 5.7.1 Rejected by policy` | A check refused it. Most often, their domain's DMARC policy says to refuse mail that fails its checks, and this mail failed | Tell them to check their own records. You cannot fix it from your side |
| `552 5.3.4 Max message size exceeded` | The message is over the size limit, 10 MB unless the person who runs your server changed it | Ask them to send a link, or smaller files |
| `452 4.2.2 Mailbox full` | That mailbox has reached its quota. Their server keeps trying | Delete some mail, or ask an administrator to raise the quota under **Mailboxes** |

If they get no answer at all, the problem is your DNS. Check your MX record and its
address:

```sh
dig +short MX example.org          # 10 mail.example.net.
dig +short A mail.example.net      # your server's address
nc -vz -w 5 mail.example.net 25    # from a network that does not block port 25
```

If your MX record has recently changed, some servers may still be using the old one
for a while ([why](../how-email-works/dns-for-email.md#changes-take-time)). If you
moved from another provider, mail may have gone there.

## Sources

- [Bounces, queues and retries](../how-email-works/bounces-queues-retries.md) and
  [DNS for email](../how-email-works/dns-for-email.md), with their sources.
- REST Mail: the bounce wording and failure kinds, in
  [`queue.go`](../../../internal/db/models/queue.go); how a reply is classified, in
  [`classify.go`](../../../internal/gateway/queue/classify.go); retries, in
  [`retry.go`](../../../internal/gateway/queue/retry.go); the bounce itself, in
  [`worker.go`](../../../internal/gateway/queue/worker.go); suppression, in
  [`suppression.go`](../../../internal/gateway/queue/suppression.go); relays, in
  [`relay.go`](../../../internal/api/handlers/relay.go); the size limit, in
  [`config.go`](../../../internal/config/config.go); the screens, in
  [`admin/src/routes/queue/`](../../../admin/src/routes/queue/).
