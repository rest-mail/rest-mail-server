package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/restmail/restmail/internal/api/endpoint"
	apitypes "github.com/restmail/restmail/internal/api/types"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/outbound"
	"gorm.io/gorm"
)

type DKIMHandler struct {
	db        *gorm.DB
	masterKey string
}

func NewDKIMHandler(db *gorm.DB, masterKey string) *DKIMHandler {
	return &DKIMHandler{db: db, masterKey: masterKey}
}

// dkimEntry contains only public monitoring metadata. Unknown legacy key ages
// are null; the domain's CreatedAt/UpdatedAt are not key timestamps.
type dkimEntry = apitypes.DKIMEntry

func newDKIMEntry(domain models.Domain, now time.Time) dkimEntry {
	entry := dkimEntry{
		DomainID: domain.ID, Domain: domain.Name, Selector: domain.DKIMSelector,
		HasKey: true, CreatedAt: domain.DKIMCreatedAt,
	}
	if domain.DKIMCreatedAt != nil {
		age := max(0, now.Sub(*domain.DKIMCreatedAt).Seconds())
		entry.AgeSeconds = &age
	}
	return entry
}

// listKeys returns DKIM configuration for all domains (keys are redacted).
// GET /api/v1/admin/dkim
func (h *DKIMHandler) listKeys(r *http.Request, _ struct{}) (endpoint.List[dkimEntry], error) {
	var domains []models.Domain
	if err := h.db.WithContext(r.Context()).Select("id", "name", "dkim_selector", "dkim_created_at").Where("dkim_selector != '' AND dkim_private_key != ''").Find(&domains).Error; err != nil {
		return endpoint.List[dkimEntry]{}, err
	}

	entries := make([]dkimEntry, 0, len(domains))
	now := time.Now()
	for _, d := range domains {
		entries = append(entries, newDKIMEntry(d, now))
	}
	return endpoint.List[dkimEntry]{Data: entries}, nil
}

type setDKIMKeyBody = apitypes.SetDKIMKeyBody
type setDKIMKeyRequest struct {
	ID   uint32         `path:"id" minimum:"1" example:"1"`
	Body setDKIMKeyBody `body:""`
}

func (h *DKIMHandler) setKey(_ *http.Request, in setDKIMKeyRequest) (dkimEntry, error) {
	var domain models.Domain
	if err := h.db.First(&domain, in.ID).Error; err != nil {
		return dkimEntry{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Domain not found")
	}
	req := in.Body

	// A key or selector go-dkim will not sign with is refused here, not found
	// when the domain's mail is deferred because it cannot be signed (#391).
	if err := outbound.CheckDKIMKey(domain.Name, req.Selector, req.PrivateKey); err != nil {
		var refused *outbound.DKIMKeyError
		if !errors.As(err, &refused) {
			return dkimEntry{}, err
		}
		return dkimEntry{}, endpoint.Invalid(map[string]string{refused.Field: refused.Reason})
	}

	if h.masterKey == "" {
		slog.Warn("DKIM private key stored in plaintext: MASTER_KEY not configured")
	}
	// OSI-8: encrypt at rest with a version-tagged (dkim:v1:) ciphertext, the
	// only form the signer reads when a MASTER_KEY is set; it fails closed on
	// anything else. With no MASTER_KEY (dev) this returns the plaintext.
	keyToStore, err := models.EncryptDKIMPrivateKey(req.PrivateKey, h.masterKey)
	if err != nil {
		return dkimEntry{}, err
	}

	createdAt := domain.DKIMCreatedAt
	// Re-saving the same key must not make it appear younger. Legacy keys
	// re-saved unchanged still have an unknown age.
	previous, loadErr := models.LoadDKIMPrivateKey(domain.DKIMPrivateKey, h.masterKey)
	if loadErr != nil || previous != req.PrivateKey {
		now := time.Now().UTC()
		createdAt = &now
	}
	if err := h.db.Model(&domain).Updates(map[string]interface{}{
		"dkim_selector":    req.Selector,
		"dkim_private_key": keyToStore,
		"dkim_created_at":  createdAt,
	}).Error; err != nil {
		return dkimEntry{}, err
	}

	domain.DKIMSelector = req.Selector
	domain.DKIMCreatedAt = createdAt
	return newDKIMEntry(domain, time.Now()), nil
}

// deleteKey removes the DKIM key for a domain.
// DELETE /api/v1/admin/dkim/{id}
func (h *DKIMHandler) deleteKey(_ *http.Request, in domainPath) (endpoint.Empty, error) {
	var domain models.Domain
	if err := h.db.First(&domain, in.ID).Error; err != nil {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Domain not found")
	}

	if err := h.db.Model(&domain).Updates(map[string]interface{}{
		"dkim_selector":    "",
		"dkim_private_key": "",
		"dkim_created_at":  nil,
	}).Error; err != nil {
		return endpoint.Empty{}, err
	}

	return endpoint.Empty{}, nil
}
