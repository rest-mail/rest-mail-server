package repositories

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"gorm.io/gorm"
)

const MaxAliasHops = 8

var ErrAliasRouting = errors.New("alias routing refused")

type Recipient struct {
	Mailbox          models.Mailbox
	Address          string
	DeliveredTo      string
	ForwardingDomain string
	Alias            bool
}

func aliasRoutingError(reason string) error {
	return fmt.Errorf("%w: %s: %w", ErrAliasRouting, reason, gorm.ErrRecordNotFound)
}

func findRecipientAlias(db *gorm.DB, address string, liveDomainOnly bool) (models.Alias, error) {
	canonical, err := rmail.CanonicalMailboxAddress(address)
	if err != nil || rmail.ContainsControlChar(address) {
		return models.Alias{}, gorm.ErrRecordNotFound
	}
	query := db.Model(&models.Alias{}).Select("aliases.*").Preload("Domain").
		Joins("JOIN domains ON domains.id = aliases.domain_id").
		Where("domains.name = ?", rmail.DomainFromAddress(canonical))
	if liveDomainOnly {
		query = query.Where("domains.active")
	}
	var candidates []models.Alias
	if err := query.Session(&gorm.Session{}).Where("aliases.source_address IN ?", []string{address, canonical}).Find(&candidates).Error; err != nil {
		return models.Alias{}, err
	}
	var legacy []models.Alias
	if err := query.Find(&legacy).Error; err != nil {
		return models.Alias{}, err
	}
	for _, alias := range legacy {
		if alias.SourceAddress == address || alias.SourceAddress == canonical {
			continue
		}
		if identity, err := rmail.CanonicalMailboxAddress(alias.SourceAddress); err == nil && identity == canonical {
			candidates = append(candidates, alias)
		}
	}
	if len(candidates) > 1 {
		return models.Alias{}, aliasRoutingError("ambiguous alias address")
	}
	if len(candidates) == 0 {
		return models.Alias{}, gorm.ErrRecordNotFound
	}
	return candidates[0], nil
}

func ResolveRecipient(db *gorm.DB, address string, liveDomainOnly bool) (Recipient, error) {
	resolved := Recipient{Address: address}
	seen := make(map[string]bool)
	for hops := 0; ; {
		if resolved.Alias {
			if rmail.ValidateAddress(resolved.Address) != nil {
				return Recipient{}, aliasRoutingError("invalid alias destination")
			}
			parsed, err := mail.ParseAddress(resolved.Address)
			if err != nil {
				return Recipient{}, aliasRoutingError("invalid alias destination")
			}
			parsed.Name = ""
			resolved.Address = strings.TrimSuffix(strings.TrimPrefix(parsed.String(), "<"), ">")
			hosted, err := IsHostedDomain(db, rmail.DomainFromAddress(parsed.Address))
			if err != nil {
				return Recipient{}, err
			}
			if !hosted {
				return resolved, nil
			}
		}
		canonical, err := rmail.CanonicalMailboxAddress(resolved.Address)
		if err != nil || rmail.ContainsControlChar(resolved.Address) {
			if resolved.Alias {
				return Recipient{}, aliasRoutingError("invalid alias destination")
			}
			return Recipient{}, gorm.ErrRecordNotFound
		}
		if seen[canonical] {
			return Recipient{}, aliasRoutingError("alias loop")
		}
		seen[canonical] = true
		var mailbox models.Mailbox
		err = FindReceivingMailboxByAddress(db, resolved.Address, &mailbox)
		// FindReceivingMailboxByAddress already refuses a mailbox on a domain that
		// is not live, so every caller gets delivery's rule (#1845).
		if err == nil {
			resolved.Mailbox, resolved.Address = mailbox, mailbox.Address
			return resolved, nil
		}
		if errors.Is(err, ErrMailboxUnavailable) || !errors.Is(err, gorm.ErrRecordNotFound) {
			return Recipient{}, err
		}
		alias, err := findRecipientAlias(db, resolved.Address, liveDomainOnly)
		if err == nil {
			if !alias.Active {
				return Recipient{}, aliasRoutingError("inactive alias")
			}
			if !alias.Domain.Active {
				return Recipient{}, aliasRoutingError("alias domain is not active")
			}
			if hops == MaxAliasHops {
				return Recipient{}, aliasRoutingError(fmt.Sprintf("alias chain exceeds %d hops", MaxAliasHops))
			}
			if !resolved.Alias {
				resolved.ForwardingDomain = rmail.DomainFromAddress(canonical)
				resolved.DeliveredTo = address
			}
			resolved.Alias = true
			resolved.Address = alias.DestinationAddress
			hops++
			continue
		}
		if errors.Is(err, ErrAliasRouting) || !errors.Is(err, gorm.ErrRecordNotFound) {
			return Recipient{}, err
		}
		if base, _, tagged := rmail.StripSubaddress(resolved.Address); tagged {
			if err := FindReceivingMailboxByAddress(db, base, &mailbox); err == nil {
				resolved.Mailbox, resolved.Address, resolved.DeliveredTo = mailbox, mailbox.Address, address
				return resolved, nil
			} else if errors.Is(err, ErrMailboxUnavailable) || !errors.Is(err, gorm.ErrRecordNotFound) {
				return Recipient{}, err
			}
		}
		if resolved.Alias {
			return Recipient{}, aliasRoutingError("alias destination is not a receiving mailbox")
		}
		return Recipient{}, gorm.ErrRecordNotFound
	}
}
