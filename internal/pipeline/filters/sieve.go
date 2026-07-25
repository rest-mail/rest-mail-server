package filters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
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
//
// ScriptHash/MailboxID are populated when the config carries a per-mailbox
// script that was validated at install time (see models.SieveScript): ScriptHash
// is the sha256 hex recorded then, MailboxID identifies the owner for logging.
// Both are optional — a domain-level or legacy config omits them and skips the
// delivery-time trust check (parse-only, the pre-existing behavior).
type sieveConfig struct {
	Script     string `json:"script"`
	ScriptHash string `json:"script_hash,omitempty"`
	MailboxID  uint   `json:"mailbox_id,omitempty"`
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

	// Delivery-time trust (install-time validation counterpart): a per-mailbox
	// script carries the sha256 hash recorded when it passed install validation.
	// If the current bytes no longer match that hash the row drifted out-of-band
	// (edited/corrupted/tampered) and was never re-validated. Fail closed — as an
	// unparseable script already does — but log a clear WARNING naming the mailbox
	// so an operator sees WHY the mailbox's mail is deferring, rather than a silent
	// defer. An empty ScriptHash means "no validated marker" (legacy/domain-level
	// config): skip the check and parse as before.
	if cfg.ScriptHash != "" {
		if actual := HashSieveScript(cfg.Script); actual != cfg.ScriptHash {
			slog.Warn("sieve: stored script no longer matches its validated hash; failing closed",
				"mailbox_id", cfg.MailboxID,
				"expected_hash", cfg.ScriptHash,
				"actual_hash", actual,
			)
			return nil, fmt.Errorf("sieve script hash mismatch (mailbox_id=%d): stored script no longer matches its validated install-time hash", cfg.MailboxID)
		}
	}

	script, err := sieve.Parse(cfg.Script)
	if err != nil {
		// A previously-validated script that no longer parses is also drift worth
		// surfacing (a require was stripped, the parser tightened, …). Fail closed
		// as before, but make it visible for the same reason as a hash mismatch.
		if cfg.ScriptHash != "" {
			slog.Warn("sieve: previously-validated stored script no longer parses; failing closed",
				"mailbox_id", cfg.MailboxID,
				"error", err,
			)
		}
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
	return sieveResult(outcome, exec, scriptRequiresCopy(f.script), &modified), nil
}

// scriptRequiresCopy reports whether the script declared the RFC 3894 "copy"
// extension. go-sieve v0.3.0 does not surface the per-action :copy flag to the
// Executor (Executor.Redirect receives only the address) nor preserve the
// implicit keep for a :copy redirect in its Outcome, so a `redirect :copy` and a
// bare `redirect` are indistinguishable from the callbacks alone. The declared
// "copy" capability is the signal we have: when a redirect-only script requires
// "copy", the redirect is treated as keep-preserving (message forwarded AND kept
// locally). This errs toward keeping the message — never losing mail — in the
// uncommon script that mixes a bare and a :copy redirect. A future go-sieve that
// exposes :copy on Executor.Redirect would let this be decided per redirect.
func scriptRequiresCopy(s *sieve.Script) bool {
	if s == nil {
		return false
	}
	for _, r := range s.Requires() {
		if strings.EqualFold(r, "copy") {
			return true
		}
	}
	return false
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
//   - A `redirect` records its target(s) in redirect_to metadata for the delivery
//     path to forward (RFC 5228 §4.2). A bare redirect cancels the implicit keep,
//     so when the message has no other local destination redirect_suppress_keep is
//     set and the delivery path forwards WITHOUT also keeping a local copy; a
//     `redirect :copy` forwards AND keeps.
//   - A runtime error (outcome.Error — always reported as Continue+ImplicitKeep)
//     fails safe to that implicit keep per §2.10.6: the message is kept.
func sieveResult(outcome sieve.Outcome, exec *metadataExecutor, copyRequired bool, modified *pipeline.EmailJSON) *pipeline.FilterResult {
	var applied []string
	var redirectTargets []string
	hasLocalDelivery := false
	if exec != nil {
		applied = exec.applied
		redirectTargets = exec.redirectTargets
		hasLocalDelivery = exec.hasLocalDelivery
	}

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
	// else INBOX via the implicit keep) and forward any recorded redirect targets.
	//
	// Redirect targets are handed to the delivery path as JSON in redirect_to.
	// The local-keep decision (RFC 5228 §4.2 / RFC 3894): the message is kept
	// locally when the implicit keep still stands, OR an explicit keep / fileinto
	// delivered it locally, OR every redirect was keep-preserving (:copy). Only a
	// bare redirect that was the message's sole destination cancels the local
	// copy — signalled with redirect_suppress_keep so the delivery path forwards
	// without also storing to INBOX.
	if len(redirectTargets) > 0 {
		ensureMetadata(modified)
		modified.Metadata["redirect_to"] = encodeRedirectTargets(redirectTargets)
		localKeep := outcome.ImplicitKeep || hasLocalDelivery || copyRequired
		if !localKeep {
			modified.Metadata["redirect_suppress_keep"] = "true"
		}
	}

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

// encodeRedirectTargets serializes the redirect targets to the JSON array stored
// in the redirect_to metadata the delivery path decodes.
func encodeRedirectTargets(targets []string) string {
	b, err := json.Marshal(targets)
	if err != nil {
		return ""
	}
	return string(b)
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

	// redirectTargets accumulates the addresses a `redirect` action selected that
	// passed the OSI-13 policy AND the loop check, in script order. The delivery
	// path forwards to each (see redirect_to metadata).
	redirectTargets []string
	// hasLocalDelivery records that an explicit keep or a fileinto delivered the
	// message to a local mailbox, so a co-occurring bare redirect does not cancel
	// the local copy.
	hasLocalDelivery bool

	// redirect / localDomains gate the `redirect` action (OSI-13): a redirect to
	// one of localDomains (the recipient's own domain(s)) is always allowed;
	// external targets are governed by the policy (deny by default).
	redirect     SieveRedirectPolicy
	localDomains []string
}

func (e *metadataExecutor) Keep() {
	e.hasLocalDelivery = true
	e.applied = append(e.applied, "keep")
}

func (e *metadataExecutor) FileInto(folder string, create bool) {
	ensureMetadata(e.email)
	e.email.Metadata["deliver_to_folder"] = folder
	if create {
		e.email.Metadata["deliver_to_folder_create"] = "true"
	}
	e.hasLocalDelivery = true
	e.applied = append(e.applied, "fileinto:"+folder)
}

func (e *metadataExecutor) Redirect(addr string) {
	if !e.redirectAllowed(addr) {
		// Deny: do NOT record the target, so the delivery path never forwards the
		// message off-domain. This is the OSI-13 exfiltration guard.
		slog.Warn("sieve: redirect to disallowed external domain denied (OSI-13)",
			"target_domain", domainOf(addr),
			"recipient_domains", strings.Join(e.localDomains, ","),
		)
		e.applied = append(e.applied, "redirect-denied:"+addr)
		return
	}
	// Loop suppression (RFC 5228 §4.2 / RFC 5321 §6.3): never forward to a target
	// the message has already been delivered to (present in its Delivered-To
	// chain) or to one of its own recipients (a self-redirect). Suppressing the
	// target here — before it is recorded — also leaves the message with no
	// redirect destination, so the implicit keep stands and it is kept locally
	// rather than lost when a looping redirect was its only action.
	if e.redirectLoops(addr) {
		slog.Warn("sieve: redirect suppressed to avoid a mail loop", "target", addr)
		e.applied = append(e.applied, "redirect-loop-suppressed:"+addr)
		return
	}
	e.redirectTargets = append(e.redirectTargets, addr)
	e.applied = append(e.applied, "redirect:"+addr)
}

// redirectLoops reports whether forwarding to addr would create a mail loop: the
// message has already been delivered to that address (it appears in the
// Delivered-To header chain), or addr is one of the message's own envelope
// recipients (a self-redirect). RFC 5228 §4.2 directs implementations to
// suppress redirects that would cause a loop. An address with no parseable domain
// is treated as a loop (never forwarded).
func (e *metadataExecutor) redirectLoops(addr string) bool {
	target := normalizeAddr(addr)
	if target == "" {
		return true
	}
	for _, dt := range deliveredToHeaders(e.email.Headers.Raw) {
		if normalizeAddr(dt) == target {
			return true
		}
	}
	for _, r := range e.email.Envelope.RcptTo {
		if normalizeAddr(r) == target {
			return true
		}
	}
	return false
}

// deliveredToHeaders returns every Delivered-To header value present in the raw
// header map (matched case-insensitively).
func deliveredToHeaders(raw map[string][]string) []string {
	var out []string
	for k, v := range raw {
		if strings.EqualFold(k, "Delivered-To") {
			out = append(out, v...)
		}
	}
	return out
}

// normalizeAddr lower-cases an email address and strips surrounding angle
// brackets and whitespace, for loop-detection comparisons.
func normalizeAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	addr = strings.TrimPrefix(addr, "<")
	addr = strings.TrimSuffix(addr, ">")
	return strings.ToLower(strings.TrimSpace(addr))
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

// HashSieveScript returns the lowercase sha256 hex of the exact script bytes.
// It is the canonical hash used both to record the "validated at install" marker
// (models.SieveScript.ScriptHash) and to re-verify it at delivery. See the note
// on models.SieveScript: sha256 is integrity / drift-detection, not
// tamper-evidence — a future HMAC-with-server-key upgrade would make the marker
// tamper-evident.
func HashSieveScript(script string) string {
	sum := sha256.Sum256([]byte(script))
	return hex.EncodeToString(sum[:])
}

// missingRequireRe extracts the capability name from a go-sieve "... requires
// \"X\" to be declared with require" parse error, so an install-time rejection
// can tell the user exactly which extension needs a `require`.
//
// NOTE (PR #249 overlap): cmd/sieve-migrate has a parallel extractor
// (extractMissingCap) it uses for its offline repair scanner. The two are kept
// independent for now — cmd/sieve-migrate is a separate `package main` this
// server package cannot import — and can be unified into a shared helper once
// that tool lands. Both target the same parser error string, so they stay in
// lockstep with the pinned go-sieve version.
var missingRequireRe = regexp.MustCompile(`requires "([^"]+)" to be declared with require`)

// missingRequireCap returns the undeclared extension a go-sieve parse error names,
// or "" when the error is not a missing-require error.
func missingRequireCap(err error) string {
	if err == nil {
		return ""
	}
	if m := missingRequireRe.FindStringSubmatch(err.Error()); len(m) == 2 {
		return m[1]
	}
	return ""
}

// SieveInstallError is a structured install-time validation failure. Stage is
// "parse" (syntax / undeclared extension) or "dryrun" (a runtime error surfaced
// by evaluating the script against a synthetic message). MissingRequire names the
// extension lacking a `require` when the parser could determine it. Error()
// renders an actionable message the API returns verbatim (HTTP 400).
type SieveInstallError struct {
	Stage          string
	MissingRequire string
	Err            error
}

func (e *SieveInstallError) Error() string {
	if e.Stage == "dryrun" {
		return fmt.Sprintf("sieve script failed a safe dry-run: %v", e.Err)
	}
	if e.MissingRequire != "" {
		return fmt.Sprintf("sieve parse error: %v (the script uses an extension it does not declare — add: require \"%s\";)", e.Err, e.MissingRequire)
	}
	return fmt.Sprintf("sieve parse error: %v", e.Err)
}

func (e *SieveInstallError) Unwrap() error { return e.Err }

// ValidateSieveForInstall is the install-time gate: it parses the script and then
// runs it in a safe, side-effect-free dry-run against a synthetic message, so
// both syntax errors AND runtime errors are caught at install (returned to the
// user immediately) instead of at delivery (where a bad script fails closed and
// silently defers the mailbox's mail). It returns a *SieveInstallError on
// failure and nil when the script is safe to store.
//
// An empty script is trivially valid (the filter treats it as "no rules").
func ValidateSieveForInstall(script string) error {
	if strings.TrimSpace(script) == "" {
		return nil
	}
	parsed, err := sieve.Parse(script)
	if err != nil {
		return &SieveInstallError{Stage: "parse", MissingRequire: missingRequireCap(err), Err: err}
	}
	if err := dryRunSieve(parsed); err != nil {
		return &SieveInstallError{Stage: "dryrun", Err: err}
	}
	return nil
}

// dryRunSieve evaluates a parsed script against a synthetic message with a
// recording no-op executor, surfacing runtime failures at install time. It
// performs NO side effects: no delivery, no redirect, no auto-reply. Two things
// count as a dry-run failure: the evaluator panicking (recovered here into an
// error, defensive against a future parser/evaluator regression), and an action
// the recording executor rejects as unrunnable (currently a redirect whose
// target has no parseable domain — which the delivery executor cannot forward and
// suppresses, so surfacing it at install turns a silent drop into immediate
// feedback).
func dryRunSieve(script *sieve.Script) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("sieve evaluation panicked: %v", r)
		}
	}()
	exec := &recordingExecutor{}
	_ = script.Evaluate(dryRunMessage(), exec)
	return exec.err
}

// dryRunMessage is the minimal synthetic RFC 5322 message the install-time
// dry-run evaluates against. It carries enough structure (envelope, common
// headers, a plain-text body) that address/header/envelope/body/size tests all
// have something to evaluate without reaching into real mail.
func dryRunMessage() *sieve.Message {
	return &sieve.Message{
		Headers: sieve.Headers{
			Subject:   "Sieve install validation",
			From:      []sieve.Address{{Name: "Test Sender", Address: "sender@example.com"}},
			To:        []sieve.Address{{Address: "recipient@example.com"}},
			MessageID: "<install-validation@example.com>",
			Date:      "Mon, 02 Jan 2006 15:04:05 -0700",
			Raw: map[string][]string{
				"From":    {"Test Sender <sender@example.com>"},
				"To":      {"recipient@example.com"},
				"Subject": {"Sieve install validation"},
			},
		},
		Envelope: sieve.Envelope{
			From: "sender@example.com",
			To:   []string{"recipient@example.com"},
		},
		Body: sieve.Body{
			ContentType: "text/plain",
			Content:     "This is a synthetic message used to dry-run a Sieve script at install time.",
		},
	}
}

// recordingExecutor implements sieve.Executor for the install-time dry-run: it
// records the actions a script would take and performs NO real side effect. It
// also flags an action that cannot run as a dry-run error via err — currently a
// redirect to an address with no parseable domain, which the delivery-time
// executor (metadataExecutor.Redirect) cannot forward and suppresses; surfacing
// it here turns that silent drop into an install-time rejection.
type recordingExecutor struct {
	actions []string
	err     error
}

func (e *recordingExecutor) Keep() { e.actions = append(e.actions, "keep") }

func (e *recordingExecutor) FileInto(folder string, create bool) {
	e.actions = append(e.actions, "fileinto:"+folder)
}

func (e *recordingExecutor) Redirect(addr string) {
	e.actions = append(e.actions, "redirect:"+addr)
	if domainOf(addr) == "" && e.err == nil {
		e.err = fmt.Errorf("redirect target %q is not a valid email address (no domain)", addr)
	}
}

func (e *recordingExecutor) Flag(op string, flags []string) {
	e.actions = append(e.actions, op+":"+strings.Join(flags, " "))
}

func (e *recordingExecutor) Vacation(v sieve.Vacation) {
	e.actions = append(e.actions, "vacation:"+v.ReplyTo)
	// The delivery path records vacation metadata but no consumer ever sends the
	// reply, so a `vacation` action silently no-ops. Reject it at install rather
	// than accept a script we won't honour (issue #201). Mailbox out-of-office is
	// configured via the vacation_configs table + the `vacation` pipeline filter,
	// not via a Sieve vacation action.
	if e.err == nil {
		e.err = fmt.Errorf("the Sieve 'vacation' action is not supported by this server (configure out-of-office via the mailbox vacation settings instead)")
	}
}

func (e *recordingExecutor) Notify(method, message string) {
	e.actions = append(e.actions, "notify:"+method)
	// `notify` (RFC 5435) has no delivery consumer either. The parser already
	// rejects the enotify/notify extensions, so this is belt-and-suspenders for a
	// future parser that accepts them (issue #201).
	if e.err == nil {
		e.err = fmt.Errorf("the Sieve 'notify' action is not supported by this server")
	}
}
