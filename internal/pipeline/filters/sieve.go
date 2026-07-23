package filters

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/rest-mail/sieve"
	"github.com/restmail/restmail/internal/pipeline"
)

// sieveConfig holds the Sieve script for this filter.
type sieveConfig struct {
	Script string `json:"script"`
}

// sieveFilter runs a Sieve (RFC 5228) script against each message. The parser
// and interpreter live in github.com/rest-mail/sieve; this filter adapts
// pipeline.EmailJSON to the library's neutral Message, implements its Executor
// by recording the selected actions as message metadata (the contract the
// delivery path consumes), and maps the terminal outcome onto pipeline results.
type sieveFilter struct {
	script *sieve.Script
}

func init() {
	pipeline.DefaultRegistry.Register("sieve", NewSieve)
}

func NewSieve(config []byte) (pipeline.Filter, error) {
	var cfg sieveConfig
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
	}
	if cfg.Script == "" {
		return &sieveFilter{}, nil
	}

	script, err := sieve.Parse(cfg.Script)
	if err != nil {
		return nil, fmt.Errorf("parse sieve: %w", err)
	}

	return &sieveFilter{script: script}, nil
}

func (f *sieveFilter) Name() string              { return "sieve" }
func (f *sieveFilter) Type() pipeline.FilterType { return pipeline.FilterTypeTransform }

func (f *sieveFilter) Execute(_ context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	if f.script.Empty() {
		return &pipeline.FilterResult{
			Type:    pipeline.FilterTypeTransform,
			Action:  pipeline.ActionContinue,
			Message: email,
			Log: pipeline.FilterLog{
				Filter: "sieve",
				Result: "pass",
				Detail: "no sieve rules",
			},
		}, nil
	}

	// Work on a copy so the input is not mutated; clone the metadata map so
	// action side-effects do not leak back to the caller's message.
	modified := *email
	if email.Metadata != nil {
		m := make(map[string]string, len(email.Metadata))
		for k, v := range email.Metadata {
			m[k] = v
		}
		modified.Metadata = m
	}

	exec := &metadataExecutor{email: &modified}
	outcome := f.script.Evaluate(sieveMessage(&modified), exec)

	switch outcome.Disposition {
	case sieve.Discard:
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeTransform,
			Action: pipeline.ActionDiscard,
			Log: pipeline.FilterLog{
				Filter: "sieve",
				Result: "discard",
				Detail: "sieve discard action",
			},
		}, nil
	case sieve.Reject:
		return &pipeline.FilterResult{
			Type:      pipeline.FilterTypeTransform,
			Action:    pipeline.ActionReject,
			RejectMsg: "550 " + outcome.RejectReason,
			Log: pipeline.FilterLog{
				Filter: "sieve",
				Result: "reject",
				Detail: "sieve reject: " + outcome.RejectReason,
			},
		}, nil
	}

	detail := "no rules matched"
	if len(exec.applied) > 0 {
		detail = "applied: " + strings.Join(exec.applied, ", ")
	}

	return &pipeline.FilterResult{
		Type:    pipeline.FilterTypeTransform,
		Action:  pipeline.ActionContinue,
		Message: &modified,
		Log: pipeline.FilterLog{
			Filter: "sieve",
			Result: "transformed",
			Detail: detail,
		},
	}, nil
}

// ── Message adaptation ───────────────────────────────────────────────

// sieveMessage adapts a pipeline message to the sieve library's neutral view,
// resolving any gateway-supplied envelope override from metadata so the
// envelope test sees the effective identities.
func sieveMessage(email *pipeline.EmailJSON) *sieve.Message {
	msg := &sieve.Message{
		Headers: sieve.Headers{
			Subject:    email.Headers.Subject,
			From:       sieveAddresses(email.Headers.From),
			To:         sieveAddresses(email.Headers.To),
			Cc:         sieveAddresses(email.Headers.Cc),
			Bcc:        sieveAddresses(email.Headers.Bcc),
			MessageID:  email.Headers.MessageID,
			InReplyTo:  email.Headers.InReplyTo,
			Date:       email.Headers.Date,
			References: email.Headers.References,
			Raw:        email.Headers.Raw,
		},
		Envelope: sieve.Envelope{
			From: email.Envelope.MailFrom,
			To:   email.Envelope.RcptTo,
		},
		Body: sieveBody(email.Body),
	}
	if email.Metadata != nil {
		if v := email.Metadata["envelope_from"]; v != "" {
			msg.Envelope.From = v
		}
		if v := email.Metadata["envelope_to"]; v != "" {
			msg.Envelope.To = []string{v}
		}
	}
	for _, a := range email.Attachments {
		msg.Attachments = append(msg.Attachments, sieve.Attachment{Size: a.Size})
	}
	return msg
}

func sieveAddresses(addrs []pipeline.Address) []sieve.Address {
	if len(addrs) == 0 {
		return nil
	}
	out := make([]sieve.Address, len(addrs))
	for i, a := range addrs {
		out[i] = sieve.Address{Name: a.Name, Address: a.Address}
	}
	return out
}

func sieveBody(b pipeline.Body) sieve.Body {
	out := sieve.Body{ContentType: b.ContentType, Content: b.Content}
	for _, p := range b.Parts {
		out.Parts = append(out.Parts, sieveBody(p))
	}
	return out
}

// ── Action execution ─────────────────────────────────────────────────

// metadataExecutor implements sieve.Executor by recording the selected actions
// as message metadata, which the delivery path consumes.
type metadataExecutor struct {
	email   *pipeline.EmailJSON
	applied []string
}

func (e *metadataExecutor) Keep() { e.applied = append(e.applied, "keep") }

func (e *metadataExecutor) FileInto(folder string, create bool) {
	ensureMetadata(e.email)
	e.email.Metadata["deliver_to_folder"] = folder
	if create {
		e.email.Metadata["deliver_to_folder_create"] = "true"
	}
	e.applied = append(e.applied, "fileinto:"+folder)
}

func (e *metadataExecutor) Redirect(addr string) {
	ensureMetadata(e.email)
	e.email.Metadata["redirect_to"] = addr
	e.applied = append(e.applied, "redirect:"+addr)
}

func (e *metadataExecutor) Flag(op string, flags []string) {
	applyFlags(e.email, op, flags)
	e.applied = append(e.applied, op+":"+strings.Join(flags, " "))
}

func (e *metadataExecutor) Vacation(v sieve.Vacation) {
	ensureMetadata(e.email)

	// Determine the sender to reply to from the raw envelope (not the resolved
	// override the library computed), preserving the previous behaviour.
	replyTo := e.email.Envelope.MailFrom
	if replyTo == "" && len(e.email.Headers.From) > 0 {
		replyTo = e.email.Headers.From[0].Address
	}

	// Dedup key: hash of the sender address. The downstream vacation filter or
	// delivery agent enforces the actual time window.
	dedupKey := vacationDedupKey(replyTo)
	lastSentKey := "vacation_last_sent_" + dedupKey
	if _, alreadySent := e.email.Metadata[lastSentKey]; alreadySent {
		e.applied = append(e.applied, "vacation:dedup-suppressed")
		return
	}

	e.email.Metadata["vacation_reply_to"] = replyTo
	e.email.Metadata["vacation_reply_subject"] = v.Subject
	e.email.Metadata["vacation_reply_body"] = v.Body
	if v.Days > 0 {
		e.email.Metadata["vacation_days"] = strconv.Itoa(v.Days)
	}
	e.email.Metadata[lastSentKey] = "pending"
	e.applied = append(e.applied, "vacation:"+replyTo)
}

func (e *metadataExecutor) Notify(method, message string) {
	ensureMetadata(e.email)
	e.email.Metadata["notify_method"] = method
	e.email.Metadata["notify_message"] = message
	e.applied = append(e.applied, "notify:"+method)
}

func ensureMetadata(email *pipeline.EmailJSON) {
	if email.Metadata == nil {
		email.Metadata = make(map[string]string)
	}
}

// applyFlags updates the imap4flags flag set stored in metadata.
func applyFlags(email *pipeline.EmailJSON, op string, flags []string) {
	ensureMetadata(email)
	var current []string
	if existing := strings.TrimSpace(email.Metadata["imap_flags"]); existing != "" {
		current = strings.Fields(existing)
	}

	switch op {
	case "setflag":
		current = uniqueStrings(flags)
	case "addflag":
		current = uniqueStrings(append(current, flags...))
	case "removeflag":
		remove := make(map[string]struct{}, len(flags))
		for _, fl := range flags {
			remove[fl] = struct{}{}
		}
		kept := current[:0]
		for _, fl := range current {
			if _, drop := remove[fl]; !drop {
				kept = append(kept, fl)
			}
		}
		current = kept
	}

	email.Metadata["imap_flags"] = strings.Join(current, " ")
}

func uniqueStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// vacationDedupKey returns a short hash for deduplication keyed on the sender address.
func vacationDedupKey(sender string) string {
	h := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(sender))))
	return fmt.Sprintf("%x", h[:8])
}

// ── Validation ───────────────────────────────────────────────────────

// ValidateSieve checks if a Sieve script is syntactically valid.
func ValidateSieve(script string) error {
	return sieve.Validate(script)
}
