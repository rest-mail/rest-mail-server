package filters

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

type domainAllowlistConfig struct {
	OnAllow       string `json:"on_allow"`        // "continue_skip_spam"
	OnBlock       string `json:"on_block"`         // "reject"
	RejectMessage string `json:"reject_message"`
}

type domainAllowlistFilter struct {
	db  *gorm.DB
	cfg domainAllowlistConfig
}

func NewDomainAllowlist(db *gorm.DB) pipeline.FilterFactory {
	return func(config []byte) (pipeline.Filter, error) {
		cfg := domainAllowlistConfig{
			OnAllow:       "continue_skip_spam",
			OnBlock:       "reject",
			RejectMessage: "550 Sender blocked by domain policy",
		}
		if len(config) > 0 {
			_ = json.Unmarshal(config, &cfg)
		}
		return &domainAllowlistFilter{db: db, cfg: cfg}, nil
	}
}

func (f *domainAllowlistFilter) Name() string             { return "domain_allowlist" }
func (f *domainAllowlistFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }

func (f *domainAllowlistFilter) Execute(_ context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	sender := email.Envelope.MailFrom
	if sender == "" && len(email.Headers.From) > 0 {
		sender = email.Headers.From[0].Address
	}

	if sender == "" {
		return continueResult("domain_allowlist", "no sender"), nil
	}

	senderDomain := ""
	if parts := strings.SplitN(sender, "@", 2); len(parts) == 2 {
		senderDomain = "@" + parts[1]
	}

	// Check for each recipient's domain
	for _, rcpt := range email.Envelope.RcptTo {
		parts := strings.SplitN(rcpt, "@", 2)
		if len(parts) != 2 {
			continue
		}
		rcptDomain := parts[1]

		// Get domain ID
		var domainID uint
		if err := f.db.Table("domains").Where("name = ?", rcptDomain).Pluck("id", &domainID).Error; err != nil {
			continue
		}

		// Check block list first (exact email, then domain pattern)
		var blockCount int64
		f.db.Table("domain_sender_rules").
			Where("domain_id = ? AND list_type = 'block' AND (pattern = ? OR pattern = ?)",
				domainID, sender, senderDomain).
			Count(&blockCount)

		if blockCount > 0 {
			return &pipeline.FilterResult{
				Type:      pipeline.FilterTypeAction,
				Action:    pipeline.ActionReject,
				RejectMsg: f.cfg.RejectMessage,
				Log: pipeline.FilterLog{
					Filter: "domain_allowlist",
					Result: "blocked",
					Detail: fmt.Sprintf("sender %s blocked for domain %s", sender, rcptDomain),
				},
			}, nil
		}

		// Check allow list
		var allowCount int64
		f.db.Table("domain_sender_rules").
			Where("domain_id = ? AND list_type = 'allow' AND (pattern = ? OR pattern = ?)",
				domainID, sender, senderDomain).
			Count(&allowCount)

		if allowCount > 0 {
			return &pipeline.FilterResult{
				Type:        pipeline.FilterTypeAction,
				Action:      pipeline.ActionContinue,
				SkipFilters: []string{"rspamd", "spamassassin", "greylist"},
				Log: pipeline.FilterLog{
					Filter: "domain_allowlist",
					Result: "allowed",
					Detail: fmt.Sprintf("sender %s allowed for domain %s", sender, rcptDomain),
				},
			}, nil
		}
	}

	return continueResult("domain_allowlist", "no matching rules"), nil
}

func continueResult(filter, detail string) *pipeline.FilterResult {
	return &pipeline.FilterResult{
		Type:   pipeline.FilterTypeAction,
		Action: pipeline.ActionContinue,
		Log: pipeline.FilterLog{
			Filter: filter,
			Result: "neutral",
			Detail: detail,
		},
	}
}
