package api

// unregisteredRoutes are the routes still mounted without endpoint.Register, by path. Each
// area issue (#630 to #652) takes its routes off as it registers them, and nothing is added:
// a new route is registered from the start (#669).
var unregisteredRoutes = []string{
	"GET /.well-known/autoconfig/mail/config-v1.1.xml",
	"POST /.well-known/smtp-tlsrpt",
	"GET /api/v1/accounts/{id}/events",
	"POST /api/v1/messages/draft/{id}/send",
	"POST /api/v1/messages/send",
	"POST /api/v1/messages/{id}/forward",
	"POST /autodiscover/autodiscover.xml",
	"GET /mail/config-v1.1.xml",
}
