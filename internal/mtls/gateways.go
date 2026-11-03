package mtls

// The protocol gateways that call the API on the internal listener. Each is a
// service with its own client certificate, whose Common Name says which one it
// is (#1586). The API maps the name to the service, `instance mtls-issue` and
// the Helm chart issue one certificate per service with that name, and
// helm/restmail/templates/_helpers.tpl (restmail.internalMtls.gateways) lists
// the same names (TestChartGatewayNamesMatch).
const (
	GatewayServiceIMAP           = "imap"
	GatewayServicePOP3           = "pop3"
	GatewayServiceSMTPSubmission = "smtp-submission"
	GatewayServiceSMTPDelivery   = "smtp-delivery"
)

// Gateway is one gateway service and the Common Name of its client certificate.
type Gateway struct {
	Service    string
	CommonName string
}

// Gateways lists every gateway service, in a fixed order.
var Gateways = []Gateway{
	{Service: GatewayServiceIMAP, CommonName: "rest-mail-imap-gateway"},
	{Service: GatewayServicePOP3, CommonName: "rest-mail-pop3-gateway"},
	{Service: GatewayServiceSMTPSubmission, CommonName: "rest-mail-smtp-submission"},
	{Service: GatewayServiceSMTPDelivery, CommonName: "rest-mail-smtp-delivery"},
}
