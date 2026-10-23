package handlers

import (
	"errors"
	"net/http"

	"github.com/restmail/restmail/internal/api/endpoint"
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
	Transport   string `json:"transport" enums:"direct,relay"`
	Host        string `json:"host,omitempty"`
	Port        int    `json:"port,omitempty" minimum:"1" maximum:"65535"`
	TLSMode     string `json:"tls_mode,omitempty" enums:"starttls,implicit"`
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

func (h *RelayHandler) domain(id uint32) (*models.Domain, error) {
	var domain models.Domain
	if err := h.db.First(&domain, id).Error; err != nil {
		return nil, endpoint.Refuse(http.StatusNotFound, "not_found", "Domain not found")
	}
	return &domain, nil
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

// get returns the domain's outbound transport; direct when none is configured.
// GET /api/v1/admin/domains/{id}/relay
func (h *RelayHandler) get(_ *http.Request, in domainPath) (relayView, error) {
	domain, err := h.domain(in.ID)
	if err != nil {
		return relayView{}, err
	}
	rel, err := h.load(domain.ID)
	if err != nil {
		return relayView{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to load the relay settings")
	}
	return viewRelay(domain.ID, rel), nil
}

// set replaces the domain's outbound transport. A password left out keeps the
// stored one; an empty password clears it.
// PUT /api/v1/admin/domains/{id}/relay
func (h *RelayHandler) set(_ *http.Request, in setRelayInput) (relayView, error) {
	domain, err := h.domain(in.ID)
	if err != nil {
		return relayView{}, err
	}
	req := in.Body
	existing, err := h.load(domain.ID)
	if err != nil {
		return relayView{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to load the relay settings")
	}
	rel := models.DomainRelay{DomainID: domain.ID, Transport: req.Transport}
	if existing != nil {
		rel.ID, rel.CreatedAt = existing.ID, existing.CreatedAt
	}

	// A field's shape is the schema's whatever the transport: direct keeps
	// none of them, but a port of 0 is no more valid for it than for a relay.
	fields := map[string]string{}
	if rmail.ContainsControlChar(req.Username) || len(req.Username) > 255 {
		fields["username"] = "Must be at most 255 characters with no control characters"
	}
	if req.Password != nil && rmail.ContainsControlChar(*req.Password) {
		fields["password"] = "Must not contain control characters"
	}

	switch req.Transport {
	case models.OutboundTransportDirect:
		// Direct keeps nothing of a relay, credentials included.
		if len(fields) > 0 {
			return relayView{}, endpoint.Invalid(fields)
		}
	case models.OutboundTransportRelay:
		if !models.ValidRelayHost(req.Host) {
			fields["host"] = "Must be a bare host name or IP address, with no scheme, port or path"
		}
		if req.Port == nil {
			fields["port"] = "Must be between 1 and 65535"
		}
		if req.TLSMode == nil {
			fields["tls_mode"] = "Must be one of: starttls, implicit"
		}
		rel.Host, rel.Username = req.Host, req.Username
		if req.Port != nil {
			rel.Port = *req.Port
		}
		if req.TLSMode != nil {
			rel.TLSMode = *req.TLSMode
		}

		passwordSet := existing != nil && existing.PasswordEnc != ""
		if req.Password != nil {
			passwordSet = *req.Password != ""
		}
		if (rel.Username != "") != passwordSet {
			fields["password"] = "A username and a password are set together, or neither"
		}
		if len(fields) > 0 {
			return relayView{}, endpoint.Invalid(fields)
		}

		switch {
		case req.Password == nil && existing != nil:
			rel.PasswordEnc = existing.PasswordEnc
		case req.Password != nil && *req.Password != "":
			if h.masterKey == "" {
				return relayView{}, endpoint.Refuse(http.StatusConflict, "master_key_required", "A relay password can only be stored when the server has a MASTER_KEY")
			}
			enc, err := models.EncryptRelayPassword(*req.Password, h.masterKey)
			if err != nil {
				return relayView{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to encrypt the relay password")
			}
			rel.PasswordEnc = enc
		}
	default:
		return relayView{}, endpoint.Invalid(map[string]string{"transport": "Must be one of: direct, relay"})
	}

	if err := h.db.Save(&rel).Error; err != nil {
		return relayView{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to save the relay settings")
	}
	return viewRelay(domain.ID, &rel), nil
}
