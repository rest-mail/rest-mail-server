# What happened to a sent message, recipient by recipient

A sender can ask what happened to each copy of a message they sent:

```
GET /api/v1/messages/{id}/recipients
```

It needs the same `mail:read` access as reading the message. Another account's
message is `404 message_not_found`, as for every message route. A message the
account received rather than sent answers an empty `recipients` list.

Each recipient in `data.recipients` has:

| Field | Meaning |
| --- | --- |
| `address` | The recipient. |
| `delivery` | `remote` (sent to another server through the outbound queue) or `local` (a mailbox on this server). |
| `state` | `queued`, `retrying`, `delivered`, `failed` or `unknown`. |
| `reason` | Why it is retrying, failed or unknown, in a sentence derived from the failure category. |
| `category` | The failure category of the last attempt (#142), when one failed. |
| `attempts` | Delivery attempts made so far; 0 for a local recipient. |
| `last_attempt_at` | When the last attempt was made. |

## Where the state comes from

A remote recipient is read from its outbound queue row:

| Queue status | `state` |
| --- | --- |
| `pending`, `delivering` | `queued`, or `retrying` once an attempt has failed |
| `deferred` | `retrying` |
| `delivered` | `delivered` |
| `bounced`, `expired` | `failed` |

A local recipient has no queue row. It is `delivered` when the copy the send put
into that mailbox is still there, and `unknown` otherwise (the copy was deleted
for good, or local delivery refused it; a refusal is not recorded). Only a copy
from the account's own address counts, so a message made to look sent, such as
an import, cannot be used to ask whether another mailbox holds a message.

A Bcc recipient is not in the Sent copy's headers. A remote one is listed from
its queue row; a local one is not listed.

## What is withheld

The remote server's own reply, the queue row's `last_error` and
`last_error_code`, is never returned. It is written for the sending server and
can carry internal host names, addresses and policy details of the recipient's
side. The sender gets the reason derived from its category instead. The queue's
envelope sender, next attempt time and retry budget are left out as well; an
administrator still sees all of it in `/api/v1/admin/queue`.
