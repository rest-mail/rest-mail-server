# API conventions

What a route should answer, for a developer adding one. This file records only what the owner has decided, each rule with its decision and a test. Everything else is listed under [Still open](#still-open), unsettled. [#672](https://github.com/rest-mail/rest-mail-server/issues/672) and [#622](https://github.com/rest-mail/rest-mail-server/issues/622) hold the decisions, and [#1165](https://github.com/rest-mail/rest-mail-server/issues/1165) holds the enforcement.

## Decided

| When | Answer | Owner's decision | Test |
|---|---|---|---|
| The body is not valid JSON, or not the declared media type | 400 `bad_request` | [2026-10-28, #672](https://github.com/rest-mail/rest-mail-server/issues/672#issuecomment-6466869752) | `TestRegisteredRoutesAnswer` |
| The body parses but breaks a rule, including one the request schema states | 422 `validation_failed`, with the failing fields | [2026-10-28, #672](https://github.com/rest-mail/rest-mail-server/issues/672#issuecomment-6466869752) | `TestRegisteredRoutesAnswer`, `TestBulkQueue_SelectionBoundaries`, `TestDomainCreate_EmptyNameContract` |
| Someone else's account, or an object in it, with no link to the caller | 404 | [2026-11-01, #672](https://github.com/rest-mail/rest-mail-server/issues/672#issuecomment-6563382794) | `TestAccountRefusals_ANoLinkCallerCannotTellAnAccountExists`, `TestMailStateRequirementExamples/updateDraft/foreign_owner` |
| The account or object does not exist | 404, identical to the row above | [2026-11-01, #672](https://github.com/rest-mail/rest-mail-server/issues/672#issuecomment-6563382794) | `TestAccountRefusals_ANoLinkCallerCannotTellAnAccountExists` |
| The caller can see the account or object, and this action is not allowed | 403 | [2026-11-01, #672](https://github.com/rest-mail/rest-mail-server/issues/672#issuecomment-6563382794) | `TestMailStateRequirementExamples/updateMessage/read_only`, `TestMailStateRequirementExamples/releaseQuarantine/read_only` |
| The caller lacks the role for an admin route | 403 | [2026-11-01, #672](https://github.com/rest-mail/rest-mail-server/issues/672#issuecomment-6563382794) | `TestRequireCapability_AdminMissingCapability` |
| An endpoint returns a list and there is no data | `[]`, never `null` | [2026-10-23, #622](https://github.com/rest-mail/rest-mail-server/issues/622), under Decided | `TestRegisteredRoutesAnswer` (the `/empty` case) |

### Bad input: 400 or 422

The owner's words:

> **400 `bad_request`** only for a body that is not valid JSON, or not the declared media type.
>
> **422 `validation_failed`**, with the failing fields, for everything that parses but breaks a rule, including rules the request schema states (required fields, `minItems`, `anyOf` alternatives such as the bulk queue's "ids or filter").

A registered route gets this from `endpoint.Register`: state the rule on the request type and binding answers for you. Return `endpoint.Invalid` for a rule only the handler can check. [#692](https://github.com/rest-mail/rest-mail-server/pull/692) and [#1232](https://github.com/rest-mail/rest-mail-server/pull/1232) built it.

Sender-policy and scheduling refusals follow the same field-bearing error shape: `list_unsubscribe`, `list_unsubscribe.https` or `scheduled_at`. A TLS report with no hosted policy domain names `policies`. The checks that require database state still run in the handler; they do not become schema constraints.

### Refusals: 403 or 404

The owner's words:

> If the caller is allowed to know the thing exists, a refusal is 403. If not, it is 404.

The 404 stops a stranger learning which accounts are hosted, because the answer is the same whether or not the account exists. RFC 9110 section 15.5.4 permits it, as the decision notes. For a route on a mailbox, call `mayReach`. It refuses with `errNoAccess`, which your route answers as its 404, or `errNotAllowed`, which is already the 403. [#1431](https://github.com/rest-mail/rest-mail-server/pull/1431) built it.

Composing, forwarding, sending a draft and releasing held mail preserve that distinction when resolving the sender too. An unknown sender and one with no link to the caller both answer the identical `404 not_found` account error. A visible sender without the selected sending capability still answers 403. `TestSendingRefusalsHideUnknownSendersButExplainVisibleOnes` proves the byte equality and keeps refused drafts and holds intact.

### Lists: never null

The owner's words:

> **A list is never null** (2026-10-23, owner; settles Open 12). An endpoint that returns a list returns `[]` when there is no data. The registry's response binding guarantees it (antimatter-studios/go-oapifly#28).

A registered route gets this from `oapifly.EmptyLists`, which the endpoint writer calls on every response. go-oapifly's own test is `TestEmptyListsEncodeAsEmpty`.

### Where the tests are

- `TestRegisteredRoutesAnswer`: `internal/api/endpoint/endpoint_test.go`
- `TestBulkQueue_SelectionBoundaries`: `internal/api/handlers/contract_queue_selection_test.go`
- `TestDomainCreate_EmptyNameContract`: `internal/api/handlers/domain_create_contract_test.go`
- `TestAccountRefusals_ANoLinkCallerCannotTellAnAccountExists`: `internal/api/account_refusal_cells_test.go`
- `TestMailStateRequirementExamples`: `internal/api/requirement_mail_examples_test.go`
- `TestRequireCapability_AdminMissingCapability`: `internal/api/middleware/middleware_test.go`

## Where the code disagrees

These were found while writing this file, by reading the handlers' 400, 403 and 422 refusals, the 400 and 422 pairs in `openapi-example.json`, and the generated description's list fields. That is a search, not a proof of every route, so there may be more. No behaviour was changed, and none of these is settled here.

**A list that answers `null`.** `getDashboardStats` answers `"recentActivity": null` when there is no recent activity, and `TestDashboardStatsKeepsNullRecentActivity` pins it. The field is a `*[]RecentActivity`, which `EmptyLists` leaves alone. The generated description also declares these response lists nullable: `MessageTrace.stages`, `TestProbeResult.mx_records` and the seven lists of `TestSnapshot`. Whether those ever answer `null` has not been checked.

**A 422 that is not `validation_failed`, or has no fields.** The decision says 422 `validation_failed`, with the failing fields. These answer otherwise:

- `mailbox_full`, from `deliverMessage` and `forwardMessage`
- `undeliverable`, from `restmailDeliver`

Some of these turn on stored state, which may place them under the stored-state question below.

**A 400 outside the decision, and not on #1165's stored-state list.**

- `credential_required`, from `openGatewaySession`, for a body without the mail client's address and password
- "invalid organizer address in calendar event", from `respondToCalendar`, about a stored message
- The OAuth token route's `invalid_request`, `invalid_scope` and `unsupported_grant_type`, in the error shape RFC 6749 section 5.2 defines, some for a form that parses
- The script routes' `scriptExecutionResponses`, which can pass on the JavaScript sidecar's own 400

**A malformed path id.** `DELETE /api/v1/devices/{device_id}` answers 404 for an id that is not a number (`TestDeviceRegister_UnknownFieldsAreRefused`), where [#692](https://github.com/rest-mail/rest-mail-server/pull/692) made other routes answer 422. The decision speaks of the body, so it may not reach this.

**A domain-scoped admin outside its domain.** `RequireCapability` answers 403 for a mailbox or domain outside the admin's scope, and for a target that does not exist (`TestRequireCapability_ScopedGrantCoversItsTargetOnly`). `createAlias`, `createAccount` and the pipeline routes refuse the same way in the handler. The decision has a 404 row for an account with no link to the caller and a 403 row for an admin route without the role, and does not say which applies here.

**Draft edits and calendar replies.** `saveDraft`, `updateDraft` and `respondToCalendar` still collapse an unknown or unlinked sender and a visible sender lacking the action into 403. The first two overlap the open draft-description PR #1560, and the calendar reply overlaps #1610. They are left for those routes' owners to reconcile with the 2026-11-01 decision; composing, forwarding and sending a draft no longer collapse the two refusals.

The legacy `openapi.yaml` description and regenerated client comments for `accountEvents` now agree with its existing 404 for a missing or inaccessible account, and 403 when the caller can see the account without mail:read (`TestMailStateRequirementExamples/accountEvents/inaccessible`).

## Still open

The owner has not answered these. [#1165](https://github.com/rest-mail/rest-mail-server/issues/1165) says: "preserve current API behavior where #622, #625, #646, #661 or #672 has no explicit answer." That covers existing routes. For a new route there is no rule to follow, so raise the question on its issue, and don't treat what you chose as the convention.

The first eight rows are the unsettled rows of the table in [#672](https://github.com/rest-mail/rest-mail-server/issues/672), whose checklist says: "Settle delete/idempotence statuses, list/pagination envelopes, naming, identifiers, dates, unknown fields, create-or-replace statuses and the error-code registry". Each quotes that table's question and the answers it found when the API was mapped.

| Topic | The question, quoted | Issue |
|---|---|---|
| Delete and idempotence statuses | "Delete": "204; 200 `{status: "deleted"}`; 200 or 204 for an id that does not exist (pipelines, custom filters, queue, bans)" | [#672](https://github.com/rest-mail/rest-mail-server/issues/672) |
| List and pagination envelopes | "Lists": "`{data, pagination}` with a cursor; `limit`/`offset` with `total`; `before_seq` (audit); the queue's own `{items, total, limit, offset}`; silent clamping of `limit` (messages)" | [#672](https://github.com/rest-mail/rest-mail-server/issues/672) |
| Naming | "Naming": "snake_case everywhere except the dashboard stats (camelCase)" | [#672](https://github.com/rest-mail/rest-mail-server/issues/672) |
| Identifiers | "Ids": "integers, except push device ids (a string in responses, an integer in the path)" | [#672](https://github.com/rest-mail/rest-mail-server/issues/672) |
| Dates | "Dates": "RFC 3339 `date-time`, except `certResponse` (preformatted strings)" | [#672](https://github.com/rest-mail/rest-mail-server/issues/672) |
| Unknown request fields | "Unknown request fields": "silently ignored in 49 of 51 handler files (#658)". See [below](#unknown-request-fields). | [#672](https://github.com/rest-mail/rest-mail-server/issues/672) |
| Create-or-replace statuses | "Success for create-or-replace": "200 and 201 (Sieve, MTA-STS, bans, contact block)" | [#672](https://github.com/rest-mail/rest-mail-server/issues/672) |
| Error-code registry | "Error codes": "68 distinct `code` strings, with no list of them" | [#672](https://github.com/rest-mail/rest-mail-server/issues/672) |
| Stored-state refusals | "whether these stored-state refusals should answer 409, 422 or stay 400 needs an owner decision." They are "retryQueueEntry already_bounced, sendDraft no_recipients, the calendar reply when the message has no calendar events, testCustomFilter when the stored filter has no script, and the two-factor confirm and disable refusals." | [#1165](https://github.com/rest-mail/rest-mail-server/issues/1165#issuecomment-6490693718) |
| Authorization and validation order | "**Order: authorization before validation.** Account routes check access in the handler, after binding (`handlerChecked`). Admin routes check it in middleware, before binding." | [#625](https://github.com/rest-mail/rest-mail-server/issues/625#issuecomment-6382834445) |
| Missing object and body validation order | "**Order: does the object exist before its body is valid?** `updatePipeline` answers 404 for a missing pipeline before it looks at `filters`. Binding first answers 422." | [#625](https://github.com/rest-mail/rest-mail-server/issues/625#issuecomment-6382834445) |
| Item error paths | "**Where an item's error is reported.**" The handler reports "`filters: must be an array of filter objects` for any bad item", and the binder reports "the path, `filters[0]: must be an object`". | [#625](https://github.com/rest-mail/rest-mail-server/issues/625#issuecomment-6382834445) |
| What the served description covers | "**Decide what the served document describes:** only the routes this server has in its configuration (the document is generated on demand), or every route with a note." | [#661](https://github.com/rest-mail/rest-mail-server/issues/661) |

### Unknown request fields

The sources do not line up, so this stays open:

- [#622](https://github.com/rest-mail/rest-mail-server/issues/622) lists "closed schemas, strict request binding, and a scan for any secret echoed (#658)" under Decided, dated 2026-10-23.
- [#658](https://github.com/rest-mail/rest-mail-server/issues/658) describes that binding as one that "refuses unknown fields with 422 `validation_failed` naming them".
- [#672](https://github.com/rest-mail/rest-mail-server/issues/672), consolidated on 2026-10-26, still lists unknown fields to settle, and says "No recommendation becomes Decided through consolidation."

In the code, `TestAllRegisteredJSONRequestTypesRejectUnknownFields` (`internal/api/closed_contract_test.go`) showed 79 registered JSON request bodies answering that 422 when this was written. `receiveTlsReport` is the one open format it found. The issues do not say whether #622's entry answers #672's row.
