package handlers

import (
	"net/http"

	oapifly "github.com/antimatter-studios/go-oapifly"

	"github.com/restmail/restmail/internal/api/endpoint"
)

// Register mounts the RESTMAIL server-to-server protocol. Like SMTP, any server may connect,
// so none of it needs a credential: delivery is authenticated per message (OSI-3), and a
// negative recipient check is tarpitted. deliverBodyLimit is the delivery body cap, which
// takes a full max-size message.
func (h *RestmailHandler) Register(rs endpoint.Routes, deliverBodyLimit endpoint.Middleware) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/restmail/capabilities",
		OperationID: "restmailCapabilities", Summary: "Get RESTMAIL server capabilities", Tags: []string{"RESTMAIL"},
		Security: oapifly.Public}, h.capabilities)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/restmail/mailboxes",
		OperationID: "restmailCheckMailbox", Summary: "Check if a recipient mailbox exists", Tags: []string{"RESTMAIL"},
		Security: oapifly.Public}, h.checkMailbox)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/restmail/messages",
		OperationID: "restmailDeliver", Summary: "Deliver a message via RESTMAIL protocol", Tags: []string{"RESTMAIL"},
		Description: "Server-to-server delivery. Unauthenticated at the HTTP layer: a delivery must come from a " +
			"trusted peer network or carry a DKIM signature aligned with its From domain, or it is refused 403 " +
			"unauthorized. The inbound pipeline can refuse it 403 rejected or defer it 503 deferred. When no " +
			"recipient takes the message the answer is 422 undeliverable.",
		Status: http.StatusCreated, Errors: []int{http.StatusForbidden, http.StatusServiceUnavailable},
		Security: oapifly.Public, Middleware: []endpoint.Middleware{deliverBodyLimit}}, h.deliver)
}
