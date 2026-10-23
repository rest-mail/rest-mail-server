package handlers

import (
	"crypto/x509"
	"encoding/pem"
	"log/slog"
	"net/http"
	"time"

	"github.com/restmail/restmail/internal/api/endpoint"
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

// listCertificates returns public certificate metadata.
// GET /api/v1/admin/certificates
func (h *CertificateHandler) listCertificates(_ *http.Request, in listDomainsRequest) (endpoint.List[Certificate], error) {
	limit, offset := pagination(in.Limit, in.Offset, defaultListLimit, maxListLimit)

	var total int64
	h.db.Model(&models.Certificate{}).Count(&total)

	var certs []models.Certificate
	if err := h.db.Preload("Domain").Limit(limit).Offset(offset).Find(&certs).Error; err != nil {
		return endpoint.List[Certificate]{}, err
	}
	rows := make([]Certificate, len(certs))
	for i, cert := range certs {
		rows[i] = certificateOf(cert)
	}
	return endpoint.List[Certificate]{Data: rows, Pagination: &respond.Pagination{Total: total, HasMore: int64(offset+limit) < total}}, nil
}

// getCertificate returns a single certificate, including CertPEM in the response.
// GET /api/v1/admin/certificates/{id}
func (h *CertificateHandler) getCertificate(_ *http.Request, in certificatePath) (CertificateDetails, error) {
	var cert models.Certificate
	if err := h.db.Preload("Domain").First(&cert, in.ID).Error; err != nil {
		return CertificateDetails{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Certificate not found")
	}

	return certificateDetailsOf(cert), nil
}

type createCertificateRequest struct {
	DomainID  uint   `json:"domain_id" minimum:"1" message:"required"`
	CertPEM   string `json:"cert_pem" minLength:"1" message:"required"`
	KeyPEM    string `json:"key_pem" minLength:"1" message:"required"`
	AutoRenew *bool  `json:"auto_renew,omitempty"`
}

func (h *CertificateHandler) createCertificate(_ *http.Request, req createCertificateRequest) (Certificate, error) {
	// Verify domain exists
	var domain models.Domain
	if err := h.db.First(&domain, req.DomainID).Error; err != nil {
		return Certificate{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Domain not found")
	}

	// Parse certificate PEM to extract metadata
	block, _ := pem.Decode([]byte(req.CertPEM))
	if block == nil {
		return Certificate{}, endpoint.Invalid(map[string]string{"cert_pem": "Invalid certificate PEM"})
	}

	x509Cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return Certificate{}, endpoint.Invalid(map[string]string{"cert_pem": "Failed to parse certificate: " + err.Error()})
	}

	// Encrypt the private key if master key is configured
	keyToStore := req.KeyPEM
	if h.masterKey != "" {
		encrypted, err := crypto.EncryptString(req.KeyPEM, h.masterKey)
		if err != nil {
			return Certificate{}, err
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
		return Certificate{}, err
	}

	// Reload with domain association
	h.db.Preload("Domain").First(&cert, cert.ID)
	return certificateOf(cert), nil
}

// deleteCertificate removes a certificate.
// DELETE /api/v1/admin/certificates/{id}
func (h *CertificateHandler) deleteCertificate(_ *http.Request, in certificatePath) (endpoint.Empty, error) {
	var cert models.Certificate
	if err := h.db.First(&cert, in.ID).Error; err != nil {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Certificate not found")
	}

	if err := h.db.Delete(&cert).Error; err != nil {
		return endpoint.Empty{}, err
	}

	return endpoint.Empty{}, nil
}

// Certificate is public certificate metadata. PEM material is absent from this type.
type Certificate struct {
	ID        uint      `json:"id"`
	DomainID  uint      `json:"domain_id"`
	Issuer    string    `json:"issuer"`
	NotBefore time.Time `json:"not_before" format:"date-time"`
	NotAfter  time.Time `json:"not_after" format:"date-time"`
	AutoRenew bool      `json:"auto_renew"`
	CreatedAt time.Time `json:"created_at" format:"date-time"`
	UpdatedAt time.Time `json:"updated_at" format:"date-time"`
	Domain    Domain    `json:"domain,omitempty"`
}

// CertificateDetails includes the public certificate, never its private key.
// Dates retain the second precision used by the existing details response.
type CertificateDetails struct {
	ID        uint   `json:"id"`
	DomainID  uint   `json:"domain_id"`
	CertPEM   string `json:"cert_pem"`
	Issuer    string `json:"issuer"`
	NotBefore string `json:"not_before" format:"date-time"`
	NotAfter  string `json:"not_after" format:"date-time"`
	AutoRenew bool   `json:"auto_renew"`
	CreatedAt string `json:"created_at" format:"date-time"`
	UpdatedAt string `json:"updated_at" format:"date-time"`
	Domain    Domain `json:"domain,omitempty,omitzero"`
}

type certificatePath struct {
	ID uint32 `path:"id" minimum:"1"`
}

func certificateOf(c models.Certificate) Certificate {
	return Certificate{ID: c.ID, DomainID: c.DomainID, Issuer: c.Issuer, NotBefore: c.NotBefore,
		NotAfter: c.NotAfter, AutoRenew: c.AutoRenew, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, Domain: domainOf(c.Domain)}
}

func certificateDetailsOf(cert models.Certificate) CertificateDetails {
	return CertificateDetails{
		ID:        cert.ID,
		DomainID:  cert.DomainID,
		CertPEM:   cert.CertPEM,
		Issuer:    cert.Issuer,
		NotBefore: cert.NotBefore.Format("2006-01-02T15:04:05Z07:00"),
		NotAfter:  cert.NotAfter.Format("2006-01-02T15:04:05Z07:00"),
		AutoRenew: cert.AutoRenew,
		CreatedAt: cert.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt: cert.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		Domain:    domainOf(cert.Domain),
	}
}
