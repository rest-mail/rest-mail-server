package api

// unregisteredRoutes are the routes still mounted without endpoint.Register, by path. Each
// area issue (#630 to #652) takes its routes off as it registers them, and nothing is added:
// a new route is registered from the start (#669).
var unregisteredRoutes = []string{
	"GET /.well-known/autoconfig/mail/config-v1.1.xml",
	"GET /.well-known/mta-sts.txt",
	"POST /.well-known/smtp-tlsrpt",
	"GET /api/mailboxes",
	"GET /api/v1/accounts/{id}/calendar-events",
	"GET /api/v1/accounts/{id}/events",
	"GET /api/v1/attachments/{id}",
	"POST /api/v1/auth/owner/challenge",
	"POST /api/v1/auth/owner/status",
	"POST /api/v1/auth/owner/token",
	"POST /api/v1/auth/reset",
	"POST /api/v1/messages/deliver",
	"POST /api/v1/messages/draft",
	"PUT /api/v1/messages/draft/{id}",
	"POST /api/v1/messages/draft/{id}/send",
	"GET /api/v1/messages/scheduled",
	"DELETE /api/v1/messages/scheduled/{id}",
	"POST /api/v1/messages/send",
	"POST /api/v1/messages/{id}/calendar-reply",
	"POST /api/v1/messages/{id}/forward",
	"GET /api/v1/messages/{id}/raw",
	"POST /autodiscover/autodiscover.xml",
	"GET /mail/config-v1.1.xml",
}
