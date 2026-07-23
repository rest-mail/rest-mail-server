package filters

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/restmail/restmail/internal/pipeline"
)

// sieveConfig holds the Sieve script for this filter.
type sieveConfig struct {
	Script string `json:"script"`
}

// sieveFilter implements a Sieve interpreter for email filtering.
//
// Control commands: if / elsif / else, require, stop.
//
// Tests: address, header, envelope, exists, size (:over/:under), body, allof,
// anyof, not, true/false.
//
// Match types: :is, :contains, :matches (glob with * and ?), and a
// non-standard :regex extension. Comparators i;ascii-casemap (default),
// i;octet and i;ascii-numeric. Address parts :all/:localpart/:domain.
//
// Actions: keep, discard, fileinto (+ :create), redirect, reject, stop,
// setflag/addflag/removeflag (imap4flags), vacation, notify.
//
// The parser and AST live in sieve_parser.go; this file evaluates the AST
// against an email and records the resulting actions as message metadata.
type sieveFilter struct {
	script *sieveScript
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

	script, err := parseSieveScript(cfg.Script)
	if err != nil {
		return nil, fmt.Errorf("parse sieve: %w", err)
	}

	return &sieveFilter{script: script}, nil
}

func (f *sieveFilter) Name() string              { return "sieve" }
func (f *sieveFilter) Type() pipeline.FilterType { return pipeline.FilterTypeTransform }

func (f *sieveFilter) Execute(_ context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	if f.script == nil || len(f.script.commands) == 0 {
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

	ev := &sieveEval{email: &modified}
	outcome := f.runBlock(f.script.commands, ev)
	if outcome.terminal != nil {
		return outcome.terminal, nil
	}

	detail := "no rules matched"
	if len(ev.applied) > 0 {
		detail = "applied: " + strings.Join(ev.applied, ", ")
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

// sieveEval carries mutable state while walking a script's commands.
type sieveEval struct {
	email   *pipeline.EmailJSON
	applied []string
}

// execOutcome reports how a block finished: a non-nil terminal short-circuits
// the whole pipeline (discard/reject), while stop halts further Sieve commands
// but still delivers according to the actions taken so far.
type execOutcome struct {
	terminal *pipeline.FilterResult
	stop     bool
}

func (f *sieveFilter) runBlock(cmds []sieveCmd, ev *sieveEval) execOutcome {
	for _, c := range cmds {
		out := f.runCmd(c, ev)
		if out.terminal != nil || out.stop {
			return out
		}
	}
	return execOutcome{}
}

func (f *sieveFilter) runCmd(c sieveCmd, ev *sieveEval) execOutcome {
	switch cmd := c.(type) {
	case *ifCmd:
		for _, br := range cmd.branches {
			if br.test == nil || evalTest(br.test, ev.email) {
				return f.runBlock(br.block, ev)
			}
		}
		return execOutcome{}

	case *stopCmd:
		return execOutcome{stop: true}

	case *keepCmd:
		ev.applied = append(ev.applied, "keep")

	case *discardCmd:
		return execOutcome{terminal: &pipeline.FilterResult{
			Type:   pipeline.FilterTypeTransform,
			Action: pipeline.ActionDiscard,
			Log: pipeline.FilterLog{
				Filter: "sieve",
				Result: "discard",
				Detail: "sieve discard action",
			},
		}}

	case *rejectCmd:
		return execOutcome{terminal: &pipeline.FilterResult{
			Type:      pipeline.FilterTypeTransform,
			Action:    pipeline.ActionReject,
			RejectMsg: "550 " + cmd.reason,
			Log: pipeline.FilterLog{
				Filter: "sieve",
				Result: "reject",
				Detail: "sieve reject: " + cmd.reason,
			},
		}}

	case *fileintoCmd:
		ensureMetadata(ev.email)
		ev.email.Metadata["deliver_to_folder"] = cmd.folder
		if cmd.create {
			ev.email.Metadata["deliver_to_folder_create"] = "true"
		}
		ev.applied = append(ev.applied, "fileinto:"+cmd.folder)

	case *redirectCmd:
		ensureMetadata(ev.email)
		ev.email.Metadata["redirect_to"] = cmd.addr
		ev.applied = append(ev.applied, "redirect:"+cmd.addr)

	case *flagCmd:
		applyFlags(ev.email, cmd.op, cmd.flags)
		ev.applied = append(ev.applied, cmd.op+":"+strings.Join(cmd.flags, " "))

	case *vacationCmd:
		f.applyVacation(cmd, ev)

	case *notifyCmd:
		ensureMetadata(ev.email)
		ev.email.Metadata["notify_method"] = cmd.method
		ev.email.Metadata["notify_message"] = cmd.message
		ev.applied = append(ev.applied, "notify:"+cmd.method)
	}

	return execOutcome{}
}

func (f *sieveFilter) applyVacation(cmd *vacationCmd, ev *sieveEval) {
	ensureMetadata(ev.email)

	// Determine the sender to reply to.
	replyTo := ev.email.Envelope.MailFrom
	if replyTo == "" && len(ev.email.Headers.From) > 0 {
		replyTo = ev.email.Headers.From[0].Address
	}

	// Dedup key: hash of the sender address. The downstream vacation filter or
	// delivery agent enforces the actual time window.
	dedupKey := vacationDedupKey(replyTo)
	lastSentKey := "vacation_last_sent_" + dedupKey
	if _, alreadySent := ev.email.Metadata[lastSentKey]; alreadySent {
		ev.applied = append(ev.applied, "vacation:dedup-suppressed")
		return
	}

	ev.email.Metadata["vacation_reply_to"] = replyTo
	ev.email.Metadata["vacation_reply_subject"] = cmd.subject
	ev.email.Metadata["vacation_reply_body"] = cmd.body
	if cmd.days > 0 {
		ev.email.Metadata["vacation_days"] = strconv.Itoa(cmd.days)
	}
	ev.email.Metadata[lastSentKey] = "pending"
	ev.applied = append(ev.applied, "vacation:"+replyTo)
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

// ── Test evaluation ──────────────────────────────────────────────────

func evalTest(t sieveTest, email *pipeline.EmailJSON) bool {
	switch tt := t.(type) {
	case *allofTest:
		for _, sub := range tt.tests {
			if !evalTest(sub, email) {
				return false
			}
		}
		return true
	case *anyofTest:
		for _, sub := range tt.tests {
			if evalTest(sub, email) {
				return true
			}
		}
		return false
	case *notTest:
		return !evalTest(tt.inner, email)
	case *boolTest:
		return tt.val
	case *existsTest:
		for _, h := range tt.headers {
			if len(headerValues(email, h)) == 0 {
				return false
			}
		}
		return true
	case *sizeTest:
		size := messageSize(email)
		if tt.over {
			return size > tt.limit
		}
		return size < tt.limit
	case *headerTest:
		values := gatherHeaderValues(email, tt.headers)
		return matchAny(values, tt.matchType, tt.comparator, tt.keys)
	case *addressTest:
		values := gatherAddressValues(email, tt.headers, tt.addressPart)
		return matchAny(values, tt.matchType, tt.comparator, tt.keys)
	case *envelopeTest:
		values := gatherEnvelopeValues(email, tt.parts, tt.addressPart)
		return matchAny(values, tt.matchType, tt.comparator, tt.keys)
	case *bodyTest:
		return matchAny([]string{extractBodyText(email)}, tt.matchType, tt.comparator, tt.keys)
	}
	return false
}

// messageSize approximates the octet size of the message: body content, nested
// part content, and known attachment sizes.
func messageSize(email *pipeline.EmailJSON) int64 {
	var size int64
	size += bodySize(email.Body)
	for _, a := range email.Attachments {
		size += a.Size
	}
	return size
}

func bodySize(b pipeline.Body) int64 {
	size := int64(len(b.Content))
	for _, p := range b.Parts {
		size += bodySize(p)
	}
	return size
}

// ── Value extraction ─────────────────────────────────────────────────

func gatherHeaderValues(email *pipeline.EmailJSON, names []string) []string {
	var out []string
	for _, n := range names {
		out = append(out, headerValues(email, n)...)
	}
	return out
}

// headerValues returns every value of the named header, drawing from both the
// structured Headers fields and the raw header map (matched case-insensitively).
func headerValues(email *pipeline.EmailJSON, name string) []string {
	lower := strings.ToLower(strings.TrimSpace(name))
	var out []string
	switch lower {
	case "subject":
		if email.Headers.Subject != "" {
			out = append(out, email.Headers.Subject)
		}
	case "from":
		out = append(out, formatAddresses(email.Headers.From)...)
	case "to":
		out = append(out, formatAddresses(email.Headers.To)...)
	case "cc":
		out = append(out, formatAddresses(email.Headers.Cc)...)
	case "bcc":
		out = append(out, formatAddresses(email.Headers.Bcc)...)
	case "message-id":
		if email.Headers.MessageID != "" {
			out = append(out, email.Headers.MessageID)
		}
	case "in-reply-to":
		if email.Headers.InReplyTo != "" {
			out = append(out, email.Headers.InReplyTo)
		}
	case "date":
		if email.Headers.Date != "" {
			out = append(out, email.Headers.Date)
		}
	case "references":
		out = append(out, email.Headers.References...)
	}
	for k, vals := range email.Headers.Raw {
		if strings.EqualFold(k, lower) {
			out = append(out, vals...)
		}
	}
	return out
}

// formatAddresses renders structured addresses as header-style values.
func formatAddresses(addrs []pipeline.Address) []string {
	var out []string
	for _, a := range addrs {
		switch {
		case a.Name != "" && a.Address != "":
			out = append(out, a.Name+" <"+a.Address+">")
		case a.Address != "":
			out = append(out, a.Address)
		case a.Name != "":
			out = append(out, a.Name)
		}
	}
	return out
}

func gatherAddressValues(email *pipeline.EmailJSON, names []string, part string) []string {
	var out []string
	for _, n := range names {
		for _, addr := range addressList(email, n) {
			out = append(out, addressPartOf(addr, part))
		}
	}
	return out
}

// addressList returns the bare addr-specs of a header (no display name).
func addressList(email *pipeline.EmailJSON, name string) []string {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch lower {
	case "from":
		return addressSpecs(email.Headers.From)
	case "to":
		return addressSpecs(email.Headers.To)
	case "cc":
		return addressSpecs(email.Headers.Cc)
	case "bcc":
		return addressSpecs(email.Headers.Bcc)
	}
	var out []string
	for k, vals := range email.Headers.Raw {
		if strings.EqualFold(k, lower) {
			out = append(out, vals...)
		}
	}
	return out
}

func addressSpecs(addrs []pipeline.Address) []string {
	var out []string
	for _, a := range addrs {
		if a.Address != "" {
			out = append(out, a.Address)
		}
	}
	return out
}

func gatherEnvelopeValues(email *pipeline.EmailJSON, parts []string, part string) []string {
	var out []string
	for _, name := range parts {
		for _, v := range envelopeValues(email, name) {
			out = append(out, addressPartOf(v, part))
		}
	}
	return out
}

func envelopeValues(email *pipeline.EmailJSON, name string) []string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "from":
		if email.Metadata != nil {
			if v := email.Metadata["envelope_from"]; v != "" {
				return []string{v}
			}
		}
		if email.Envelope.MailFrom != "" {
			return []string{email.Envelope.MailFrom}
		}
	case "to":
		if email.Metadata != nil {
			if v := email.Metadata["envelope_to"]; v != "" {
				return []string{v}
			}
		}
		return email.Envelope.RcptTo
	}
	return nil
}

// addressPartOf extracts the requested part (:all/:localpart/:domain) from an
// address. For an address without an "@", the whole string is the local part
// and the domain is empty.
func addressPartOf(addr, part string) string {
	switch part {
	case ":localpart":
		if i := strings.LastIndex(addr, "@"); i >= 0 {
			return addr[:i]
		}
		return addr
	case ":domain":
		if i := strings.LastIndex(addr, "@"); i >= 0 {
			return addr[i+1:]
		}
		return ""
	default: // :all
		return addr
	}
}

// ── Matching ─────────────────────────────────────────────────────────

// matchAny reports whether any value matches any key under the given match
// type and comparator.
func matchAny(values []string, matchType, comparator string, keys []string) bool {
	for _, v := range values {
		for _, k := range keys {
			if matchOne(v, k, matchType, comparator) {
				return true
			}
		}
	}
	return false
}

func matchOne(value, key, matchType, comparator string) bool {
	switch matchType {
	case ":is":
		return compareIs(value, key, comparator)
	case ":contains":
		if comparator == "i;octet" {
			return strings.Contains(value, key)
		}
		return strings.Contains(strings.ToLower(value), strings.ToLower(key))
	case ":matches":
		return wildcardMatch(foldForComparator(value, comparator), foldForComparator(key, comparator))
	case ":regex":
		prefix := "(?i)"
		if comparator == "i;octet" {
			prefix = ""
		}
		re, err := regexp.Compile(prefix + key)
		if err != nil {
			return false // skip invalid regex
		}
		return re.MatchString(value)
	default:
		return strings.Contains(strings.ToLower(value), strings.ToLower(key))
	}
}

func compareIs(value, key, comparator string) bool {
	switch comparator {
	case "i;octet":
		return value == key
	case "i;ascii-numeric":
		nv, okv := asciiNumeric(value)
		nk, okk := asciiNumeric(key)
		if !okv || !okk {
			// Non-numbers are all equal to one another and unequal to numbers.
			return !okv && !okk
		}
		return nv == nk
	default: // i;ascii-casemap
		return strings.EqualFold(value, key)
	}
}

// asciiNumeric parses a leading run of digits per the i;ascii-numeric
// comparator (RFC 4790 §9.1). Returns false when the value does not begin with
// a digit.
func asciiNumeric(s string) (uint64, bool) {
	s = strings.TrimSpace(s)
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	n, err := strconv.ParseUint(s[:end], 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func foldForComparator(s, comparator string) string {
	if comparator == "i;octet" {
		return s
	}
	return strings.ToLower(s)
}

// wildcardMatch implements Sieve :matches semantics: '*' matches zero or more
// characters, '?' matches exactly one, and a backslash escapes the next
// character. It compiles the pattern to an anchored regular expression.
func wildcardMatch(value, pattern string) bool {
	var b strings.Builder
	b.WriteString(`\A`)
	runes := []rune(pattern)
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '\\':
			if i+1 < len(runes) {
				b.WriteString(regexp.QuoteMeta(string(runes[i+1])))
				i++
			} else {
				b.WriteString(regexp.QuoteMeta(`\`))
			}
		case '*':
			b.WriteString(`(?s:.*)`)
		case '?':
			b.WriteString(`(?s:.)`)
		default:
			b.WriteString(regexp.QuoteMeta(string(runes[i])))
		}
	}
	b.WriteString(`\z`)
	re, err := regexp.Compile(b.String())
	if err != nil {
		return false
	}
	return re.MatchString(value)
}

// ── Body text extraction ─────────────────────────────────────────────

// extractBodyText returns the plain text content of the email body.
// It prefers text/plain parts; falls back to stripping HTML tags from text/html.
func extractBodyText(email *pipeline.EmailJSON) string {
	// Try top-level body first.
	if email.Body.Content != "" {
		ct := strings.ToLower(email.Body.ContentType)
		if strings.HasPrefix(ct, "text/plain") || ct == "" {
			return email.Body.Content
		}
		if strings.HasPrefix(ct, "text/html") {
			return stripHTMLTags(email.Body.Content)
		}
	}

	// Search parts for text/plain first, then text/html.
	if plain := findPartContent(email.Body.Parts, "text/plain"); plain != "" {
		return plain
	}
	if html := findPartContent(email.Body.Parts, "text/html"); html != "" {
		return stripHTMLTags(html)
	}

	// Fallback: return raw content.
	return email.Body.Content
}

// findPartContent recursively searches body parts for a matching content type
// and returns the first match's content.
func findPartContent(parts []pipeline.Body, contentType string) string {
	for _, p := range parts {
		if strings.HasPrefix(strings.ToLower(p.ContentType), contentType) && p.Content != "" {
			return p.Content
		}
		if found := findPartContent(p.Parts, contentType); found != "" {
			return found
		}
	}
	return ""
}

// stripHTMLTags removes HTML tags from a string for plain-text matching.
// This is a simplified implementation, not a full HTML parser.
func stripHTMLTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
			b.WriteByte(' ') // replace tag with space
		case !inTag:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ── Validation ───────────────────────────────────────────────────────

// ValidateSieve checks if a Sieve script is syntactically valid.
func ValidateSieve(script string) error {
	if _, err := parseSieveScript(script); err != nil {
		return fmt.Errorf("invalid sieve script: %w", err)
	}
	return nil
}
