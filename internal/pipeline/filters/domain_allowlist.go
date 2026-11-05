package filters

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"gorm.io/gorm"
)

type domainAllowlistConfig struct {
	OnAllow       string `json:"on_allow"` // "continue_skip_spam"
	OnBlock       string `json:"on_block"` // "reject"
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

func (f *domainAllowlistFilter) Name() string              { return "domain_allowlist" }
func (f *domainAllowlistFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }

func (f *domainAllowlistFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	// A local forward runs inside its delivery transaction. Keep filter reads
	// and writes on that connection without changing the registered instance.
	if db := pipeline.DBFromContext(ctx); db != nil {
		copy := *f
		copy.db = db
		f = &copy
	}
	sender := email.Envelope.MailFrom
	if sender == "" && email.Headers.From.Len() > 0 {
		sender = email.Headers.From.FirstAddress()
	}

	if sender == "" {
		return continueResult("domain_allowlist", "no sender"), nil
	}

	senderDomain := ""
	senderDomainBare := ""
	if parts := strings.SplitN(sender, "@", 2); len(parts) == 2 {
		senderDomain = "@" + parts[1]
		senderDomainBare = parts[1]
	}

	// The recipients' domains, by name and ID. A recipient whose domain is not
	// hosted here has no domain rules, but installation rules still apply to it.
	type rcptDomain struct {
		name string
		id   uint
	}
	var domains []rcptDomain
	for _, rcpt := range email.Envelope.RcptTo {
		parts := strings.SplitN(rcpt, "@", 2)
		if len(parts) != 2 {
			continue
		}
		var domainID uint
		if err := f.db.Table("domains").Where("name = ?", parts[1]).Pluck("id", &domainID).Error; err != nil {
			continue
		}
		domains = append(domains, rcptDomain{name: parts[1], id: domainID})
	}

	// Precedence (#1576): a block wins over an allow, whatever the scope of
	// either. Every block is checked before any allow: the installation's
	// blocks, then each recipient domain's. So an installation block cannot
	// be lifted by a domain's allow, a domain can add blocks of its own, and
	// one recipient's allow cannot carry the message past another
	// recipient's block.
	blocked := func(detail string) *pipeline.FilterResult {
		return &pipeline.FilterResult{
			Type:      pipeline.FilterTypeAction,
			Action:    pipeline.ActionReject,
			RejectMsg: f.cfg.RejectMessage,
			Log: pipeline.FilterLog{
				Filter: "domain_allowlist",
				Result: "blocked",
				Detail: detail,
			},
		}
	}
	if f.installationMatch(sender, senderDomain, "block") {
		return blocked(fmt.Sprintf("sender %s blocked for the installation", sender)), nil
	}
	for _, d := range domains {
		if f.domainMatch(d.id, sender, senderDomain, "block") {
			return blocked(fmt.Sprintf("sender %s blocked for domain %s", sender, d.name)), nil
		}
	}

	allowedFor := ""
	for _, d := range domains {
		if f.domainMatch(d.id, sender, senderDomain, "allow") {
			allowedFor = "domain " + d.name
			break
		}
	}
	if allowedFor == "" && f.installationMatch(sender, senderDomain, "allow") {
		allowedFor = "the installation"
	}
	if allowedFor != "" {
		// A match may skip spam/greylist scanning ONLY when the sender's
		// identity is actually authenticated and aligned to its domain (SPF or
		// DKIM). Otherwise a spoofer who forges an allowlisted envelope-from
		// would reuse the entry to bypass scanning entirely (#177). An
		// unauthenticated match still passes the allowlist (no reject) but is
		// scanned like any other mail.
		skip := []string{"rspamd", "spamassassin", "greylist"}
		detail := fmt.Sprintf("sender %s allowed for %s", sender, allowedFor)
		if !senderAuthenticated(ctx, senderDomainBare) {
			skip = nil
			detail += " (unauthenticated sender: scanning not skipped)"
		}
		return &pipeline.FilterResult{
			Type:        pipeline.FilterTypeAction,
			Action:      pipeline.ActionContinue,
			SkipFilters: skip,
			Log: pipeline.FilterLog{
				Filter: "domain_allowlist",
				Result: "allowed",
				Detail: detail,
			},
		}, nil
	}

	return continueResult("domain_allowlist", "no matching rules"), nil
}

// domainMatch reports whether the domain has a rule of listType for the sender's
// address or its @domain.
func (f *domainAllowlistFilter) domainMatch(domainID uint, sender, senderDomain, listType string) bool {
	var n int64
	f.db.Table("domain_sender_rules").
		Where("domain_id = ? AND list_type = ? AND (pattern = ? OR pattern = ?)", domainID, listType, sender, senderDomain).
		Count(&n)
	return n > 0
}

// installationMatch reports whether the installation has a rule of listType for the
// sender's address or its @domain.
func (f *domainAllowlistFilter) installationMatch(sender, senderDomain, listType string) bool {
	var n int64
	f.db.Table("installation_sender_rules").
		Where("list_type = ? AND (pattern = ? OR pattern = ?)", listType, sender, senderDomain).
		Count(&n)
	return n > 0
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
