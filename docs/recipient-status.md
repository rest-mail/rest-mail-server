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
| `delivery` | `remote` (a recipient on another server) or `local` (a mailbox on this server). Either goes through the outbound queue. |
| `state` | `queued`, `retrying`, `delivered`, `failed` or `unknown`. |
| `reason` | Why it is retrying, failed or unknown, in a sentence derived from the failure category. |
| `category` | The failure category of the last attempt (#142), when one failed. |
| `attempts` | Delivery attempts made so far; 0 for a recipient with no queue row. |
| `last_attempt_at` | When the last attempt was made. |

## Where the state comes from

Every send puts every recipient on the outbound queue, on this server or
elsewhere (#1564), so a recipient is read from its queue row:

| Queue status | `state` |
| --- | --- |
| `pending`, `delivering` | `queued`, or `retrying` once an attempt has failed |
| `deferred` | `retrying` |
| `delivered` | `delivered` |
| `bounced`, `expired` | `failed` |

A To or Cc recipient with no queue row and a mailbox on this server is `delivered`
when a copy from the sender is in that mailbox, and `unknown` otherwise. Only a
copy from the account's own address counts, so a message made to look sent, such
as an import, cannot be used to ask whether another mailbox holds a message.

A Bcc recipient is not in the Sent copy's headers. It is listed from its queue
row.

## What is withheld

The remote server's own reply, the queue row's `last_error` and
`last_error_code`, is never returned. It is written for the sending server and
can carry internal host names, addresses and policy details of the recipient's
side. The sender gets the reason derived from its category instead. The queue's
envelope sender, next attempt time and retry budget are left out as well; an
administrator still sees all of it in `/api/v1/admin/queue`.
