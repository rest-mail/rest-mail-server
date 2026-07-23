# Open Security Items — 2026-07-23

Live register of **unresolved** security items carried forward from the point-in-time
review docs in this folder. This is the maintained doc; the dated review files are
historical and, once fully processed + captured here, can be deleted.

## Open items

### OSI-1 — `/restmail/mailboxes` recipient enumeration
- **Status:** decision pending (accept-by-design vs harden).
- **Detail:** `/restmail/mailboxes?address=` reveals recipient existence. It is the
  server-to-server `RCPT TO` equivalent — protocol-inherent (refusing to answer breaks
  legitimate delivery). The sibling `/api/mailboxes` is closed by internal mTLS (#65).
- **Decision needed:** accept as protocol-inherent, **or** harden with greylist/tarpit
  on that path — **not** per-IP rate caps (which would break legitimate bulk
  server-to-server delivery).
- **Origin:** `codex-security-review-2026-04-23` (SR-004).

### OSI-2 — Internal mTLS is default-off
- **Status:** on roadmap (secure-by-construction epic).
- **Detail:** the gateway→API machine routes (`/api/v1/messages/deliver`,
  `/api/mailboxes`) only fully close when `INTERNAL_MTLS_ENABLED=true`. The mechanism
  exists (#65); the residual is that it is opt-in.
- **Action:** make mTLS mandatory — auto-provision the internal CA + certs in
  `instance:new`, default new instances on, and boot-refuse when enabled-but-uncertified.
  Pre-enable gate: a live `task e2e` run with `internal_mtls: true`.
- **Origin:** `codex-security-review-2026-04-23` (SR-003 / SR-004).

## Pending intake

These reviews still need a re-evaluation against current `main` before their items can
be captured here and their files retired (scheduled — not yet done):

- `kimi-security-review.md`
- `opus-security-review-2026-04-23.md`

The codex trio — `codex-security-review-2026-04-23.md` (deep-dive),
`…-board-2026-04-23.md`, `…-summary-2026-04-23.md` — is **fully resolved** (struck
through in #77 / #80); only OSI-1 and OSI-2 above carry forward from it.

## Retirement

Once a source review is fully resolved (every finding struck through) and its open items
are captured above, its dated file can be deleted.
