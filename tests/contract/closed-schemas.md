# Closed schemas and request binding

`TestEveryGeneratedObjectIsClosedOrHasARecordedReason` checks every generated
component and inline request, response, parameter and header schema. Objects
must set `additionalProperties: false`. Unconstrained values and open dictionaries
need an exact JSON-pointer entry with a reason in `open-object-exceptions.json`.
An exception that no longer exists fails too, so the list cannot silently rot.

The list records the current boundaries rather than approving new ones. It
separates extensible foreign mail/report formats, dictionaries and script data
from raw serialized values whose concrete wire types still need work under
#637, #638, #649, #652 and #658. In particular, a pipeline’s raw filter list is
not proof that unknown fields inside its step envelopes are refused. Those
remaining shapes keep #658 open; this guard prevents adding further holes
without an explicit, reviewable reason.

`TestAllRegisteredJSONRequestTypesRejectUnknownFields` passes each actual
registered closed JSON request type through the production binding/refusal
adapter, checking 422 `validation_failed` with the unknown field named. It
covers production and optional push/testing registrations without accessing
PostgreSQL. Foreign formats explicitly marked open remain exceptions, and
closed nested shapes inside an opaque value are not claimed as covered.

The closure guard’s negative controls widen the real generated Domain response
and add an unconstrained property. Both must be detected. The secret-response
and audit-log scan is already a separate required contract step (#673); its
echoed-password controls are in `test_secret_scan.py`.
