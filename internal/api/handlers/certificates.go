package handlers

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/crypto"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

type CertificateHandler struct {
	db        *gorm.DB
	masterKey string
}

func NewCertificateHandler(db *gorm.DB, masterKey string) *CertificateHandler {
	return &CertificateHandler{db: db, masterKey: masterKey}
}

// ListCertificates returns all certificates (PEM fields redacted via json:"-").
// GET /api/v1/admin/certificates
func (h *CertificateHandler) ListCertificates(w http.ResponseWriter, r *http.Request) {
	var certs []models.Certificate
	if err := h.db.Preload("Domain").Find(&certs).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to list certificates")
		return
	}
	respond.List(w, certs, nil)
}

// GetCertificate returns a single certificate, including CertPEM in the response.
// GET /api/v1/admin/certificates/{id}
func (h *CertificateHandler) GetCertificate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid certificate ID")
		return
	}

	var cert models.Certificate
	if err := h.db.Preload("Domain").First(&cert, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Certificate not found")
		return
	}

	// Return a struct that includes cert_pem but NOT key_pem
	type certResponse struct {
		ID        uint          `json:"id"`
		DomainID  uint          `json:"domain_id"`
		CertPEM   string        `json:"cert_pem"`
		Issuer    string        `json:"issuer"`
		NotBefore string        `json:"not_before"`
		NotAfter  string        `json:"not_after"`
		AutoRenew bool          `json:"auto_renew"`
		CreatedAt string        `json:"created_at"`
		UpdatedAt string        `json:"updated_at"`
		Domain    models.Domain `json:"domain,omitzero"`
	}

	respond.Data(w, http.StatusOK, certResponse{
		ID:        cert.ID,
		DomainID:  cert.DomainID,
		CertPEM:   cert.CertPEM,
		Issuer:    cert.Issuer,
		NotBefore: cert.NotBefore.Format("2006-01-02T15:04:05Z07:00"),
		NotAfter:  cert.NotAfter.Format("2006-01-02T15:04:05Z07:00"),
		AutoRenew: cert.AutoRenew,
		CreatedAt: cert.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt: cert.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		Domain:    cert.Domain,
	})
}

// CreateCertificate stores a new TLS certificate.
// POST /api/v1/admin/certificates
func (h *CertificateHandler) CreateCertificate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DomainID  uint   `json:"domain_id"`
		CertPEM   string `json:"cert_pem"`
		KeyPEM    string `json:"key_pem"`
		AutoRenew *bool  `json:"auto_renew"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.DomainID == 0 || req.CertPEM == "" || req.KeyPEM == "" {
		respond.Error(w, http.StatusBadRequest, "bad_request", "domain_id, cert_pem, and key_pem are required")
		return
	}

	// Verify domain exists
	var domain models.Domain
	if err := h.db.First(&domain, req.DomainID).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Domain not found")
		return
	}

	// Parse certificate PEM to extract metadata
	block, _ := pem.Decode([]byte(req.CertPEM))
	if block == nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid certificate PEM")
		return
	}

	x509Cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Failed to parse certificate: "+err.Error())
		return
	}

	// Encrypt the private key if master key is configured
	keyToStore := req.KeyPEM
	if h.masterKey != "" {
		encrypted, err := crypto.Encrypt(req.KeyPEM, h.masterKey)
		if err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to encrypt private key")
			return
		}
		keyToStore = encrypted
	} else {
		slog.Warn("Certificate private key stored in plaintext: MASTER_KEY not configured")
	}

	autoRenew := true
	if req.AutoRenew != nil {
		autoRenew = *req.AutoRenew
	}

	cert := models.Certificate{
		DomainID:  req.DomainID,
		CertPEM:   req.CertPEM,
		KeyPEM:    keyToStore,
		Issuer:    x509Cert.Issuer.String(),
		NotBefore: x509Cert.NotBefore,
		NotAfter:  x509Cert.NotAfter,
		AutoRenew: autoRenew,
	}

	if err := h.db.Create(&cert).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to create certificate")
		return
	}

	// Reload with domain association
	h.db.Preload("Domain").First(&cert, cert.ID)
	respond.Data(w, http.StatusCreated, cert)
}

// DeleteCertificate removes a certificate.
// DELETE /api/v1/admin/certificates/{id}
func (h *CertificateHandler) DeleteCertificate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid certificate ID")
		return
	}

	var cert models.Certificate
	if err := h.db.First(&cert, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Certificate not found")
		return
	}

	if err := h.db.Delete(&cert).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to delete certificate")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
