# Mobile capabilities: design

Status: not built. This is the contract the app implements against, not a
description of it. The app lives in its own repository; everything below either
exists in this API today or is a named change to it.

The point of the list is that the app is a composition of **capabilities**, not a
set of screens. A capability is a fact the server can state or an action it can
perform; a screen is one arrangement of several; a theme only changes how they
are drawn. If something below cannot be expressed as a fact plus a default plus
a degradation, it is not a capability and does not belong here.

Legend for **Status**:

- `ship` — the endpoint and the data already exist; the work is client-side.
- `derive` — the data exists but must be aggregated or computed; no schema change.
- `new` — a field, table, or endpoint has to be added to this API.
- `mobile` — push/OS infrastructure, outside this repository.

## 1. Message facts

| | Capability | Source | Status |
|---|---|---|---|
| `message.auth` | SPF/DKIM/ARC/DMARC verdicts for a received message, with the checked domain and identity | `Message.headers["Authentication-Results"]`, written at delivery from the `AuthResult` verdicts; typed form in `ExecutionResult.auth_results` | `derive` |
| `message.seal` | Digest of headers and body taken at receipt, to prove nothing changed after the server accepted it | new column on the message, set in the delivery path | `new` |
| `message.privacy` | Whether remote images were stripped, and whether the message carries tracking | `body_html` + client-side loading policy; `headers` for known trackers | `ship` |
| `message.verdict` | One derived state for the reader: `signed` / `unsigned` / `mismatch`, never "safe" | composition of auth + seal + ARC | `derive` |
| `message.note` | A private annotation on your own copy; never sent, never part of the message | on-device store first; `/messages/{id}/notes` if it must sync | `ship` |

Rules:

- Absent signature is **not** a failure. Forwards and mailing lists break DKIM
  legitimately; `dmarc=none` means the domain publishes no policy. Only an
  aligned `dmarc=fail` is `mismatch`.
- `message.verdict` must render in every composition, including ones that hide
  the rest of the chrome. A theme may choose the form (chip, margin note,
  bracket); it may not omit the state.

## 2. Thread facts

| | Capability | Source | Status |
|---|---|---|---|
| `thread.exists` | Grouping by `thread_id`, falling back to `references` then `in_reply_to` | `Message.thread_id`, `.references`, `.in_reply_to` | `ship` |
| `thread.awaiting` | The thread is waiting on someone else: last message is not from this account | derive over the thread; needs a thread read | `derive` |
| `thread.awaiting_since` | How long it has been waiting, for nudge and for "waiting 2d" | same | `derive` |
| `thread.due` | A declared deadline, plus where it came from | new field + `due_source: user \| rule \| parsed` | `new` |
| `thread.priority` | Ordering hint; only if `due`/`awaiting` prove insufficient | new field | `new` |
| `thread.conversation` | Quoted history collapses; subject becomes metadata rather than the row title | `in_reply_to` / `references` | `ship` |

Rules:

- `thread.awaiting` is derived, not inferred: last message sender ≠ account
  address. No model, no setting, always reproducible.
- A parsed `due` is a suggestion with a visible source. It never reorders a list
  until accepted, and it never files mail anywhere.
- Threads span folders. The list endpoints are folder-scoped, so this needs
  `GET /api/v1/accounts/{id}/threads/{thread_id}` rather than a client-side
  join across folders.

## 3. Inbox actions

| | Capability | Source | Status |
|---|---|---|---|
| `snooze.until` | Return time. The message leaves the folder and comes back, with an `events` wake | new `snoozed_until` on the thread/message | `new` |
| `send.later` | Scheduled send, visible and cancellable before it fires | `scheduled_at` on drafts; `POST /messages/draft/{id}/send` already exists | `new` |
| `bundle.class` | Newsletter / receipt / notification / person, for collapsing and for triage | `List-Id`, `List-Unsubscribe`, `Precedence`, plus `sieve` rules | `derive` |
| `quarantine.count` | Held mail as a first-class count, never folded into unread | `GET /accounts/{id}/quarantine` | `ship` |
| `quarantine.review` | Release or delete a held message, singly or in bulk | `POST .../quarantine/{mid}/release`, `DELETE .../quarantine/{mid}` | `ship` |
| `quarantine.reason` | Why it was held: SPF fail, DNSBL, score, greylist | stored filter verdicts on the held message | `derive` |
| `rule.trace` | Which rule moved this message, and how often that rule has run | matched rule id on the message; run counts from pipeline logs | `new` |
| `search.first` | Search is the retrieval model, not a fallback | `GET /accounts/{id}/search` | `ship` |
| `sync.live` | Server pushes changes rather than the client polling | `GET /accounts/{id}/events` (SSE) | `ship` |
| `sync.state` | Per-account connection and last-sync state, shown honestly when broken | `events` stream state + foldered fetches | `ship` |
| `flags` | Read, flagged, starred, draft, deleted | `UpdateMessageRequest` | `ship` |

Rules:

- Snooze hides from the folder; it does not delete. The SSE stream must emit the
  return so a second device sees it without a refresh.
- `bundle.class` never hides a person. Only list-shaped mail is collapsible.

## 4. Attention

| | Capability | Source | Status |
|---|---|---|---|
| `quiet.window` | Hours in which nothing notifies; held mail still arrives, silently | account setting | `new` |
| `notify.reply` | Quick actions inside the notification payload: reply, suggest another time, later | push payload built from `thread.awaiting` + contact | `mobile` |
| `notify.held` | A held message notifies once, as a decision, not as an alarm | quarantine + push | `mobile` |
| `digest.clear` | "Nothing needs you after 17:00" as a single statement | `thread.due` + `thread.awaiting` | `derive` |
| `presence.latency` | "Usually replies in ~2h", from this account's own history with that contact | aggregation over sent messages | `new` |

Rules:

- Notification actions must be complete operations. If "reply: works" cannot
  send from the payload, it must not be offered.
- `presence.latency` is off by default and is computed from your own history
  only; it is not a signal from the other side.

## 5. Agent

The agent is a client of the capabilities above, not a parallel system. It may
only use facts that already exist, and it may only act where it can be undone.

| | Capability | Source | Status |
|---|---|---|---|
| `agent.ledger` | Every autonomous action, ordered, attributed, reversible for a window | new table; mirrors the audit shape used for quarantine | `new` |
| `agent.trust` | Permission per capability: `suggest` / `approve` / `auto` | account setting | `new` |
| `agent.propose` | A draft or action with reason, confidence, and undo | `POST /messages/draft`, `POST /messages/draft/{id}/send` | `new` |
| `agent.triage` | Outcomes instead of a list: archived 14, drafted 2, held 1 | `agent.ledger` + `bundle.class` | `new` |
| `agent.ask` | A command surface over capabilities, not a chat | search + capability calls | `new` |

Rules, in order of importance:

1. **`agent.propose` refuses on `message.verdict = mismatch`.** Drafting replies
   to unauthenticated mail is a phishing amplifier.
2. **No send without `agent.ledger`.** If the action cannot be listed and undone,
   it cannot be automatic.
3. **`agent.triage` requires `bundle.class`.** Without clustering, "archived 14
   newsletters" is a list with extra steps.
4. `agent.trust` is per capability. Sending mail and filing newsletters are not
   the same permission.
5. An agent proposal always shows its reason and its confidence. A proposal
   without a reason is not reviewable, and if it cannot be reviewed it is not
   supervised.

## 6. Every composition must pass

These apply to any screen, in any theme, before it ships:

1. **200 items.** It still works with a full inbox, not the six messages in the
   mock.
2. **One thumb.** Primary action reachable one-handed; no drag required for
   something a tap can do.
3. **Search first.** Any object on screen is findable by typing; layout is never
   the only route to it.
4. **Screen-reader parity.** Nothing meaningful is conveyed only by position,
   colour, or gesture.
5. **Safety state.** `message.verdict`, quarantine counts, and sync failure are
   rendered in every composition.

## 7. Not doing

- **Spatial canvas.** Mail is a queue, not a space; retrieval is search, and a
  two-hundred-card pile is not an overview. Pen annotation survives alone, as
  `message.note`.
- **The lock screen or widget as the inbox.** Those are OS surfaces we do not
  own; they can start a session, not replace one. The actions survive as
  `notify.reply`.
- **A paper-delivery theme.** Two deliveries a day is a positioning choice for a
  personal inbox, not a layout. It needs its own product decision, not a theme.

## 8. Order of work

1. **P0 — no server change.** `message.auth` (parse the header), `message.privacy`,
   `message.note`, `thread.exists`, `thread.conversation`, `quarantine.*`,
   `search.first`, `sync.*`, `flags`.
2. **P1 — small API additions.** `threads/{id}`, `thread.awaiting`, `thread.due`,
   `snooze.until`, `send.later`, `bundle.class`, `message.seal`, `rule.trace`.
3. **P2 — attention.** `quiet.window`, `digest.clear`, `notify.*`, `presence.latency`.
4. **P3 — agent.** `agent.ledger` and `agent.trust` first, then `agent.propose`,
   then `agent.triage`, then `agent.ask`. Nothing in P3 starts before P1 is
   correct, because every agent rule above depends on it.

## 9. API changes this implies

Fields: `sealed_hash` (message), `due` + `due_source` (thread), `snoozed_until`
(thread), `scheduled_at` (draft), `matched_rule_id` (message), `priority`
(thread, optional).

Endpoints: `GET /accounts/{id}/threads/{thread_id}`,
`GET /accounts/{id}/threads?state=awaiting`, `POST /accounts/{id}/threads/{id}/snooze`,
`GET/POST /accounts/{id}/agent/ledger`, `PATCH /accounts/{id}/agent/trust`,
`GET /accounts/{id}/notes/{message_id}` (only if notes must sync).

Typed verdicts: return `AuthResult[]` on the message rather than requiring every
client to parse `Authentication-Results`. The schema already exists for
pipelines; reusing it keeps one definition of a verdict.
