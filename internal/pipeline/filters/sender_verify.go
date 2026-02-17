package filters

import (
	"context"
	"fmt"
	"strings"

	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

type senderVerifyFilter struct {
	db *gorm.DB
}

func NewSenderVerify(db *gorm.DB) pipeline.FilterFactory {
	return func(config []byte) (pipeline.Filter, error) {
		return &senderVerifyFilter{db: db}, nil
	}
}

func (f *senderVerifyFilter) Name() string             { return "sender_verify" }
func (f *senderVerifyFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }

func (f *senderVerifyFilter) Execute(_ context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	sender := email.Envelope.MailFrom
	if sender == "" && len(email.Headers.From) > 0 {
		sender = email.Headers.From[0].Address
	}

	if sender == "" {
		return &pipeline.FilterResult{
			Type:      pipeline.FilterTypeAction,
			Action:    pipeline.ActionReject,
			RejectMsg: "550 Sender address required",
			Log: pipeline.FilterLog{
				Filter: "sender_verify",
				Result: "reject",
				Detail: "no sender address",
			},
		}, nil
	}

	parts := strings.SplitN(sender, "@", 2)
	if len(parts) != 2 {
		return &pipeline.FilterResult{
			Type:      pipeline.FilterTypeAction,
			Action:    pipeline.ActionReject,
			RejectMsg: "550 Invalid sender address format",
			Log: pipeline.FilterLog{
				Filter: "sender_verify",
				Result: "reject",
				Detail: "invalid address format",
			},
		}, nil
	}

	// For outbound: verify the sender mailbox exists in our system
	var count int64
	f.db.Table("mailboxes").
		Joins("JOIN domains ON domains.id = mailboxes.domain_id").
		Where("mailboxes.local_part = ? AND domains.name = ?", parts[0], parts[1]).
		Count(&count)

	if count == 0 {
		return &pipeline.FilterResult{
			Type:      pipeline.FilterTypeAction,
			Action:    pipeline.ActionReject,
			RejectMsg: fmt.Sprintf("550 <%s>: Sender not authorized", sender),
			Log: pipeline.FilterLog{
				Filter: "sender_verify",
				Result: "reject",
				Detail: fmt.Sprintf("sender %s not found in local mailboxes", sender),
			},
		}, nil
	}

	return &pipeline.FilterResult{
		Type:   pipeline.FilterTypeAction,
		Action: pipeline.ActionContinue,
		Log: pipeline.FilterLog{
			Filter: "sender_verify",
			Result: "pass",
			Detail: fmt.Sprintf("sender %s verified", sender),
		},
	}, nil
}
