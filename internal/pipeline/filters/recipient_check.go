package filters

import (
	"context"
	"fmt"
	"strings"

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
// no such mailbox exists; a non-nil error is a real database failure.
func (f *recipientCheckFilter) lookupMailbox(localPart, domain string) (mailboxInfo, error) {
	var mb mailboxInfo
	err := f.db.Table("mailboxes").
		Select("mailboxes.id, mailboxes.quota_bytes, mailboxes.quota_used_bytes").
		Joins("JOIN domains ON domains.id = mailboxes.domain_id").
		Where("mailboxes.local_part = ? AND domains.name = ?", localPart, domain).
		Scan(&mb).Error
	return mb, err
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
		parts := strings.SplitN(rcpt, "@", 2)
		if len(parts) != 2 {
			continue
		}

		mb, err := f.lookupMailbox(parts[0], parts[1])
		if err != nil {
			return f.deferResult(err), nil
		}

		if mb.ID == 0 {
			// No exact mailbox. An explicit alias for the full address claims it
			// (and wins over the subaddressing fallback below).
			var aliasCount int64
			f.db.Table("aliases").
				Joins("JOIN domains ON domains.id = aliases.domain_id").
				Where("aliases.source_address = ?", rcpt).
				Count(&aliasCount)

			if aliasCount == 0 {
				// RFC 5233 subaddressing: when the local part carries a "+detail"
				// tag and no alias claims the full address, retry against the base
				// mailbox (user+tag@domain -> user@domain).
				if base, _, tagged := rmail.StripSubaddress(rcpt); tagged {
					if bp := strings.SplitN(base, "@", 2); len(bp) == 2 {
						mb, err = f.lookupMailbox(bp[0], bp[1])
						if err != nil {
							return f.deferResult(err), nil
						}
					}
				}
			}

			if mb.ID == 0 {
				if aliasCount > 0 {
					// An alias claims the address; it resolves elsewhere, so there
					// is no local mailbox to quota-check here.
					continue
				}
				return &pipeline.FilterResult{
					Type:      pipeline.FilterTypeAction,
					Action:    pipeline.ActionReject,
					RejectMsg: fmt.Sprintf("550 5.1.1 <%s>: Recipient not found", rcpt),
					Log: pipeline.FilterLog{
						Filter: "recipient_check",
						Result: "reject",
						Detail: fmt.Sprintf("recipient %s not found", rcpt),
					},
				}, nil
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
