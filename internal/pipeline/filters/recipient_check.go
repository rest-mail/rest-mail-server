package filters

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

type recipientCheckConfig struct {
	// No config needed; uses the database
}

type recipientCheckFilter struct {
	db *gorm.DB
}

func NewRecipientCheck(db *gorm.DB) pipeline.FilterFactory {
	return func(config []byte) (pipeline.Filter, error) {
		return &recipientCheckFilter{db: db}, nil
	}
}

func (f *recipientCheckFilter) Name() string             { return "recipient_check" }
func (f *recipientCheckFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }

func (f *recipientCheckFilter) Execute(_ context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	for _, rcpt := range email.Envelope.RcptTo {
		// Check if mailbox exists
		var count int64
		parts := strings.SplitN(rcpt, "@", 2)
		if len(parts) != 2 {
			continue
		}

		err := f.db.Table("mailboxes").
			Joins("JOIN domains ON domains.id = mailboxes.domain_id").
			Where("mailboxes.local_part = ? AND domains.name = ?", parts[0], parts[1]).
			Count(&count).Error

		if err != nil {
			return &pipeline.FilterResult{
				Type:   pipeline.FilterTypeAction,
				Action: pipeline.ActionDefer,
				Log: pipeline.FilterLog{
					Filter: "recipient_check",
					Result: "defer",
					Detail: fmt.Sprintf("database error: %v", err),
				},
			}, nil
		}

		if count == 0 {
			// Check aliases
			var aliasCount int64
			f.db.Table("aliases").
				Joins("JOIN domains ON domains.id = aliases.domain_id").
				Where("aliases.source_address = ?", rcpt).
				Count(&aliasCount)

			if aliasCount == 0 {
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

// recipientCheckConfigUnused suppresses unused import warning
var _ json.RawMessage
