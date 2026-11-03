package handlers

import (
	"encoding/json"
	"errors"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"net/http"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

// RestmailDiscoveryHandler advertises delivery for a domain, never a mailbox.
// The endpoint comes from operator configuration, not caller-controlled Host or
// forwarding headers. Senders must fetch discovery over verified HTTPS to the MX.
type RestmailDiscoveryHandler struct {
	db       *gorm.DB
	endpoint string
}

func NewRestmailDiscoveryHandler(db *gorm.DB, hostname string) *RestmailDiscoveryHandler {
	h := &RestmailDiscoveryHandler{db: db}
	if host, err := validateDomainName(hostname); err == nil {
		h.endpoint = "https://" + host + "/restmail"
	}
	return h
}

// RestmailDiscovery is the protocol document a sender reads: the HTTPS base URL it delivers
// to, appending /messages. It is written as it is, without the API's data envelope.
type RestmailDiscovery = apitypes.RestmailDiscovery

// restmailDiscoverQuery names the recipient domain, once. Its pattern is the shape
// validateDomainName needs: a dot with something after it, since a name's one trailing dot
// is removed, or one of the full stops IDNA maps to a dot. Whether the name is a valid
// hostname is IDNA's to say, so a domain that matches can still be refused, but the pattern
// never refuses one the handler looks up.
//
// The first branch permits nothing the second does not. It is first because a generated
// value takes the first branch: a.a is a name the handler looks up, where the second
// branch alone generates ".!", which it refuses.
type restmailDiscoverQuery struct {
	Domain string `query:"domain,required" pattern:"[a-z]\\.[a-z]|\\.\\S|[。．｡]" message:"Invalid domain query parameter" example:"restmail.test" description:"Exactly one fully-qualified recipient domain, never a single label or a mailbox address. Case, a trailing dot and internationalized names are normalized as for domain creation; a name IDNA refuses is 422 validation_failed even when it matches this pattern."`
}

// Register mounts discovery. Like the rest of RESTMAIL, any server may ask.
func (h *RestmailDiscoveryHandler) Register(rs endpoint.Routes) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/.well-known/restmail",
		OperationID: "restmailDiscover", Summary: "Discover RESTMAIL delivery for a hosted domain", Tags: []string{"RESTMAIL"},
		Description: "Fetched from the recipient domain's MX host over verified HTTPS. The answer is a protocol document " +
			"without the data envelope, naming the configured GATEWAY_HOSTNAME, never the request's Host. A 404 means " +
			"the domain is not hosted here; a missing hostname is 503 and a failed lookup 500, never 404. A domain " +
			"given twice, or not a domain, is 422 validation_failed.",
		Security: oapifly.Public, Headers: noStoreHeader,
		Errors:     []int{http.StatusUnprocessableEntity, http.StatusNotFound, http.StatusServiceUnavailable},
		Middleware: []endpoint.Middleware{noStore}}, h.discover)
}

var noStoreHeader = map[string]oapifly.ResponseHeader{
	"Cache-Control": {Description: "no-store, on every answer"},
}

// discover answers GET /.well-known/restmail?domain=<recipient-domain>. A 404 means the
// domain is not hosted here. Operational errors must never look like a 404: senders may use
// a verified 404 to forget a domain's REST capability.
func (h *RestmailDiscoveryHandler) discover(r *http.Request, in restmailDiscoverQuery) (endpoint.Forwarded[RestmailDiscovery], error) {
	var zero endpoint.Forwarded[RestmailDiscovery]
	if len(r.URL.Query()["domain"]) != 1 {
		return zero, endpoint.Invalid(map[string]string{"domain": "Exactly one domain query parameter required"})
	}
	domain, err := validateDomainName(in.Domain)
	if err != nil {
		return zero, endpoint.Invalid(map[string]string{"domain": "Invalid domain query parameter"})
	}
	if h.endpoint == "" {
		return zero, endpoint.Refuse(http.StatusServiceUnavailable, "unavailable", "RESTMAIL discovery hostname not configured")
	}

	var hosted models.Domain
	err = h.db.WithContext(r.Context()).Select("id").Where("name = ? AND active = ?", domain, true).First(&hosted).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return zero, endpoint.Refuse(http.StatusNotFound, "not_found", "Domain not hosted")
	}
	if err != nil {
		return zero, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to look up domain")
	}

	// The protocol document itself: the sender appends /messages to this base URL.
	body, err := json.Marshal(RestmailDiscovery{Endpoint: h.endpoint})
	if err != nil {
		return zero, err
	}
	return endpoint.Forwarded[RestmailDiscovery]{Status: http.StatusOK, Body: body}, nil
}
