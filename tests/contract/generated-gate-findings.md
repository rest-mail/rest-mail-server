# Generated contract gate failure, run 40652763740

[CI run](https://github.com/rest-mail/rest-mail-server/actions/runs/40652763740/job/122696539690), tested merge commit `0ee73213166303d5d6b5d6861d0546dcb717ade9` (PR head `0945bbefe1e4acbf3d66229a07672a0b2475e313`, main `47b7fc7c55001e868c1e9d7ca50126d3def100b4`).

## Required gate

The examples phase failed only `sendDraft`'s 201 transaction. The published `sent` pair had a path but no body. Main's #1285 added an optional request body, so Vertrag synthesized `{"list_unsubscribe":{"https":"","mailto":""}}`. The handler correctly refused those empty unsubscribe targets with 400. The resulting surface failure was a second consequence of that same failed exchange: 144/145 operations passed.

The fix gives `sendDraft/sent` an explicit empty object body. The generated description now publishes that named request as `{}`, and the existing lifecycle checks exercise it through the real handler. The optional-body implementation from #1285 is also present on the branch so the generated-description regression can run before merging. No refusal validation or coverage baseline entry changes.

Coverage did not execute: `run.sh` stopped at the failed examples command under `set -e`. The artifacts contain no coverage report, and the log contains no coverage boundary findings. It would be incorrect to claim that coverage passed or that its remaining findings are known from this run.

## Report-only diagnostics

The full command continued into authorization and ownership reports after the required gate failed. These reports are explicitly not gates. The inventory below includes every failed/error JUnit case, including repeated source examples and the three queue link-resolution findings.

The authorization matrix assigns no roles to ordinary/app-password principals even though they retain capabilities on their own mailbox. Its flat role expectations cannot express the server's resource-scoped permissions or concealed-resource 404 policy; `authorization.py` documents that limitation. In particular, own-account Sieve and scheduled-message access cannot be made to return 403 merely to satisfy this matrix without changing the authorization policy. A decision to change that policy must identify which own-mailbox capabilities these principals should lose, or the matrix must gain resource-scoped expectations. This patch does neither.

Other diagnostic failures reflect reused or consumed fixtures: the held queue entry is consumed by the preceding examples run; the native refresh token is rotated and then reused for matrix principals; the pipeline is deleted before its preview is replayed; account/contact/alias creates repeat state from preceding processes; the mailbox-session success replay uses illustrative mailbox 1 rather than a surviving fixture mailbox. The account-link app-password error occurs in setup: unlinking uses Basic credentials on a person-only route and receives 401 before the actual probe. These require separate fixture work with probe-preservation tests, rather than changing refusal probes or weakening server checks. The draft-send source/admin failures share the required gate's fixed missing-body cause.

### Examples

| Transaction/finding | Failure |
|---|---|
| /api/v1/messages/draft/{id}/send > Send a draft > 201 > application/json | statusCode: Expected status code '201', but got '400'. |
| API surface | The run passed 144 of 145 operation(s), 99.3%, below the 100.0% it requires. |

### Authorization

| Transaction/finding | Failure |
|---|---|
| /api/v1/messages/draft/{id}/send > Send a draft > 201 > application/json | statusCode: Expected status code '201', but got '400'. |
| links: /api/v1/admin/queue > List outbound queue entries > 200 > application/json · bounceQueueEntry | link bounceQueueEntry claims id is "$response.body#/data/items/0/id", and the response of /api/v1/admin/queue > List outbound queue entries > 200 > application/json carries no such value |
| links: /api/v1/admin/queue > List outbound queue entries > 200 > application/json · getQueueEntry | link getQueueEntry claims id is "$response.body#/data/items/0/id", and the response of /api/v1/admin/queue > List outbound queue entries > 200 > application/json carries no such value |
| links: /api/v1/admin/queue > List outbound queue entries > 200 > application/json · retryQueueEntry | link retryQueueEntry claims id is "$response.body#/data/items/0/id", and the response of /api/v1/admin/queue > List outbound queue entries > 200 > application/json carries no such value |
| authorization: POST /api/v1/accounts/link as app-password | before hook: beforeEach hook: HTTPError: HTTP Error 401: Unauthorized |
| authorization: GET /api/v1/accounts/{id} as ordinary | refused ordinary with 404 where the description requires 403 (the credential lacks mail:read for BearerAuth; its ids come from GET /api/v1/accounts, sent as admin) |
| authorization: GET /api/v1/accounts/{id} as app-password | refused app-password with 404 where the description requires 403 (the credential lacks mail:read for BasicAuth; its ids come from GET /api/v1/accounts, sent as admin) |
| authorization: GET /api/v1/accounts/{id}/credentials as ordinary | refused ordinary with 404 where the description requires 403 (the credential lacks mailbox:manage for BearerAuth; its ids come from GET /api/v1/accounts, sent as admin) |
| authorization: POST /api/v1/accounts/{id}/credentials as ordinary | refused ordinary with 404 where the description requires 403 (the credential lacks mailbox:manage for BearerAuth; its ids come from GET /api/v1/accounts, sent as admin) |
| authorization: DELETE /api/v1/accounts/{id}/credentials/{credential_id} as ordinary | refused ordinary with 404 where the description requires 403 (the credential lacks mailbox:manage for BearerAuth; its ids come from GET /api/v1/accounts → POST /api/v1/accounts/{id}/credentials, sent as admin) |
| authorization: GET /api/v1/accounts/{id}/search{?q*,folder*,from*,after*,before*,has_attachment*,limit*} as ordinary | refused ordinary with 404 where the description requires 403 (the credential lacks mail:read for BearerAuth; its ids come from GET /api/v1/accounts, sent as admin) |
| authorization: GET /api/v1/accounts/{id}/search{?q*,folder*,from*,after*,before*,has_attachment*,limit*} as app-password | refused app-password with 404 where the description requires 403 (the credential lacks mail:read for BasicAuth; its ids come from GET /api/v1/accounts, sent as admin) |
| authorization: GET /api/v1/accounts/{id}/sieve as ordinary | authorization bypass: GET /api/v1/accounts/{id}/sieve answered 200 for ordinary where the description requires 403 (the credential lacks mail:read for BearerAuth) |
| authorization: DELETE /api/v1/accounts/{id}/sieve as ordinary | authorization bypass: DELETE /api/v1/accounts/{id}/sieve answered 200 for ordinary where the description requires 403 (the credential lacks mail:write for BearerAuth; its ids come from GET /api/v1/accounts/{id}/sieve, sent as admin) |
| authorization: POST /api/v1/admin/pipelines/test as admin | statusCode: Expected status code '200', but got '404'. |
| authorization: GET /api/v1/attachments/{id} as ordinary | refused ordinary with 404 where the description requires 403 (the credential lacks mail:read for BearerAuth) |
| authorization: GET /api/v1/attachments/{id} as app-password | refused app-password with 404 where the description requires 403 (the credential lacks mail:read for BasicAuth) |
| authorization: POST /api/v1/auth/refresh as admin | statusCode: Expected status code '200', but got '401'. |
| authorization: POST /api/v1/auth/refresh as ordinary | statusCode: Expected status code '200', but got '401'. |
| authorization: POST /api/v1/auth/refresh as app-password | statusCode: Expected status code '200', but got '401'. |
| authorization: POST /api/v1/messages/draft as ordinary | authorization bypass: POST /api/v1/messages/draft answered 201 for ordinary where the description requires 403 (the credential lacks mail:write for BearerAuth) |
| authorization: POST /api/v1/messages/draft as app-password | authorization bypass: POST /api/v1/messages/draft answered 201 for app-password where the description requires 403 (the credential lacks mail:write for BasicAuth) |
| authorization: POST /api/v1/messages/draft/{id}/send as admin | statusCode: Expected status code '201', but got '400'. |
| authorization: GET /api/v1/messages/scheduled as ordinary | authorization bypass: GET /api/v1/messages/scheduled answered 200 for ordinary where the description requires 403 (the credential lacks mail:send for BearerAuth) |
| authorization: GET /api/v1/messages/scheduled as app-password | authorization bypass: GET /api/v1/messages/scheduled answered 200 for app-password where the description requires 403 (the credential lacks mail:send for BasicAuth) |
| authorization: DELETE /api/v1/messages/scheduled/{id} as ordinary | refused ordinary with 404 where the description requires 403 (the credential lacks mail:send for BearerAuth) |
| authorization: DELETE /api/v1/messages/scheduled/{id} as app-password | refused app-password with 404 where the description requires 403 (the credential lacks mail:send for BasicAuth) |

### Account ownership

| Transaction/finding | Failure |
|---|---|
| /api/v1/accounts > Create an account > 201 > application/json | statusCode: Expected status code '201', but got '409'. |
| /api/v1/accounts/{id}/contacts > Create a new contact > 201 > application/json | statusCode: Expected status code '201', but got '409'. |

### Domain ownership

| Transaction/finding | Failure |
|---|---|
| /api/v1/admin/aliases > Create a new alias > 201 > application/json | statusCode: Expected status code '201', but got '409'. |

### Mailbox ownership

| Transaction/finding | Failure |
|---|---|
| /api/v1/admin/mailboxes/{id}/sessions > Sign an account out everywhere > 204 | statusCode: Expected status code '204', but got '404'. |
