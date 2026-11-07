---
title: Why mail goes to spam
part: Using a REST Mail domain
summary: Why the mail you send lands in other people's spam folders and what you can do about it, and where mail sent to you goes when REST Mail holds it back.
---

# Why mail goes to spam

There are two questions here. If **mail you send** lands in someone else's spam
folder, that provider made the decision, and you can only influence it. If **mail
sent to you** seems to have vanished, that is usually something you can check
yourself.

## Mail you send

No provider tells you exactly how it decides, but they do publish what they expect
([Google](https://support.google.com/a/answer/81126)). A correct setup is required
and is still not enough: a new server can do everything right and land in spam for
a while ([why](../how-it-goes-wrong/deliverability.md)). Go through these in order,
because the first ones are the easiest to fix.

### 1. Do your records pass?

Send a message to an account at another provider, and read its headers. In Gmail,
open the message, then the **More** menu beside **Reply**, and choose **Show
original** ([Google](https://support.google.com/mail/answer/29436)). You want SPF,
DKIM and DMARC to pass. If one does not, [fix that
first](setting-up-dkim-spf-dmarc.md#check-they-work). Gmail requires SPF or DKIM of
every sender, and all three of anyone sending more than 5,000 messages a day to Gmail
accounts ([Google](https://support.google.com/a/answer/81126)).

### 2. Does reverse DNS agree both ways?

Your server's address must name a host, and that host must point back to the same
address. Gmail requires this. It is easy to miss, because you set it with whoever
owns the address and not in your domain's DNS. The person who runs your server can
check it
([how](setting-up-your-first-domain.md#if-you-run-the-server-yourself)).

### 3. Is your address on a blocklist?

Blocklists are lists of addresses that have sent spam, or that should not be sending
mail at all. Look up your server's address at
[Spamhaus](https://check.spamhaus.org/). Addresses given to home internet connections
are often listed just for being what they are
([Spamhaus PBL](https://www.spamhaus.org/blocklists/policy-blocklist/)).

If you are listed, fix the cause first, such as a stolen password, and then follow the
list's own steps for removal. A person who runs a server can also have REST Mail
[watch for listings](../../dnsbl-monitor.md).

### 4. Are you new?

A new domain or address has no reputation, and small servers start behind. Build one
slowly:

- **Start small.** Google's advice is to "Increase sending volume slowly". REST Mail
  has no warm-up schedule for you
  ([#1650](https://github.com/rest-mail/rest-mail-server/issues/1650)), so it is up to
  you: begin with people who expect your mail and will reply.
- **Look at what providers see.** [Google Postmaster
  Tools](https://postmaster.google.com/) shows your spam rate at Gmail, and Microsoft's
  [Smart Network Data Services](https://sendersupport.olc.protection.outlook.com/snds/)
  shows Outlook.com's view of your address.

### 5. Do people want it?

Providers watch how many people mark your mail as spam. Google asks senders to keep the rate
reported in Postmaster Tools below 0.10%, and to avoid ever reaching 0.30%
([Google](https://support.google.com/a/answer/81126)).

- Send only to people who asked for it. Do not buy address lists.
- Stop mailing addresses that bounce. REST Mail
  [does this for you](troubleshooting-bouncing-mail.md#sent-to-an-address-that-bounced-before).
- For newsletters, make unsubscribing easy
  ([Bulk mail](../security/bulk-mail.md)).
- Keep kinds of mail apart if you can: a newsletter's complaints should not hold up
  your invoices.

### 6. What does the message look like?

Providers also judge the message itself, and they do not publish how. Send a plain
test message from the same address. If that arrives and your real one does not, the
content is the likely cause.

### If you share a server

Providers judge both your domain and the address your mail leaves from
([more](../how-it-goes-wrong/deliverability.md#what-a-provider-looks-at)). Everyone on
your server shares that address, so someone else's spam can affect you. If everything above looks right, tell the person who runs your server.

## Mail coming to you

### What REST Mail does by default

REST Mail checks every incoming message's SPF, DKIM and DMARC. It does **not** look at
what the message says, and it has no spam folder of its own, unless the person who runs
your server added a content scanner such as Rspamd
([more](../security/spam-defences.md#how-rest-mail-handles-it)). What happens depends on
the sender's own DMARC policy:

| The sender's domain says | The message fails its checks | What happens |
|---|---|---|
| `p=reject` | Yes | Refused with `550 5.7.1 Rejected by policy`. You never see it |
| `p=quarantine` | Yes | Held in quarantine. It is **not** in your inbox |
| `p=none`, or nothing | Either | Delivered to your inbox |

So obvious spam from a domain with no records can reach your inbox. If that is too much,
ask the person who runs your server about content scanning, and turn on your mail app's
own junk filter.

Mail from a sender REST Mail has not seen before is also delayed by about five minutes
while it asks the other server to try again. That is not spam filtering going wrong, and
the mail should still arrive ([more](../security/spam-defences.md)).

### Finding a message that has been held

Held messages do not appear in your mail app. You can see them only in the webmail.

1. Sign in to the webmail, and choose `quarantine` at the bottom left.
2. Each entry shows the sender, the subject and the reason, such as `dmarc_quarantine`.
3. Press **Release** to put it in your inbox, or the bin icon to delete it for good.

**Releasing needs two-factor authentication.** Without it you will see: `This action needs a
session signed in with two-factor authentication: sign in again with your code`. Set up an
authenticator under the menu at the top right, `settings`, then **Security**, and sign in
again. Your mail app will then need an [app
password](connecting-your-mail-app.md#if-you-use-two-factor-authentication).

If you have held mail, you also get a daily message headed `Quarantine Digest`, listing
it. Be careful before you release anything. The message failed its sender's own checks, so it
may be a forgery. If a real sender's mail keeps being held, tell them: they need to fix
their records.

### If a message is not there either

- **Ask the sender for the bounce.** If they got one,
  [look up what it means](troubleshooting-bouncing-mail.md#when-other-people-cannot-reach-you).
- **If they got no bounce,** look in your mail app's own junk folder, and check any rules
  you set up.
- **Check your MX record** points to your mail host.

## Sources

- Google: [Email sender guidelines](https://support.google.com/a/answer/81126),
  [Trace an email with its full headers](https://support.google.com/mail/answer/29436),
  [Postmaster Tools](https://postmaster.google.com/).
- Microsoft: [Smart Network Data Services](https://sendersupport.olc.protection.outlook.com/snds/).
- Spamhaus: [Reputation Checker](https://check.spamhaus.org/),
  [Policy Blocklist](https://www.spamhaus.org/blocklists/policy-blocklist/).
- [Deliverability](../how-it-goes-wrong/deliverability.md) and [Spam
  defences](../security/spam-defences.md), with their sources.
- REST Mail: the default filters, in [`templates.go`](../../../internal/pipeline/templates.go);
  DMARC, in [`dmarc_check.go`](../../../internal/pipeline/filters/dmarc_check.go); greylisting,
  in [`greylist.go`](../../../internal/pipeline/filters/greylist.go); the quarantine screen, in
  [`QuarantineView.tsx`](../../../webmail/src/components/settings/QuarantineView.tsx).
