package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	rmail "github.com/restmail/restmail/internal/mail"
	"gorm.io/gorm"
)

// RelayHandler sets a sending domain's outbound transport: direct MX delivery
// or an operator-chosen SMTP relay (#143). The relay password is write-only.
type RelayHandler struct {
	db        *gorm.DB
	masterKey string
}

func NewRelayHandler(db *gorm.DB, masterKey string) *RelayHandler {
	return &RelayHandler{db: db, masterKey: masterKey}
}

// relayView is what the API returns: never the password, only whether one is set.
type relayView struct {
	DomainID    uint   `json:"domain_id"`
	Transport   string `json:"transport"`
	Host        string `json:"host,omitempty"`
	Port        int    `json:"port,omitempty"`
	TLSMode     string `json:"tls_mode,omitempty"`
	Username    string `json:"username,omitempty"`
	PasswordSet bool   `json:"password_set"`
}

func viewRelay(domainID uint, rel *models.DomainRelay) relayView {
	if rel == nil {
		return relayView{DomainID: domainID, Transport: models.OutboundTransportDirect}
	}
	return relayView{
		DomainID:    domainID,
		Transport:   rel.Transport,
		Host:        rel.Host,
		Port:        rel.Port,
		TLSMode:     rel.TLSMode,
		Username:    rel.Username,
		PasswordSet: rel.PasswordEnc != "",
	}
}

func (h *RelayHandler) domain(w http.ResponseWriter, r *http.Request) (*models.Domain, bool) {
	id, err := pathID(r, "id")
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid domain ID")
		return nil, false
	}
	var domain models.Domain
	if err := h.db.First(&domain, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Domain not found")
		return nil, false
	}
	return &domain, true
}

func (h *RelayHandler) load(domainID uint) (*models.DomainRelay, error) {
	var rel models.DomainRelay
	err := h.db.Where("domain_id = ?", domainID).First(&rel).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rel, nil
}

// Get returns the domain's outbound transport; direct when none is configured.
// GET /api/v1/admin/domains/{id}/relay
func (h *RelayHandler) Get(w http.ResponseWriter, r *http.Request) {
	domain, ok := h.domain(w, r)
	if !ok {
		return
	}
	rel, err := h.load(domain.ID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to load the relay settings")
		return
	}
	respond.Data(w, http.StatusOK, viewRelay(domain.ID, rel))
}

// Set replaces the domain's outbound transport. A password left out keeps the
// stored one; an empty password clears it.
// PUT /api/v1/admin/domains/{id}/relay
func (h *RelayHandler) Set(w http.ResponseWriter, r *http.Request) {
	domain, ok := h.domain(w, r)
	if !ok {
		return
	}
	var req struct {
		Transport string  `json:"transport"`
		Host      string  `json:"host"`
		Port      int     `json:"port"`
		TLSMode   string  `json:"tls_mode"`
		Username  string  `json:"username"`
		Password  *string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	existing, err := h.load(domain.ID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to load the relay settings")
		return
	}
	rel := models.DomainRelay{DomainID: domain.ID, Transport: req.Transport}
	if existing != nil {
		rel.ID, rel.CreatedAt = existing.ID, existing.CreatedAt
	}

	switch req.Transport {
	case models.OutboundTransportDirect:
		// Direct keeps nothing of a relay, credentials included.
	case models.OutboundTransportRelay:
		fields := map[string]string{}
		if !models.ValidRelayHost(req.Host) {
			fields["host"] = "Must be a bare host name or IP address, with no scheme, port or path"
		}
		if req.Port < 1 || req.Port > 65535 {
			fields["port"] = "Must be between 1 and 65535"
		}
		if req.TLSMode != models.RelayTLSStartTLS && req.TLSMode != models.RelayTLSImplicit {
			fields["tls_mode"] = "Must be one of: starttls, implicit"
		}
		if rmail.ContainsControlChar(req.Username) || len(req.Username) > 255 {
			fields["username"] = "Must be at most 255 characters with no control characters"
		}
		if req.Password != nil && rmail.ContainsControlChar(*req.Password) {
			fields["password"] = "Must not contain control characters"
		}
		rel.Host, rel.Port, rel.TLSMode, rel.Username = req.Host, req.Port, req.TLSMode, req.Username

		passwordSet := existing != nil && existing.PasswordEnc != ""
		if req.Password != nil {
			passwordSet = *req.Password != ""
		}
		if (rel.Username != "") != passwordSet {
			fields["password"] = "A username and a password are set together, or neither"
		}
		if len(fields) > 0 {
			respond.ValidationError(w, fields)
			return
		}

		switch {
		case req.Password == nil && existing != nil:
			rel.PasswordEnc = existing.PasswordEnc
		case req.Password != nil && *req.Password != "":
			if h.masterKey == "" {
				respond.Error(w, http.StatusConflict, "master_key_required",
					"A relay password can only be stored when the server has a MASTER_KEY")
				return
			}
			enc, err := models.EncryptRelayPassword(*req.Password, h.masterKey)
			if err != nil {
				respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to encrypt the relay password")
				return
			}
			rel.PasswordEnc = enc
		}
	default:
		respond.ValidationError(w, map[string]string{"transport": "Must be one of: direct, relay"})
		return
	}

	if err := h.db.Save(&rel).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to save the relay settings")
		return
	}
	respond.Data(w, http.StatusOK, viewRelay(domain.ID, &rel))
}
