package handlers

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

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
		return MTASTSPolicy{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Domain not found")
	}

	var policy models.MTASTSPolicy
	if err := h.db.Where("domain_id = ?", domainID).First(&policy).Error; err != nil {
		return MTASTSPolicy{}, endpoint.Refuse(http.StatusNotFound, "not_found", "No MTA-STS policy found for this domain")
	}

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
		return endpoint.Status[MTASTSPolicy]{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Domain not found")
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
	if result.RowsAffected == 0 {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusNotFound, "not_found", "No MTA-STS policy found for this domain")
	}

	return endpoint.Empty{}, nil
}

// ServePolicy serves the MTA-STS policy file at /.well-known/mta-sts.txt.
// It extracts the domain from the Host header and returns the appropriate
// text/plain policy file per RFC 8461.
// GET /.well-known/mta-sts.txt
func (h *MTASTSHandler) ServePolicy(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}

	// The Host header carries the A-label form of an internationalized name
	// (#153), in any case, while the domain may be stored as the Unicode name
	// it was entered with, so the name is matched in both forms.
	ascii, err := rmail.DomainASCII(strings.TrimSuffix(host, "."))
	if err != nil {
		http.Error(w, "domain not found", http.StatusNotFound)
		return
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
		http.Error(w, "domain not found", http.StatusNotFound)
		return
	}

	// Look up the MTA-STS policy
	var policy models.MTASTSPolicy
	if err := h.db.Where("domain_id = ? AND active = ?", domain.ID, true).First(&policy).Error; err != nil {
		http.Error(w, "no MTA-STS policy configured", http.StatusNotFound)
		return
	}

	// Build the policy text
	var sb strings.Builder
	sb.WriteString("version: STSv1\n")
	sb.WriteString(fmt.Sprintf("mode: %s\n", policy.Mode))

	// Write each MX host on its own line. A policy's mx patterns are DNS names
	// (RFC 8461 section 3.2), so an internationalized host is written as its
	// A-label. One that is not a valid name fails the fetch rather than serve
	// a policy that silently allows fewer hosts: a sending MTA then keeps the
	// policy it has cached.
	if policy.MXHosts != "" {
		for _, mx := range strings.Split(policy.MXHosts, ",") {
			mx = strings.TrimSpace(mx)
			if mx == "" {
				continue
			}
			pattern, err := mxPatternASCII(mx)
			if err != nil {
				http.Error(w, "MTA-STS policy has an invalid mx host", http.StatusInternalServerError)
				return
			}
			sb.WriteString(fmt.Sprintf("mx: %s\n", pattern))
		}
	}

	sb.WriteString(fmt.Sprintf("max_age: %d\n", policy.MaxAge))

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(sb.String()))
}

// mxPatternASCII is an MTA-STS mx pattern, a host name or a "*." wildcard over
// one, in A-label form.
func mxPatternASCII(pattern string) (string, error) {
	wildcard := strings.HasPrefix(pattern, "*.")
	ascii, err := rmail.DomainASCII(strings.TrimPrefix(pattern, "*."))
	if err != nil {
		return "", err
	}
	if wildcard {
		return "*." + ascii, nil
	}
	return ascii, nil
}
