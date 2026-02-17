package filters

import (
	"context"
	"fmt"
	"strings"

	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

type contactWhitelistFilter struct {
	db *gorm.DB
}

// NewContactWhitelist creates a factory for contact whitelist filters.
// This filter checks if the sender is in the recipient's contacts list
// and skips spam scoring for known senders.
func NewContactWhitelist(db *gorm.DB) pipeline.FilterFactory {
	return func(config []byte) (pipeline.Filter, error) {
		return &contactWhitelistFilter{db: db}, nil
	}
}

func (f *contactWhitelistFilter) Name() string             { return "contact_whitelist" }
func (f *contactWhitelistFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }

func (f *contactWhitelistFilter) Execute(_ context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	sender := email.Envelope.MailFrom
	if sender == "" && len(email.Headers.From) > 0 {
		sender = email.Headers.From[0].Address
	}

	if sender == "" {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "contact_whitelist",
				Result: "skip",
				Detail: "no sender to check",
			},
		}, nil
	}

	// Check each recipient's contacts
	for _, rcpt := range email.Envelope.RcptTo {
		parts := strings.SplitN(rcpt, "@", 2)
		if len(parts) != 2 {
			continue
		}

		var trustLevel string
		err := f.db.Table("contacts").
			Joins("JOIN mailboxes ON mailboxes.id = contacts.mailbox_id").
			Joins("JOIN domains ON domains.id = mailboxes.domain_id").
			Where("mailboxes.local_part = ? AND domains.name = ? AND contacts.email = ?",
				parts[0], parts[1], sender).
			Pluck("trust_level", &trustLevel).Error

		if err != nil || trustLevel == "" {
			continue // No contact found for this recipient
		}

		// Handle trust levels
		switch trustLevel {
		case "blocked":
			return &pipeline.FilterResult{
				Type:      pipeline.FilterTypeAction,
				Action:    pipeline.ActionReject,
				RejectMsg: "550 Sender blocked by recipient",
				Log: pipeline.FilterLog{
					Filter: "contact_whitelist",
					Result: "blocked",
					Detail: fmt.Sprintf("sender %s blocked by %s", sender, rcpt),
				},
			}, nil
		case "trusted":
			return &pipeline.FilterResult{
				Type:        pipeline.FilterTypeAction,
				Action:      pipeline.ActionContinue,
				SkipFilters: []string{"rspamd", "spamassassin", "greylist"},
				Log: pipeline.FilterLog{
					Filter: "contact_whitelist",
					Result: "pass",
					Detail: fmt.Sprintf("sender %s is trusted contact of %s", sender, rcpt),
				},
			}, nil
		case "auto":
			return &pipeline.FilterResult{
				Type:        pipeline.FilterTypeAction,
				Action:      pipeline.ActionContinue,
				SkipFilters: []string{"rspamd", "spamassassin"},
				Log: pipeline.FilterLog{
					Filter: "contact_whitelist",
					Result: "pass",
					Detail: fmt.Sprintf("sender %s is auto-collected contact of %s", sender, rcpt),
				},
			}, nil
		}
	}

	return &pipeline.FilterResult{
		Type:   pipeline.FilterTypeAction,
		Action: pipeline.ActionContinue,
		Log: pipeline.FilterLog{
			Filter: "contact_whitelist",
			Result: "unknown",
			Detail: fmt.Sprintf("sender %s not in any recipient's contacts", sender),
		},
	}, nil
}
