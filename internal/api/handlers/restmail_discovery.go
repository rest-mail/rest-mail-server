package handlers

import (
	"errors"
	"net/http"

	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
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

// Discover serves GET /.well-known/restmail?domain=<recipient-domain>.
// A 404 means the domain is not hosted here. Operational errors must never look
// like a 404: senders may use a verified 404 to forget a domain's REST capability.
func (h *RestmailDiscoveryHandler) Discover(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	domains := r.URL.Query()["domain"]
	if len(domains) != 1 {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Exactly one domain query parameter required")
		return
	}
	domain, err := validateDomainName(domains[0])
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid domain query parameter")
		return
	}
	if h.endpoint == "" {
		respond.Error(w, http.StatusServiceUnavailable, "unavailable", "RESTMAIL discovery hostname not configured")
		return
	}

	var hosted models.Domain
	err = h.db.WithContext(r.Context()).Select("id").Where("name = ? AND active = ?", domain, true).First(&hosted).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		respond.Error(w, http.StatusNotFound, "not_found", "Domain not hosted")
		return
	}
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to look up domain")
		return
	}

	// This is the protocol document itself, without the API's data envelope.
	// The sender appends /messages to this base URL when delivering.
	respond.JSON(w, http.StatusOK, map[string]string{"endpoint": h.endpoint})
}
