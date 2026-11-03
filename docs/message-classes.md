# Message classes

Every message response carries a `class`: `newsletter`, `receipt`, `notification`
or `person`. A client can use it to collapse list mail and to offer triage. The
class describes the shape of a message. It says nothing about who wrote the
message or whether it is safe, and anyone can send these headers.

## How the answer is reached

The first line that applies wins.

| Order | Class | Source |
| --- | --- | --- |
| 1 | `receipt` | A Sieve rule added the imap4flags keyword `$receipt` at delivery. |
| 2 | `newsletter` | One valid `List-Id` or one valid `List-Unsubscribe` field. |
| 3 | `notification` | `Auto-Submitted` is `auto-generated` or `auto-replied`, or `Precedence` is `bulk`, `list` or `junk`. |
| 4 | `person` | Anything else, including malformed signals and a field that appears more than once. |

Header names are case-insensitive and folded values are unfolded first. No sender
address, subject, body text, model or network lookup is involved, so the same
message always gets the same answer.

## What collapse may do

Only `newsletter` may collapse. A `person` message is never hidden, and neither
is any other class or a class a client does not recognize. A forged list header
on a person's mail can therefore cause that message to collapse behind an explicit
control, and nothing more. Classifying a message never moves, files or deletes it.

## Marking a receipt with a Sieve rule

No listed header says that a message is a receipt, so only a rule assigns this
class. A rule adds the keyword as it would any other (RFC 5232):

```sieve
require ["imap4flags"];
if address :is "from" "orders@shop.example" {
    addflag "$receipt";
}
```

The keyword name is case-insensitive. The class is recorded once with the message
at delivery, in the `rule_class` column, so changing or removing the script later
does not change the answer for mail already delivered. The rule runs only where
Sieve runs: SMTP, local and RESTMAIL delivery. Imports, IMAP APPEND and COPY, and
a message released from quarantine have no rule class. Other keywords, such as
`$newsletter`, have no effect, which means no rule can make a message collapse.

## When the class is worked out

The header part is worked out on read, as the authentication results are. It reads
the stored decoded headers if the row has them and otherwise the header section of
the stored source, because most delivery paths store only the source. Old mail
therefore gets its header class straight away, with no migration of its contents.

The rule part is written once at acceptance, as `sealed_hash` is. `rule_class` is
an additive nullable column with no backfill, because a rule's answer cannot be
reconstructed later. The seal does not cover it, as it does not cover flags.

A newsletter's `List-Unsubscribe` targets can be used from the webmail: see
[unsubscribing.md](unsubscribing.md).
