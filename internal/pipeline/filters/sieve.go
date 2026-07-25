package filters

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/rest-mail/go-sieve"
	"github.com/restmail/restmail/internal/pipeline"
)

// SieveRedirectPolicy governs where a Sieve `redirect` action may send mail
// (OSI-13). A redirect to one of the recipient's own domains is always allowed;
// this policy governs EXTERNAL targets. The zero value denies every external
// redirect — the secure default — so a mailbox owner (or an attacker abusing a
// script) cannot silently exfiltrate mail to an arbitrary domain.
type SieveRedirectPolicy struct {
	// AllowExternal permits redirect to any domain (legacy behavior); the
	// redirect is still logged. Default false.
	AllowExternal bool
	// AllowedDomains are external domains explicitly permitted as redirect
	// targets even when AllowExternal is false (exact, case-insensitive match).
	AllowedDomains []string
}

// defaultSieveRedirectPolicy is the deny-external default bound by the init()
// registration. routes.go re-registers "sieve" via NewSieveWithPolicy with the
// deployment's configured policy, overriding this.
var defaultSieveRedirectPolicy = SieveRedirectPolicy{}

// sieveConfig holds the Sieve script for this filter.
type sieveConfig struct {
	Script string `json:"script"`
}

// sieveFilter runs a Sieve (RFC 5228) script against each message. The parser
// and interpreter live in github.com/rest-mail/go-sieve; this filter adapts
// pipeline.EmailJSON to the library's neutral Message, implements its Executor
// by recording the selected actions as message metadata (the contract the
// delivery path consumes), and maps the terminal outcome onto pipeline results.
type sieveFilter struct {
	script   *sieve.Script
	redirect SieveRedirectPolicy
}

func init() {
	pipeline.DefaultRegistry.Register("sieve", NewSieve)
}

// NewSieve builds a sieve filter with the deny-external default redirect policy
// (OSI-13). Deployments override the policy by re-registering the filter with
// NewSieveWithPolicy.
func NewSieve(config []byte) (pipeline.Filter, error) {
	return newSieveFilter(config, defaultSieveRedirectPolicy)
}

// NewSieveWithPolicy returns a sieve FilterFactory bound to the given redirect
// policy (OSI-13). routes.go registers it with the deployment's configured
// allowlist so the runtime policy overrides the deny-external default.
func NewSieveWithPolicy(policy SieveRedirectPolicy) pipeline.FilterFactory {
	return func(config []byte) (pipeline.Filter, error) {
		return newSieveFilter(config, policy)
	}
}

func newSieveFilter(config []byte, policy SieveRedirectPolicy) (pipeline.Filter, error) {
	var cfg sieveConfig
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
	}
	if cfg.Script == "" {
		return &sieveFilter{redirect: policy}, nil
	}

	script, err := sieve.Parse(cfg.Script)
	if err != nil {
		return nil, fmt.Errorf("parse sieve: %w", err)
	}

	return &sieveFilter{script: script, redirect: policy}, nil
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

	exec := &metadataExecutor{
		email:        &modified,
		redirect:     f.redirect,
		localDomains: recipientDomains(&modified),
	}
	// Evaluate applies the RFC 5228 implicit-keep model itself and returns the
	// terminal Outcome; sieveResult maps that Outcome onto the pipeline result.
	outcome := f.script.Evaluate(sieveMessage(&modified), exec)
	if outcome.Error != nil {
		// RFC 5228 §2.10.6: a runtime error fails safe to an implicit keep — the
		// message is kept, never lost. Log it so the failure stays visible.
		slog.Error("sieve: script evaluation error, keeping message (fail-safe)",
			"error", outcome.Error)
	}
	return sieveResult(outcome, exec.applied, &modified), nil
}

// sieveResult maps a completed sieve evaluation onto a pipeline.FilterResult,
// honouring the RFC 5228 delivery model exposed by the go-sieve Outcome:
//
//   - Reject and Discard are terminal: the message is rejected or dropped (a
//     bare `discard` drops the mail).
//   - Continue delivers the message. When a delivering action ran, fileinto has
//     recorded deliver_to_folder metadata and the message goes to that folder;
//     otherwise the §2.10.2 implicit keep (outcome.ImplicitKeep) delivers to the
//     default mailbox (INBOX), which ActionContinue with no folder override does.
//   - A runtime error (outcome.Error — always reported as Continue+ImplicitKeep)
//     fails safe to that implicit keep per §2.10.6: the message is kept.
func sieveResult(outcome sieve.Outcome, applied []string, modified *pipeline.EmailJSON) *pipeline.FilterResult {
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
		}
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
		}
	}

	// Continue: deliver the message (to the fileinto folder if one was chosen,
	// else INBOX via the implicit keep).
	result := "transformed"
	var detail string
	switch {
	case outcome.Error != nil:
		result = "kept"
		detail = "evaluation error, kept (fail-safe): " + outcome.Error.Error()
	case len(applied) > 0:
		detail = "applied: " + strings.Join(applied, ", ")
	case outcome.ImplicitKeep:
		result = "kept"
		detail = "implicit keep -> INBOX"
	default:
		detail = "no rules matched"
	}

	return &pipeline.FilterResult{
		Type:    pipeline.FilterTypeTransform,
		Action:  pipeline.ActionContinue,
		Message: modified,
		Log: pipeline.FilterLog{
			Filter: "sieve",
			Result: result,
			Detail: detail,
		},
	}
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

	// redirect / localDomains gate the `redirect` action (OSI-13): a redirect to
	// one of localDomains (the recipient's own domain(s)) is always allowed;
	// external targets are governed by the policy (deny by default).
	redirect     SieveRedirectPolicy
	localDomains []string
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
	if !e.redirectAllowed(addr) {
		// Deny: do NOT record redirect_to, so the delivery path never forwards the
		// message off-domain. This is the OSI-13 exfiltration guard.
		slog.Warn("sieve: redirect to disallowed external domain denied (OSI-13)",
			"target_domain", domainOf(addr),
			"recipient_domains", strings.Join(e.localDomains, ","),
		)
		e.applied = append(e.applied, "redirect-denied:"+addr)
		return
	}
	ensureMetadata(e.email)
	e.email.Metadata["redirect_to"] = addr
	e.applied = append(e.applied, "redirect:"+addr)
}

// redirectAllowed reports whether a sieve redirect to addr is permitted under
// the OSI-13 policy: always to one of the recipient's own domains; to any domain
// when AllowExternal is set (logged); otherwise only to an explicitly
// allowlisted external domain. An unparseable target is denied.
func (e *metadataExecutor) redirectAllowed(addr string) bool {
	target := domainOf(addr)
	if target == "" {
		return false
	}
	for _, d := range e.localDomains {
		if d == target {
			return true
		}
	}
	if e.redirect.AllowExternal {
		slog.Warn("sieve: redirect to external domain permitted by policy (SIEVE_REDIRECT_ALLOW_EXTERNAL)",
			"target_domain", target)
		return true
	}
	for _, d := range e.redirect.AllowedDomains {
		if strings.EqualFold(strings.TrimSpace(d), target) {
			return true
		}
	}
	return false
}

// recipientDomains returns the lower-cased domains of the message recipients —
// the mailbox owner's own domain(s) — used to classify a redirect target as
// internal (always allowed). It prefers the SMTP envelope recipients (which the
// inbound delivery path sets to the resolved mailbox address) and falls back to
// the To/Cc header addresses.
func recipientDomains(email *pipeline.EmailJSON) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(addr string) {
		if d := domainOf(addr); d != "" {
			if _, ok := seen[d]; !ok {
				seen[d] = struct{}{}
				out = append(out, d)
			}
		}
	}
	for _, r := range email.Envelope.RcptTo {
		add(r)
	}
	if len(out) == 0 {
		for _, a := range email.Headers.To {
			add(a.Address)
		}
		for _, a := range email.Headers.Cc {
			add(a.Address)
		}
	}
	return out
}

// domainOf returns the lower-cased domain part of an email address, or "" if
// there is none. A trailing root dot (FQDN form) is stripped.
func domainOf(addr string) string {
	addr = strings.TrimSpace(addr)
	if i := strings.LastIndex(addr, "@"); i >= 0 && i < len(addr)-1 {
		return strings.ToLower(strings.TrimSuffix(addr[i+1:], "."))
	}
	return ""
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
	// go-sieve now carries the resend interval as a duration (RFC 5230 :days /
	// RFC 6131 :seconds). The downstream vacation agent keys off whole days, so
	// convert; a sub-day :seconds interval rounds down to 0 and is omitted.
	if days := int(v.Interval / (24 * time.Hour)); days > 0 {
		e.email.Metadata["vacation_days"] = strconv.Itoa(days)
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
