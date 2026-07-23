package middleware

// Capability names used to authorize admin API routes.
//
// These MUST match the capability names seeded into the admin_capabilities
// table (cmd/seed/main.go, seedRBAC): admin access tokens carry a snapshot of
// the caller's capability names in their claims, so introducing a name here
// that is not granted to the existing seeded roles would silently lock those
// roles out. The wildcard "*" (superadmin) satisfies every requirement.
//
// How capabilities derive from token types:
//   - Admin tokens (UserType "admin"): the Capabilities claim issued at login
//     from the user's roles. "*" grants everything.
//   - Legacy mailbox-admin tokens (UserType "mailbox" with the deprecated
//     IsAdmin flag): treated as wildcard, preserving the pre-RBAC AdminOnly
//     behavior for already-issued tokens and webmail accounts flagged
//     is_admin.
//   - Mailbox tokens: no admin capabilities. They are confined to the
//     mailbox-scoped route group (accounts, folders, messages, send, drafts,
//     attachments, contacts, vacation, sieve, search, quarantine, SSE events)
//     and receive 403 on every capability-gated admin route.
//
// Route-group mapping (wired in internal/api/routes.go):
//
//	domains:*    domains CRUD/DNS, sender allow/blocklists, MTA-STS,
//	             TLS-RPT reports, DKIM keys, certificates
//	mailboxes:*  mailboxes CRUD, aliases CRUD, webmail accounts
//	users:*      admin users CRUD, roles/capabilities listing,
//	             activity log (read)
//	pipelines:*  pipelines CRUD/test, custom filters, pipeline logs
//	queue:read   outbound queue listing/stats/detail
//	queue:manage retry/bounce/delete (single and bulk)
//	bans:*       IP ban listing/creation/removal
//	messages:read delivery log (read)
//
// The dashboard stats endpoint and the non-production test endpoints remain
// behind AdminOnly without a finer capability: every admin role may see the
// dashboard, and the test endpoints already refuse to run in production.
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
)
