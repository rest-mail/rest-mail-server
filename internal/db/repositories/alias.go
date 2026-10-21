package repositories

import (
	"strings"

	"github.com/restmail/restmail/internal/db/models"
	rmail "github.com/restmail/restmail/internal/mail"
	"gorm.io/gorm"
)

// AliasClaimsAddress reports whether an explicit alias reserves a hosted
// recipient before plus-address fallback. Like the existing exact lookup, it
// includes inactive aliases: claiming an address does not resolve or forward it.
// liveDomainOnly preserves the pipeline's requirement that domains are active.
// No destination is selected, even if distinct legacy sources normalize alike.
func AliasClaimsAddress(db *gorm.DB, address string, liveDomainOnly bool) (bool, error) {
	canonical, err := rmail.CanonicalMailboxAddress(address)
	if err != nil || rmail.ContainsControlChar(address) {
		return false, nil
	}
	_, domain, _ := strings.Cut(canonical, "@")
	aliases := func() *gorm.DB {
		query := db.Model(&models.Alias{}).
			Joins("JOIN domains ON domains.id = aliases.domain_id").
			Where("domains.name = ?", domain)
		if liveDomainOnly {
			query = query.Where("domains.active")
		}
		return query
	}
	// Keep provisioned identities and exact legacy spellings on the source index.
	var count int64
	if err := aliases().Where("aliases.source_address IN ?", []string{address, canonical}).Count(&count).Error; err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	// Older rows may contain Unicode domains or decomposed local parts. Only
	// inspect sources belonging to this hosted domain (domain_id is indexed),
	// and compare with the same Go NFC/IDNA rules used at provisioning time.
	var sources []string
	if err := aliases().Pluck("aliases.source_address", &sources).Error; err != nil {
		return false, err
	}
	for _, source := range sources {
		if identity, err := rmail.CanonicalMailboxAddress(source); err == nil && identity == canonical {
			return true, nil
		}
	}
	return false, nil
}
