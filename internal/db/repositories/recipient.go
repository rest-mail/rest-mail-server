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

// ErrDomainRetired is a recipient on a domain deleted through the API and not restored.
// It is not gorm.ErrRecordNotFound: the owner's decision on #1870 is that this mail waits
// (4xx), so that it arrives if the domain comes back, where an absent recipient is refused.
var ErrDomainRetired = errors.New("recipient domain is retired")

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

// IsRetiredDomain reports whether the domain was deleted through the API and can still
// be restored. A live domain that has since taken the name answers for it instead, and
// a name that is not a domain is simply not retired.
func IsRetiredDomain(db *gorm.DB, domain string) (bool, error) {
	ascii, err := rmail.DomainASCII(domain)
	if err != nil {
		return false, nil
	}
	var n int64
	err = db.Model(&models.DomainRetirement{}).
		Where("name = ? AND NOT EXISTS (SELECT 1 FROM domains WHERE domains.name = domain_retirements.name)", ascii).
		Count(&n).Error
	return n > 0, err
}

// absentRecipientError is why an address no mailbox or alias answers for has no
// recipient: its domain is retired, or it is simply not here. Setting up a live domain
// asks only about that domain's own route, so it is never told to wait.
func absentRecipientError(db *gorm.DB, domain string, setupDomainID uint) error {
	if setupDomainID != 0 {
		return gorm.ErrRecordNotFound
	}
	retired, err := IsRetiredDomain(db, domain)
	if err != nil {
		return err
	}
	if retired {
		return ErrDomainRetired
	}
	return gorm.ErrRecordNotFound
}

func ResolveRecipient(db *gorm.DB, address string, liveDomainOnly bool) (Recipient, error) {
	return resolveRecipient(db, address, liveDomainOnly, 0)
}

func ResolveSetupRecipient(db *gorm.DB, address string, domainID uint) (Recipient, error) {
	return resolveRecipient(db, address, false, domainID)
}

func resolveRecipient(db *gorm.DB, address string, liveDomainOnly bool, setupDomainID uint) (Recipient, error) {
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
				// An alias into a retired domain waits with it: sent outbound, the
				// message would leave for wherever that name's MX now points.
				if err := absentRecipientError(db, rmail.DomainFromAddress(parsed.Address), setupDomainID); !errors.Is(err, gorm.ErrRecordNotFound) {
					return Recipient{}, err
				}
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
		err = findReceivingMailbox(db, resolved.Address, &mailbox, setupDomainID)
		// findReceivingMailbox already refuses a mailbox on a domain that is not
		// live, so every caller gets delivery's rule (#1845). Only the domain being
		// set up is excused, because its postmaster route is checked before it goes
		// live.
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
			if !alias.Domain.Active && alias.DomainID != setupDomainID {
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
			if err := findReceivingMailbox(db, base, &mailbox, setupDomainID); err == nil {
				resolved.Mailbox, resolved.Address, resolved.DeliveredTo = mailbox, mailbox.Address, address
				return resolved, nil
			} else if errors.Is(err, ErrMailboxUnavailable) || !errors.Is(err, gorm.ErrRecordNotFound) {
				return Recipient{}, err
			}
		}
		if resolved.Alias {
			return Recipient{}, aliasRoutingError("alias destination is not a receiving mailbox")
		}
		return Recipient{}, absentRecipientError(db, rmail.DomainFromAddress(canonical), setupDomainID)
	}
}
