package handlers

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
func (h *MTASTSHandler) getPolicy(r *http.Request, in domainPath) (MTASTSPolicy, error) {
	db := h.db.WithContext(r.Context())
	domainID := in.ID

	// Verify domain exists
	var domain models.Domain
	if err := db.First(&domain, domainID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return MTASTSPolicy{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Domain not found")
		}
		return MTASTSPolicy{}, fmt.Errorf("look up MTA-STS domain: %w", err)
	}

	var policy models.MTASTSPolicy
	if err := db.Where("domain_id = ?", domainID).First(&policy).Error; err != nil {
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
func (h *MTASTSHandler) setPolicy(r *http.Request, in setMTASTSInput) (endpoint.Status[MTASTSPolicy], error) {
	db := h.db.WithContext(r.Context())
	domainID := in.ID

	// The body is checked before the domain is looked up: a refused request
	// reaches nothing (#386).
	req := in.Body

	// One transaction holding the domain row: concurrent saves of the policy
	// are serialized, so neither loses the other's fields and two first saves
	// cannot both insert, and the domain's deletion (which locks the same row)
	// either removes the saved policy afterwards or makes this save a 404.
	var domain models.Domain
	var policy models.MTASTSPolicy
	created := false
	err := db.Transaction(func(tx *gorm.DB) error {
		locked, err := lockPolicyDomain(tx, domainID)
		if err != nil {
			return err
		}
		domain = *locked
		result := tx.Where("domain_id = ?", domainID).First(&policy)
		created = errors.Is(result.Error, gorm.ErrRecordNotFound)
		if result.Error != nil && !created {
			return endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to save MTA-STS policy")
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
			saveErr = insertMTASTSPolicy(tx, &policy)
		} else {
			// Save would recreate a row deleted after the lookup. Update only the
			// selected policy, keeping explicit zero/false values and its domain.
			result := tx.Model(&policy).Where("domain_id = ?", domainID).
				Select("mode", "mx_hosts", "max_age", "active").Updates(&policy)
			saveErr = result.Error
			if saveErr == nil && result.RowsAffected == 0 {
				saveErr = fmt.Errorf("MTA-STS policy disappeared before update")
			}
		}
		if saveErr != nil {
			return endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to save MTA-STS policy")
		}
		return nil
	})
	if err != nil {
		return endpoint.Status[MTASTSPolicy]{}, err
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
func (h *MTASTSHandler) deletePolicy(r *http.Request, in domainPath) (endpoint.Empty, error) {
	db := h.db.WithContext(r.Context())
	domainID := in.ID

	// Hold the domain row as a save does, so a delete never lands between a
	// save's read and its update. A missing domain still has no policy: 404.
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: policyDomainLock}).
			Where("id = ?", domainID).Find(&[]models.Domain{}).Error; err != nil {
			return fmt.Errorf("lock MTA-STS domain for delete: %w", err)
		}
		result := tx.Where("domain_id = ?", domainID).Delete(&models.MTASTSPolicy{})
		if result.Error != nil {
			return fmt.Errorf("delete MTA-STS policy: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return endpoint.Refuse(http.StatusNotFound, "not_found", "No MTA-STS policy found for this domain")
		}
		return nil
	})
	return endpoint.Empty{}, err
}

// policyDomainLock is the row lock a domain's MTA-STS or relay save holds on
// the domain for its whole transaction. It conflicts with itself, so saves of
// one domain's policy run one after another, and with the FOR UPDATE domain
// deletion takes, but not with the key-share locks of unrelated inserts.
const policyDomainLock = "NO KEY UPDATE"

// lockPolicyDomain reads the domain row under policyDomainLock in tx: 404
// when the domain does not exist, or no longer does once the lock is granted.
func lockPolicyDomain(tx *gorm.DB, id uint32) (*models.Domain, error) {
	var domain models.Domain
	if err := tx.Clauses(clause.Locking{Strength: policyDomainLock}).First(&domain, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, endpoint.Refuse(http.StatusNotFound, "not_found", "Domain not found")
		}
		return nil, fmt.Errorf("lock policy domain: %w", err)
	}
	return &domain, nil
}

type mtaSTSPolicyFileRequest struct {
	Host string `header:"Host" description:"Authority naming mta-sts.<domain>, normally supplied by the request URL"`
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
func (h *MTASTSHandler) servePolicy(r *http.Request, in mtaSTSPolicyFileRequest) (endpoint.Document, error) {
	host := in.Host
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
	if err := h.db.WithContext(r.Context()).Where("name IN ?", names).First(&domain).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return endpoint.Document{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Domain not found")
		}
		return endpoint.Document{}, fmt.Errorf("look up public MTA-STS domain: %w", err)
	}

	// Look up the MTA-STS policy
	var policy models.MTASTSPolicy
	if err := h.db.WithContext(r.Context()).Where("domain_id = ? AND active = ?", domain.ID, true).First(&policy).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return endpoint.Document{}, endpoint.Refuse(http.StatusNotFound, "not_found", "No MTA-STS policy configured")
		}
		return endpoint.Document{}, fmt.Errorf("look up active public MTA-STS policy: %w", err)
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
