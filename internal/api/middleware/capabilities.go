package middleware

// Capability names used to authorize admin API routes.
//
// These MUST match the capability names seeded into the capabilities table
// (internal/bootstrap, EnsureRBAC): an account holds the capabilities of the
// roles it is granted, so introducing a name here that is not granted to the
// seeded roles would silently lock those roles out. The wildcard "*"
// (superadmin) satisfies every requirement.
//
// There is one kind of account, and one kind of token. What an account may do
// is its grants in force (active ones, and eligible ones while switched on,
// #346), read on every request (internal/grants), never the token, which
// carries none. A grant is a role at a scope; a route acting on a domain or
// mailbox accepts a grant covering it, every other route wants the capability
// at "*". "*" grants everything. An account holding no admin role is refused
// on every admin route; its mail is reached through the mail capabilities
// (models.CapMail*), which it holds on its own mailbox and on any it owns or
// has linked.
//
// Route-group mapping (wired in internal/api/routes.go):
//
//	domains:*    domains CRUD/DNS, sender allow/blocklists, MTA-STS,
//	             TLS-RPT reports, DKIM keys, certificates
//	mailboxes:*  accounts (mailboxes) CRUD, aliases CRUD; creating an
//	             account with POST /api/v1/accounts (mailboxes:write)
//	users:*      roles/capabilities listing, grants (read; granting and
//	             removing need users:write)
//	pipelines:*  pipelines CRUD/test, custom filters, pipeline logs
//	queue:read   outbound queue listing/stats/detail
//	queue:manage retry/bounce/delete (single and bulk)
//	bans:*       IP ban listing/creation/removal
//	messages:read delivery log (read)
//	observability:read  pipeline analytics funnel + per-message trace read
//	audit:read   the audit log and its verification (read)
//	agents:create creating an owned account (POST /api/v1/accounts with owned)
//
// The dashboard stats endpoint needs domains:read, which every admin role
// holds; the non-production test endpoints need "*" itself, and refuse to run
// in production anyway.
// The pipeline-execution trace read surface (analytics funnel + per-message
// trace) is gated by the dedicated observability:read capability, seeded into
// the admin and readonly roles (both already carry pipelines:read, so this is
// purely additive to existing admin flows) and satisfied by superadmin's "*".
const (
	CapDomainsRead    = "domains:read"
	CapDomainsWrite   = "domains:write"
	CapDomainsDelete  = "domains:delete"
	CapMailboxesRead  = "mailboxes:read"
	CapMailboxesWrite = "mailboxes:write"
	CapMailboxesDel   = "mailboxes:delete"
	CapUsersRead      = "users:read"
	CapUsersWrite     = "users:write"
	CapUsersDelete    = "users:delete"
	CapPipelinesRead  = "pipelines:read"
	CapPipelinesWrite = "pipelines:write"
	CapPipelinesDel   = "pipelines:delete"
	CapQueueRead      = "queue:read"
	CapQueueManage    = "queue:manage"
	CapBansRead       = "bans:read"
	CapBansWrite      = "bans:write"
	CapBansDelete     = "bans:delete"
	CapMessagesRead   = "messages:read"

	// CapObservabilityRead gates the pipeline-observability read surface added in
	// PR5: the aggregate analytics funnel (GET /admin/pipelines/analytics) and the
	// per-message trace read (GET /admin/messages/{id}/trace). It is a dedicated
	// capability rather than a reuse of pipelines:read so the funnel/trace surface
	// — which exposes per-message forensic detail including trace-only PII — can be
	// granted or withheld independently of pipeline configuration access.
	CapObservabilityRead = "observability:read"

	// CapAuditRead gates reading the audit log (GET /admin/audit) and checking
	// its chain (GET /admin/audit/verify). Seeded into the admin role, not
	// readonly; superadmin's "*" satisfies it. Reading is itself audited.
	CapAuditRead = "audit:read"

	// CapAgentsCreate gates creating an owned account (an agent's, or a sub
	// account) on a domain, from a session that signed in with a second
	// factor (#341, Decided 12). Seeded into the admin role; superadmin's "*"
	// satisfies it.
	CapAgentsCreate = "agents:create"
)
