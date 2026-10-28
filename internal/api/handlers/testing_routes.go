package handlers

import (
	"net/http"
	"time"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/grants"
)

// TestRouteChecks requires the wildcard capability itself at installation scope.
type TestRouteChecks struct {
	At func(string) endpoint.Middleware
}

// Register describes and mounts the test operations. Production installations omit
// the entire surface, including its generated description.
func (h *TestHandler) Register(rs endpoint.Routes, checks TestRouteChecks) {
	if h.productionLocked() {
		return
	}
	tags := []string{"Admin: Testing"}
	check := []endpoint.Middleware{checks.At(grants.Wildcard)}
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/test/send", OperationID: "sendTestEmail", Summary: "Inject a test email directly into a recipient mailbox", Tags: tags, Status: http.StatusCreated, Errors: []int{http.StatusNotFound}, Middleware: check}, h.sendTestEmail)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/test/verify", OperationID: "verifyDelivery", Summary: "Poll for a delivered message matching the given criteria", Tags: tags, Middleware: check}, h.verifyDelivery)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/test/probe", OperationID: "probeServices", Summary: "Probe connectivity to SMTP/IMAP/POP3/DNS/DB", Tags: tags, Middleware: check}, h.probeServices)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/test/reset", OperationID: "resetTestData", Summary: "Wipe all data and re-seed default test data", Tags: tags, Middleware: check}, h.resetTestData)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/test/seed", OperationID: "seedTestData", Summary: "Seed default test domains, mailboxes, aliases and accounts", Tags: tags, Middleware: check}, h.seedTestData)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/test/snapshot", OperationID: "snapshotTestData", Summary: "Capture the current database state as JSON", Tags: tags, Middleware: check}, h.snapshot)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/test/snapshot/restore", OperationID: "restoreTestSnapshot", Summary: "Restore database state from a previously captured snapshot", Tags: tags, Middleware: check}, h.restoreSnapshot)
}

type sendTestEmailRequest struct {
	From    string `json:"from" minLength:"1" message:"sender address required" format:"email" formatMessage:"must be a mail address"`
	To      string `json:"to" minLength:"1" message:"recipient address required" format:"email" formatMessage:"must be a mail address"`
	Subject string `json:"subject,omitempty"`
	Body    string `json:"body,omitempty"`
}

type verifyDeliveryRequest struct {
	To        string `query:"to,required" minLength:"1" message:"recipient address required" example:"admin@restmail.test"`
	Subject   string `query:"subject,omitempty"`
	MessageID string `query:"message_id,omitempty"`
	Timeout   string `query:"timeout,omitempty"`
}

type probeServicesRequest struct {
	Services []string `json:"services,omitempty"`
	Host     string   `json:"host,omitempty"`
	Timeout  string   `json:"timeout,omitempty"`
}

// TestEmailSent identifies the injected message, preserving the existing JSON.
type TestEmailSent struct {
	MessageID uint   `json:"message_id"`
	MsgID     string `json:"msg_id"`
	From      string `json:"from"`
	To        string `json:"to"`
	Subject   string `json:"subject"`
}

// TestDelivery carries the matching message only when Found is true.
type TestDelivery struct {
	Found      bool       `json:"found"`
	Message    string     `json:"message,omitempty"`
	MessageID  *uint      `json:"message_id,omitempty"`
	MsgID      *string    `json:"msg_id,omitempty"`
	Subject    *string    `json:"subject,omitempty"`
	Sender     *string    `json:"sender,omitempty"`
	ReceivedAt *time.Time `json:"received_at,omitempty"`
}

type TestProbeResults struct {
	SMTP           *TestProbeResult `json:"smtp,omitempty"`
	SMTPSubmission *TestProbeResult `json:"smtp_submission,omitempty"`
	IMAP           *TestProbeResult `json:"imap,omitempty"`
	POP3           *TestProbeResult `json:"pop3,omitempty"`
	DNS            *TestProbeResult `json:"dns,omitempty"`
	DB             *TestProbeResult `json:"db,omitempty"`
}

type TestProbeResult struct {
	Status    string    `json:"status"`
	Address   string    `json:"address,omitempty"`
	Domain    string    `json:"domain,omitempty"`
	Error     string    `json:"error,omitempty"`
	Latency   string    `json:"latency,omitempty"`
	MXRecords *[]string `json:"mx_records,omitempty"`
}

func probePtr(result TestProbeResult) *TestProbeResult { return &result }

type TestDataStatus struct {
	Status string `json:"status"`
}

// TestSnapshot uses rows generated from the models' JSON fields, with distinct names so
// fixture rows cannot collide with the public API's schemas. Raw JSON columns
// stay JSON instead of being described as base64 byte strings.
//
//go:generate sh -c "cd ../../.. && python3 scripts/generate_testing_snapshot.py"
type TestSnapshot struct {
	Domains    *[]TestSnapshotDomain        `json:"domains"`
	Mailboxes  *[]TestSnapshotMailbox       `json:"mailboxes"`
	Aliases    *[]TestSnapshotAlias         `json:"aliases"`
	Messages   *[]TestSnapshotMessage       `json:"messages"`
	Queue      *[]TestSnapshotOutboundQueue `json:"queue"`
	Pipelines  *[]TestSnapshotPipeline      `json:"pipelines"`
	Bans       *[]TestSnapshotBan           `json:"bans"`
	SnapshotAt time.Time                    `json:"snapshot_at"`
}

// RestoreTestSnapshot accepts sparse fixture rows as the original model decoder did.
type RestoreTestSnapshot struct {
	Domains    *[]RestoreSnapshotDomain        `json:"domains,omitempty"`
	Mailboxes  *[]RestoreSnapshotMailbox       `json:"mailboxes,omitempty"`
	Aliases    *[]RestoreSnapshotAlias         `json:"aliases,omitempty"`
	Messages   *[]RestoreSnapshotMessage       `json:"messages,omitempty"`
	Queue      *[]RestoreSnapshotOutboundQueue `json:"queue,omitempty"`
	Pipelines  *[]RestoreSnapshotPipeline      `json:"pipelines,omitempty"`
	Bans       *[]RestoreSnapshotBan           `json:"bans,omitempty"`
	SnapshotAt *time.Time                      `json:"snapshot_at,omitempty"`
}
