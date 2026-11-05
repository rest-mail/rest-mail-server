package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"gorm.io/gorm"
)

// accountAddresses is what GET /api/v1/accounts/{id}/addresses answers: the
// addresses that deliver to the account and the addresses it may send from.
type accountAddresses struct {
	AccountID uint                 `json:"account_id" description:"The account (mailbox) these addresses belong to"`
	Address   string               `json:"address" format:"idn-email" description:"The account's own address, its primary address"`
	Receives  []receivingAddress   `json:"receives" description:"Addresses whose mail is delivered to this account: its own address first, then every active alias on an active domain whose destination is this account, by address"`
	SendsAs   []sendingAddressMode `json:"sends_as" description:"Addresses this account may put in From when sending, its own address first and then by address, each with the modes sending accepts. The same check sendMessage makes decides each one, so a From listed here is accepted and one missing is refused"`
}

type receivingAddress struct {
	Address string `json:"address" format:"idn-email" description:"An address that delivers to this account"`
	Primary bool   `json:"primary" description:"True for the account's own address; false for an alias"`
}

type sendingAddressMode struct {
	Address  string `json:"address" format:"idn-email" description:"A mailbox address this account may send from"`
	Primary  bool   `json:"primary" description:"True for the account's own address"`
	From     bool   `json:"from" description:"Sending with this From and on_behalf false is allowed: mail:send on the account's own mailbox, mail:send_as on another"`
	OnBehalf bool   `json:"on_behalf" description:"Sending with this From and on_behalf true is allowed: mail:send_on_behalf on that mailbox, with the account's own address as RFC 5322 Sender"`
}

// addresses answers GET /api/v1/accounts/{id}/addresses. It needs mail:read on
// the account, like the account itself; an account the caller may not read is
// 404, as for getAccount.
func (h *AccountHandler) addresses(r *http.Request, in accountPath) (accountAddresses, error) {
	h = h.forRequest(r)
	claims := middleware.GetClaims(r)
	if claims == nil {
		return accountAddresses{}, errAuthenticationRequired
	}
	mb, err := mayReach(h.db, claims, uint(in.ID), models.CapMailRead)
	if err != nil {
		if errors.Is(err, errNoAccess) {
			return accountAddresses{}, errAccountNotFound
		}
		return accountAddresses{}, err
	}
	receives, err := receivingAddresses(h.db, mb)
	if err != nil {
		return accountAddresses{}, err
	}
	// The account's own request answers for the credential it is made with,
	// as sending would. Anyone else reading the account is told what the
	// account may send from when it signs in itself.
	sender := claims
	if claims.AccountID != mb.ID {
		sender = &auth.Claims{AccountID: mb.ID, Address: mb.Address}
	}
	sends, err := sendingAddresses(h.db, sender, mb)
	if err != nil {
		return accountAddresses{}, err
	}
	return accountAddresses{AccountID: mb.ID, Address: mb.Address, Receives: receives, SendsAs: sends}, nil
}

// receivingAddresses is the account's own address, then each active alias on
// an active domain whose destination is the account. Destinations are
// compared in the canonical form a hosted mailbox is provisioned in, so an
// alias stored in another spelling of the address still counts. Only the
// aliases' sources are returned: they are the addresses that reach this
// account, and nothing about any other account is.
func receivingAddresses(db *gorm.DB, mb *models.Mailbox) ([]receivingAddress, error) {
	own, err := rmail.CanonicalMailboxAddress(mb.Address)
	if err != nil {
		own = strings.ToLower(mb.Address)
	}
	var rows []models.Alias
	if err := db.Model(&models.Alias{}).
		Joins("JOIN domains ON domains.id = aliases.domain_id").
		Where("aliases.active AND domains.active").
		Select("aliases.source_address", "aliases.destination_address").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list aliases: %w", err)
	}
	seen := map[string]bool{own: true}
	var aliases []receivingAddress
	for _, row := range rows {
		destination, err := rmail.CanonicalMailboxAddress(row.DestinationAddress)
		if err != nil || destination != own {
			continue
		}
		key, err := rmail.CanonicalMailboxAddress(row.SourceAddress)
		if err != nil {
			key = strings.ToLower(row.SourceAddress)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		aliases = append(aliases, receivingAddress{Address: row.SourceAddress})
	}
	sort.Slice(aliases, func(i, j int) bool { return aliases[i].Address < aliases[j].Address })
	return append([]receivingAddress{{Address: mb.Address, Primary: true}}, aliases...), nil
}

// sendingAddresses asks maySendFrom, the check sendMessage makes on From, about
// every mailbox claims could send from: its own, and each one a send capability
// it holds covers. Nothing is decided here; the grants only narrow which
// mailboxes are worth asking about.
func sendingAddresses(db *gorm.DB, claims *auth.Claims, mb *models.Mailbox) ([]sendingAddressMode, error) {
	held, err := grants.Load(db, claims.AccountID, time.Now())
	if err != nil {
		return nil, err
	}
	scopes := map[string]bool{}
	for _, capability := range []string{models.CapMailSend, models.CapMailSendAs, models.CapMailSendOnBehalf} {
		for _, scope := range held.Scopes(capability) {
			scopes[scope] = true
		}
	}
	candidates := []models.Mailbox{*mb}
	if len(scopes) > 0 {
		query := db.Model(&models.Mailbox{}).Joins("JOIN domains ON domains.id = mailboxes.domain_id").Where("mailboxes.id <> ?", mb.ID)
		if !scopes[grants.All] {
			var addresses, domains []string
			for scope := range scopes {
				if address, ok := strings.CutPrefix(scope, "mailbox:"); ok {
					addresses = append(addresses, address)
				} else if domain, ok := strings.CutPrefix(scope, "domain:"); ok {
					domains = append(domains, domain)
				}
			}
			query = query.Where("LOWER(mailboxes.address) IN ? OR LOWER(domains.name) IN ?", append(addresses, ""), append(domains, ""))
		}
		var others []models.Mailbox
		if err := query.Select("mailboxes.*").Order("mailboxes.address").Find(&others).Error; err != nil {
			return nil, fmt.Errorf("list sending candidates: %w", err)
		}
		candidates = append(candidates, others...)
	}
	var out []sendingAddressMode
	for _, candidate := range candidates {
		mode := sendingAddressMode{Address: candidate.Address, Primary: candidate.ID == mb.ID}
		for _, onBehalf := range []bool{false, true} {
			sender, err := maySendFrom(db, claims, candidate.Address, onBehalf)
			if err != nil && !refusedAccount(err) {
				return nil, fmt.Errorf("check sender %d: %w", candidate.ID, err)
			}
			// maySendFrom finds the mailbox by address; only a match on this
			// candidate counts.
			allowed := err == nil && sender.ID == candidate.ID
			if onBehalf {
				mode.OnBehalf = allowed
			} else {
				mode.From = allowed
			}
		}
		if mode.From || mode.OnBehalf {
			out = append(out, mode)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Primary != out[j].Primary {
			return out[i].Primary
		}
		return out[i].Address < out[j].Address
	})
	if out == nil {
		out = []sendingAddressMode{}
	}
	return out, nil
}
