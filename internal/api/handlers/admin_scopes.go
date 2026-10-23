package handlers

import (
	"net/http"
	"strings"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

// filterAdminList is paired with RequireCapabilityOnList on each public route.
// Direct handler tests and global-only handler users have no scoped context.
// Apply the same filtered query to Count and Find, before limit/offset.
func filterAdminList(query *gorm.DB, r *http.Request, capability, resource string) *gorm.DB {
	scopes, restricted := middleware.CapabilityScopes(r, capability)
	if !restricted {
		return query
	}
	var domains, mailboxes []string
	for _, scope := range scopes {
		if scope == grants.All {
			return query
		}
		if name, ok := strings.CutPrefix(scope, "domain:"); ok {
			domains = append(domains, name)
		}
		if address, ok := strings.CutPrefix(scope, "mailbox:"); ok {
			mailboxes = append(mailboxes, address)
		}
	}
	if resource == "domains" {
		if len(domains) == 0 {
			return query.Where("1 = 0")
		}
		return query.Where("lower(domains.name) IN ?", domains)
	}
	condition := query.Session(&gorm.Session{NewDB: true}).Where("1 = 0")
	if len(domains) > 0 {
		condition = condition.Or(resource+".domain_id IN (SELECT id FROM domains WHERE lower(name) IN ?)", domains)
	}
	// A mailbox grant identifies an account, never an alias at that address
	// and never the configuration of its parent domain.
	if resource == "mailboxes" && len(mailboxes) > 0 {
		condition = condition.Or("lower(mailboxes.address) IN ?", mailboxes)
	}
	return query.Where(condition)
}

// scopedDomainAllowed checks a domain named by a request body, after syntax
// validation but before any lookup that could reveal another domain exists.
func scopedDomainAllowed(r *http.Request, capability, name string) bool {
	scopes, restricted := middleware.CapabilityScopes(r, capability)
	if !restricted {
		return true
	}
	for _, scope := range scopes {
		if grants.Covers(scope, grants.Domain(name)) {
			return true
		}
	}
	return false
}

// Parent domain configuration is separately protected by domains:read. An
// administrator of one mailbox may read that mailbox without learning its
// domain's quota, DKIM selector, sending policy or timestamps.
func redactMailboxDomain(r *http.Request, mailbox *models.Mailbox) {
	redactAdminDomain(r, &mailbox.Domain)
}

func redactAdminDomain(r *http.Request, domain *models.Domain) {
	held, authorized := middleware.AuthorizedGrants(r)
	if !authorized {
		return
	}
	scope := grants.Domain(domain.Name)
	if !held.Allows(middleware.CapDomainsRead, scope) || !middleware.CredentialAllows(middleware.GetClaims(r), middleware.CapDomainsRead, scope) {
		*domain = models.Domain{}
	}
}

// AliasScopeOf is its source domain, never the destination mailbox: alias
// routing is domain configuration, even if the source happens to match an account.
func AliasScopeOf(db *gorm.DB) middleware.ScopeOf {
	return func(r *http.Request) (string, bool, error) {
		id, err := pathID(r, "id")
		if err != nil {
			return "", false, nil
		}
		var names []string
		err = db.Table("aliases").Joins("JOIN domains ON domains.id = aliases.domain_id").Where("aliases.id = ?", id).Pluck("domains.name", &names).Error
		if err != nil || len(names) == 0 {
			return "", false, err
		}
		return grants.Domain(names[0]), true, nil
	}
}
