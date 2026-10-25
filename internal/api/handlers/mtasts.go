package handlers

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/db/models"
	rmail "github.com/restmail/restmail/internal/mail"
	"gorm.io/gorm"
)

// MTASTSHandler manages MTA-STS policies via admin API and serves
// the well-known policy file for external MTA consumption.
type MTASTSHandler struct {
	db *gorm.DB
}

func NewMTASTSHandler(db *gorm.DB) *MTASTSHandler {
	return &MTASTSHandler{db: db}
}

// getPolicy returns the MTA-STS policy for a domain.
// GET /api/v1/admin/domains/{id}/mta-sts
func (h *MTASTSHandler) getPolicy(_ *http.Request, in domainPath) (MTASTSPolicy, error) {
	domainID := in.ID

	// Verify domain exists
	var domain models.Domain
	if err := h.db.First(&domain, domainID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return MTASTSPolicy{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Domain not found")
		}
		return MTASTSPolicy{}, fmt.Errorf("look up MTA-STS domain: %w", err)
	}

	var policy models.MTASTSPolicy
	if err := h.db.Where("domain_id = ?", domainID).First(&policy).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return MTASTSPolicy{}, endpoint.Refuse(http.StatusNotFound, "not_found", "No MTA-STS policy found for this domain")
		}
		return MTASTSPolicy{}, fmt.Errorf("look up MTA-STS policy: %w", err)
	}

	policy.Domain = domain
	return mtaSTSPolicyOf(policy), nil
}

// setPolicy creates or updates the MTA-STS policy for a domain.
// PUT /api/v1/admin/domains/{id}/mta-sts
func (h *MTASTSHandler) setPolicy(_ *http.Request, in setMTASTSInput) (endpoint.Status[MTASTSPolicy], error) {
	domainID := in.ID

	// The body is checked before the domain is looked up: a refused request
	// reaches nothing (#386).
	req := in.Body

	// Verify domain exists
	var domain models.Domain
	if err := h.db.First(&domain, domainID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return endpoint.Status[MTASTSPolicy]{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Domain not found")
		}
		return endpoint.Status[MTASTSPolicy]{}, fmt.Errorf("look up MTA-STS domain for update: %w", err)
	}

	// Upsert: find existing or create new
	var policy models.MTASTSPolicy
	result := h.db.Where("domain_id = ?", domainID).First(&policy)
	created := errors.Is(result.Error, gorm.ErrRecordNotFound)
	if result.Error != nil && !created {
		return endpoint.Status[MTASTSPolicy]{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to save MTA-STS policy")
	}

	if created {
		// Create new policy
		policy = models.MTASTSPolicy{
			DomainID: uint(domainID),
			Mode:     "testing",
			MaxAge:   604800,
			Active:   true,
		}
	}

	if req.Mode != "" {
		policy.Mode = req.Mode
	}
	if req.MXHosts != "" {
		policy.MXHosts = req.MXHosts
	}
	if req.MaxAge != nil {
		policy.MaxAge = *req.MaxAge
	}
	if req.Active != nil {
		policy.Active = *req.Active
	}

	var saveErr error
	if created {
		saveErr = insertMTASTSPolicy(h.db, &policy)
	} else {
		saveErr = h.db.Save(&policy).Error
	}
	if saveErr != nil {
		return endpoint.Status[MTASTSPolicy]{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to save MTA-STS policy")
	}

	// Populate the response after saving, so GORM does not write the domain
	// association as part of a policy update.
	policy.Domain = domain
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	return endpoint.Status[MTASTSPolicy]{Code: status, Value: mtaSTSPolicyOf(policy)}, nil
}

// Non-nil pointers preserve explicit zero/false instead of GORM replacing
// them with the model's default:604800/default:true on insertion. Overlay
// only this insert, keeping ordinary model creation and existing-row saves
// unchanged. Use the model's configured table name, including any prefix.
func insertMTASTSPolicy(db *gorm.DB, policy *models.MTASTSPolicy) error {
	row := struct {
		models.MTASTSPolicy
		MaxAge *int
		Active *bool
	}{MTASTSPolicy: *policy, MaxAge: &policy.MaxAge, Active: &policy.Active}
	err := db.Table(db.NamingStrategy.TableName("MTASTSPolicy")).Create(&row).Error
	*policy = row.MTASTSPolicy
	return err
}

// deletePolicy removes the MTA-STS policy for a domain.
// DELETE /api/v1/admin/domains/{id}/mta-sts
func (h *MTASTSHandler) deletePolicy(_ *http.Request, in domainPath) (endpoint.Empty, error) {
	domainID := in.ID

	result := h.db.Where("domain_id = ?", domainID).Delete(&models.MTASTSPolicy{})
	if result.Error != nil {
		return endpoint.Empty{}, fmt.Errorf("delete MTA-STS policy: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusNotFound, "not_found", "No MTA-STS policy found for this domain")
	}

	return endpoint.Empty{}, nil
}

// RegisterPolicyFile mounts the MTA-STS policy file, which any MTA may fetch (RFC 8461 §3.3).
func (h *MTASTSHandler) RegisterPolicyFile(rs endpoint.Routes) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/.well-known/mta-sts.txt",
		OperationID: "mtaStsPolicyFile", Summary: "MTA-STS policy file (RFC 8461)", Tags: []string{"System"},
		Description: "Served to external MTAs from mta-sts.<domain>: the domain is the Host's, without its mta-sts. " +
			"label. 404 for a domain not hosted here or without an active policy; a sending MTA then keeps " +
			"the policy it has cached.",
		MediaType: "text/plain", Security: oapifly.Public, Errors: []int{http.StatusNotFound}}, h.servePolicy)
}

// servePolicy serves the MTA-STS policy file at /.well-known/mta-sts.txt.
// It extracts the domain from the Host header and returns the appropriate
// text/plain policy file per RFC 8461.
// GET /.well-known/mta-sts.txt
func (h *MTASTSHandler) servePolicy(r *http.Request, _ endpoint.Empty) (endpoint.Document, error) {
	host := r.Host
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}

	// The Host header carries the A-label form of an internationalized name
	// (#153), in any case, while the domain may be stored as the Unicode name
	// it was entered with, so the name is matched in both forms.
	ascii, err := rmail.DomainASCII(strings.TrimSuffix(host, "."))
	if err != nil {
		return endpoint.Document{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Domain not found")
	}
	// MTA-STS policies are served from mta-sts.<domain>, so strip that prefix
	ascii = strings.TrimPrefix(ascii, "mta-sts.")
	names := []string{ascii}
	if unicode, err := rmail.DomainUnicode(ascii); err == nil && unicode != ascii {
		names = append(names, unicode)
	}

	// Look up the domain
	var domain models.Domain
	if err := h.db.Where("name IN ?", names).First(&domain).Error; err != nil {
		return endpoint.Document{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Domain not found")
	}

	// Look up the MTA-STS policy
	var policy models.MTASTSPolicy
	if err := h.db.Where("domain_id = ? AND active = ?", domain.ID, true).First(&policy).Error; err != nil {
		return endpoint.Document{}, endpoint.Refuse(http.StatusNotFound, "not_found", "No MTA-STS policy configured")
	}

	// The policy file, as the _mta-sts record's id is derived from it. One whose
	// mx host is not a valid name fails the fetch rather than serve a policy that
	// silently allows fewer hosts: a sending MTA then keeps the policy it has
	// cached.
	text, err := policy.Text()
	if err != nil {
		return endpoint.Document{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "MTA-STS policy has an invalid mx host")
	}
	return endpoint.Document{ContentType: "text/plain; charset=utf-8", Body: []byte(text)}, nil
}
