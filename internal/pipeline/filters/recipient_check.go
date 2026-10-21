package filters

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	rmail "github.com/restmail/restmail/internal/mail"
	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

type recipientCheckFilter struct {
	db *gorm.DB
}

func NewRecipientCheck(db *gorm.DB) pipeline.FilterFactory {
	return func(config []byte) (pipeline.Filter, error) {
		return &recipientCheckFilter{db: db}, nil
	}
}

func (f *recipientCheckFilter) Name() string              { return "recipient_check" }
func (f *recipientCheckFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }

type mailboxInfo struct {
	ID             uint
	QuotaBytes     int64
	QuotaUsedBytes int64
}

// lookupMailbox finds a mailbox by local part and domain name. A zero ID means
// no such mailbox exists. ErrMailboxUnavailable blocks fallback to another
// identity; any other non-nil error is a database failure.
//
// A domain that is not live is not served at all: it has no usable certificate,
// so mail for it cannot be carried over TLS (issues #289, #290). Its mailboxes
// are therefore invisible here, and mail addressed to them is refused like any
// other unknown recipient.
func (f *recipientCheckFilter) lookupMailbox(localPart, domain string) (mailboxInfo, error) {
	var mailbox models.Mailbox
	err := repositories.FindActiveMailboxByAddress(f.db, localPart+"@"+domain, &mailbox)
	if errors.Is(err, repositories.ErrMailboxUnavailable) {
		return mailboxInfo{}, err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return mailboxInfo{}, nil
	}
	if err != nil {
		return mailboxInfo{}, err
	}
	var live int64
	if err := f.db.Model(&models.Domain{}).Where("id = ? AND active", mailbox.DomainID).Count(&live).Error; err != nil {
		return mailboxInfo{}, err
	}
	if live == 0 {
		return mailboxInfo{}, nil
	}
	return mailboxInfo{ID: mailbox.ID, QuotaBytes: mailbox.QuotaBytes, QuotaUsedBytes: mailbox.QuotaUsedBytes}, nil
}

func (f *recipientCheckFilter) deferResult(err error) *pipeline.FilterResult {
	return &pipeline.FilterResult{
		Type:   pipeline.FilterTypeAction,
		Action: pipeline.ActionDefer,
		Log: pipeline.FilterLog{
			Filter: "recipient_check",
			Result: "defer",
			Detail: fmt.Sprintf("database error: %v", err),
		},
	}
}

func (f *recipientCheckFilter) Execute(_ context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	for _, rcpt := range email.Envelope.RcptTo {
		_, validationErr := rmail.CanonicalMailboxAddress(rcpt)
		if validationErr != nil || rmail.ContainsControlChar(rcpt) {
			return &pipeline.FilterResult{
				Type: pipeline.FilterTypeAction, Action: pipeline.ActionReject,
				RejectMsg: "550 5.1.1 Invalid recipient",
				Log:       pipeline.FilterLog{Filter: "recipient_check", Result: "reject", Detail: "invalid recipient address"},
			}, nil
		}
		parts := strings.SplitN(rcpt, "@", 2)

		mb, err := f.lookupMailbox(parts[0], parts[1])
		if errors.Is(err, repositories.ErrMailboxUnavailable) {
			return f.rejectRecipient(rcpt), nil
		}
		if err != nil {
			return f.deferResult(err), nil
		}

		if mb.ID == 0 {
			// No exact mailbox. An explicit alias for the full address claims it
			// (and wins over the subaddressing fallback below).
			aliasClaims, err := repositories.AliasClaimsAddress(f.db, rcpt, true)
			if err != nil {
				return f.deferResult(err), nil
			}

			if !aliasClaims {
				// RFC 5233 subaddressing: when the local part carries a "+detail"
				// tag and no alias claims the full address, retry against the base
				// mailbox (user+tag@domain -> user@domain).
				if base, _, tagged := rmail.StripSubaddress(rcpt); tagged {
					if bp := strings.SplitN(base, "@", 2); len(bp) == 2 {
						mb, err = f.lookupMailbox(bp[0], bp[1])
						if errors.Is(err, repositories.ErrMailboxUnavailable) {
							return f.rejectRecipient(rcpt), nil
						}
						if err != nil {
							return f.deferResult(err), nil
						}
					}
				}
			}

			if mb.ID == 0 {
				if aliasClaims {
					// An alias claims the address; it resolves elsewhere, so there
					// is no local mailbox to quota-check here.
					continue
				}
				return f.rejectRecipient(rcpt), nil
			}
		}

		// Check quota
		if mb.QuotaBytes > 0 && mb.QuotaUsedBytes >= mb.QuotaBytes {
			return &pipeline.FilterResult{
				Type:      pipeline.FilterTypeAction,
				Action:    pipeline.ActionReject,
				RejectMsg: fmt.Sprintf("452 4.2.2 <%s>: Mailbox full", rcpt),
				Log: pipeline.FilterLog{
					Filter: "recipient_check",
					Result: "reject",
					Detail: fmt.Sprintf("recipient %s over quota (%d/%d bytes)", rcpt, mb.QuotaUsedBytes, mb.QuotaBytes),
				},
			}, nil
		}
	}

	return &pipeline.FilterResult{
		Type:   pipeline.FilterTypeAction,
		Action: pipeline.ActionContinue,
		Log: pipeline.FilterLog{
			Filter: "recipient_check",
			Result: "pass",
			Detail: fmt.Sprintf("all %d recipients valid", len(email.Envelope.RcptTo)),
		},
	}, nil
}

func (f *recipientCheckFilter) rejectRecipient(rcpt string) *pipeline.FilterResult {
	return &pipeline.FilterResult{
		Type:      pipeline.FilterTypeAction,
		Action:    pipeline.ActionReject,
		RejectMsg: fmt.Sprintf("550 5.1.1 <%s>: Recipient not found", rcpt),
		Log: pipeline.FilterLog{
			Filter: "recipient_check", Result: "reject",
			Detail: fmt.Sprintf("recipient %s not found", rcpt),
		},
	}
}
