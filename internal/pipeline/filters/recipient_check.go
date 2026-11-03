package filters

import (
	"context"
	"errors"
	"fmt"

	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
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

func (f *recipientCheckFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	// A local forward runs inside its delivery transaction. Keep filter reads
	// and writes on that connection without changing the registered instance.
	if db := pipeline.DBFromContext(ctx); db != nil {
		copy := *f
		copy.db = db
		f = &copy
	}
	for _, rcpt := range email.Envelope.RcptTo {
		_, validationErr := rmail.CanonicalMailboxAddress(rcpt)
		if validationErr != nil || rmail.ContainsControlChar(rcpt) {
			return &pipeline.FilterResult{
				Type: pipeline.FilterTypeAction, Action: pipeline.ActionReject,
				RejectMsg: "550 5.1.1 Invalid recipient",
				Log:       pipeline.FilterLog{Filter: "recipient_check", Result: "reject", Detail: "invalid recipient address"},
			}, nil
		}
		recipient, err := repositories.ResolveRecipient(f.db.WithContext(ctx), rcpt, true)
		if errors.Is(err, repositories.ErrAliasRouting) {
			return &pipeline.FilterResult{Type: pipeline.FilterTypeAction, Action: pipeline.ActionReject,
				RejectMsg: "550 5.1.1 " + rmail.AliasRoutingRefusal,
				Log:       pipeline.FilterLog{Filter: "recipient_check", Result: "reject", Detail: err.Error()}}, nil
		}
		if errors.Is(err, repositories.ErrMailboxUnavailable) {
			return f.rejectRecipient(rcpt), nil
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return f.rejectRecipient(rcpt), nil
		}
		if err != nil {
			return f.deferResult(err), nil
		}
		mb := recipient.Mailbox
		if mb.ID == 0 {
			continue
		}

		// The final delivery reserves bytes under the same owner's lock. This
		// early check preserves the protocol's existing mailbox-full response.
		chain, err := repositories.StorageQuotaChain(f.db.WithContext(ctx), mb.ID, false)
		if err != nil {
			return f.deferResult(err), nil
		}
		remaining, err := repositories.StorageRemaining(f.db.WithContext(ctx), chain)
		if err != nil {
			return f.deferResult(err), nil
		}
		if remaining <= 0 {
			return &pipeline.FilterResult{
				Type:      pipeline.FilterTypeAction,
				Action:    pipeline.ActionReject,
				RejectMsg: fmt.Sprintf("452 4.2.2 <%s>: Mailbox full", rcpt),
				Log: pipeline.FilterLog{
					Filter: "recipient_check",
					Result: "reject",
					Detail: fmt.Sprintf("recipient %s storage quota exhausted", rcpt),
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
