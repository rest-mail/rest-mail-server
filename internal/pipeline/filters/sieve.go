package filters

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/restmail/restmail/internal/pipeline"
)

// sieveConfig holds the Sieve script for this filter.
type sieveConfig struct {
	Script string `json:"script"`
}

// sieveFilter implements a basic Sieve interpreter for email filtering.
// Supports: keep, fileinto, redirect, discard, reject, vacation, notify,
// and conditionals on header, address, size, body, and envelope.
// Match types: :contains, :is, :matches (glob), :regex.
type sieveFilter struct {
	script string
	rules  []sieveRule
}

type sieveRule struct {
	condition sieveCondition
	actions   []sieveAction
	stop      bool
}

type sieveCondition struct {
	test     string // "header", "address", "size", "body", "envelope", "true"
	header   string
	match    string // ":contains", ":is", ":matches", ":regex"
	values   []string
	sizeOp   string // ":over", ":under"
	sizeVal  int64
	negate   bool
}

type sieveAction struct {
	command string // "keep", "fileinto", "redirect", "discard", "reject", "vacation", "notify"
	arg     string // general argument (folder, address, reject reason)

	// vacation-specific fields
	vacationDays    int
	vacationSubject string
	vacationBody    string

	// notify-specific fields
	notifyMethod  string
	notifyMessage string
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

	rules, err := parseSieve(cfg.Script)
	if err != nil {
		return nil, fmt.Errorf("parse sieve: %w", err)
	}

	return &sieveFilter{script: cfg.Script, rules: rules}, nil
}

func (f *sieveFilter) Name() string             { return "sieve" }
func (f *sieveFilter) Type() pipeline.FilterType { return pipeline.FilterTypeTransform }

func (f *sieveFilter) Execute(_ context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	if len(f.rules) == 0 {
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

	modified := *email
	var appliedActions []string

	for _, rule := range f.rules {
		if !evaluateCondition(rule.condition, &modified) {
			continue
		}

		for _, action := range rule.actions {
			switch action.command {
			case "keep":
				appliedActions = append(appliedActions, "keep")
			case "fileinto":
				if modified.Metadata == nil {
					modified.Metadata = make(map[string]string)
				}
				modified.Metadata["deliver_to_folder"] = action.arg
				appliedActions = append(appliedActions, "fileinto:"+action.arg)
			case "redirect":
				if modified.Metadata == nil {
					modified.Metadata = make(map[string]string)
				}
				modified.Metadata["redirect_to"] = action.arg
				appliedActions = append(appliedActions, "redirect:"+action.arg)
			case "discard":
				return &pipeline.FilterResult{
					Type:   pipeline.FilterTypeTransform,
					Action: pipeline.ActionDiscard,
					Log: pipeline.FilterLog{
						Filter: "sieve",
						Result: "discard",
						Detail: "sieve discard action",
					},
				}, nil
			case "reject":
				return &pipeline.FilterResult{
					Type:      pipeline.FilterTypeTransform,
					Action:    pipeline.ActionReject,
					RejectMsg: "550 " + action.arg,
					Log: pipeline.FilterLog{
						Filter: "sieve",
						Result: "reject",
						Detail: "sieve reject: " + action.arg,
					},
				}, nil
			case "vacation":
				if modified.Metadata == nil {
					modified.Metadata = make(map[string]string)
				}
				// Determine the sender to reply to.
				replyTo := modified.Envelope.MailFrom
				if replyTo == "" && len(modified.Headers.From) > 0 {
					replyTo = modified.Headers.From[0].Address
				}

				// Dedup key: hash of the recipient+sender pair.
				dedupKey := vacationDedupKey(replyTo)

				// Check if we already sent a vacation reply recently.
				// The dedup is tracked via metadata; the downstream vacation
				// filter or delivery agent checks the timestamp.
				lastSentKey := "vacation_last_sent_" + dedupKey
				if _, alreadySent := modified.Metadata[lastSentKey]; !alreadySent {
					modified.Metadata["vacation_reply_to"] = replyTo
					modified.Metadata["vacation_reply_subject"] = action.vacationSubject
					modified.Metadata["vacation_reply_body"] = action.vacationBody
					if action.vacationDays > 0 {
						modified.Metadata["vacation_days"] = fmt.Sprintf("%d", action.vacationDays)
					}
					modified.Metadata[lastSentKey] = "pending"
					appliedActions = append(appliedActions, "vacation:"+replyTo)
				} else {
					appliedActions = append(appliedActions, "vacation:dedup-suppressed")
				}
			case "notify":
				if modified.Metadata == nil {
					modified.Metadata = make(map[string]string)
				}
				modified.Metadata["notify_method"] = action.notifyMethod
				modified.Metadata["notify_message"] = action.notifyMessage
				appliedActions = append(appliedActions, "notify:"+action.notifyMethod)
			}
		}

		if rule.stop {
			break
		}
	}

	detail := "no rules matched"
	if len(appliedActions) > 0 {
		detail = "applied: " + strings.Join(appliedActions, ", ")
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

// vacationDedupKey returns a short hash for deduplication keyed on the sender address.
func vacationDedupKey(sender string) string {
	h := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(sender))))
	return fmt.Sprintf("%x", h[:8])
}

func evaluateCondition(cond sieveCondition, email *pipeline.EmailJSON) bool {
	result := false

	switch cond.test {
	case "true", "":
		result = true
	case "header":
		headerVal := getHeaderValue(email, cond.header)
		result = matchString(headerVal, cond.match, cond.values)
	case "address":
		addrVal := getAddressValue(email, cond.header)
		result = matchString(addrVal, cond.match, cond.values)
	case "size":
		// Simplified: use body length as proxy
		size := int64(len(email.Body.Content))
		for _, p := range email.Body.Parts {
			size += int64(len(p.Content))
		}
		switch cond.sizeOp {
		case ":over":
			result = size > cond.sizeVal
		case ":under":
			result = size < cond.sizeVal
		}
	case "body":
		bodyText := extractBodyText(email)
		result = matchString(bodyText, cond.match, cond.values)
	case "envelope":
		envVal := getEnvelopeValue(email, cond.header)
		result = matchString(envVal, cond.match, cond.values)
	}

	if cond.negate {
		return !result
	}
	return result
}

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

// getEnvelopeValue returns the envelope sender or recipient from metadata or
// the Envelope struct fields.
func getEnvelopeValue(email *pipeline.EmailJSON, field string) string {
	switch strings.ToLower(field) {
	case "from":
		// Prefer metadata if set by the SMTP gateway.
		if email.Metadata != nil {
			if v, ok := email.Metadata["envelope_from"]; ok && v != "" {
				return v
			}
		}
		return email.Envelope.MailFrom
	case "to":
		// Prefer metadata if set by the SMTP gateway.
		if email.Metadata != nil {
			if v, ok := email.Metadata["envelope_to"]; ok && v != "" {
				return v
			}
		}
		if len(email.Envelope.RcptTo) > 0 {
			return email.Envelope.RcptTo[0]
		}
	}
	return ""
}

func getHeaderValue(email *pipeline.EmailJSON, header string) string {
	switch strings.ToLower(header) {
	case "subject":
		return email.Headers.Subject
	case "from":
		if len(email.Headers.From) > 0 {
			return email.Headers.From[0].Address
		}
	case "to":
		if len(email.Headers.To) > 0 {
			return email.Headers.To[0].Address
		}
	default:
		if email.Headers.Raw != nil {
			if vals, ok := email.Headers.Raw[header]; ok && len(vals) > 0 {
				return vals[0]
			}
		}
	}
	return ""
}

func getAddressValue(email *pipeline.EmailJSON, header string) string {
	switch strings.ToLower(header) {
	case "from":
		if len(email.Headers.From) > 0 {
			return email.Headers.From[0].Address
		}
	case "to":
		if len(email.Headers.To) > 0 {
			return email.Headers.To[0].Address
		}
	}
	return ""
}

func matchString(value, matchType string, patterns []string) bool {
	valueLower := strings.ToLower(value)
	for _, pattern := range patterns {
		switch matchType {
		case ":is":
			if valueLower == strings.ToLower(pattern) {
				return true
			}
		case ":contains":
			if strings.Contains(valueLower, strings.ToLower(pattern)) {
				return true
			}
		case ":matches":
			// Simple glob matching
			if globMatch(valueLower, strings.ToLower(pattern)) {
				return true
			}
		case ":regex":
			// Regex matching (case-insensitive via (?i) prefix).
			re, err := regexp.Compile("(?i)" + pattern)
			if err != nil {
				continue // skip invalid regex
			}
			if re.MatchString(value) {
				return true
			}
		default:
			// Default to contains
			if strings.Contains(valueLower, strings.ToLower(pattern)) {
				return true
			}
		}
	}
	return false
}

func globMatch(value, pattern string) bool {
	// Simple glob: * matches any sequence of characters
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return value == pattern
	}

	// Check prefix
	if !strings.HasPrefix(value, parts[0]) {
		return false
	}
	value = value[len(parts[0]):]

	// Check middle parts
	for i := 1; i < len(parts)-1; i++ {
		idx := strings.Index(value, parts[i])
		if idx < 0 {
			return false
		}
		value = value[idx+len(parts[i]):]
	}

	// Check suffix
	return strings.HasSuffix(value, parts[len(parts)-1])
}

// parseSieve is a simplified Sieve parser that handles common patterns.
func parseSieve(script string) ([]sieveRule, error) {
	var rules []sieveRule

	lines := strings.Split(script, "\n")
	i := 0
	for i < len(lines) {
		line := strings.TrimSpace(lines[i])
		i++

		// Skip comments and blank lines
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "require") {
			continue
		}

		// Parse "if" statements
		if strings.HasPrefix(line, "if ") {
			rule, newI, err := parseSieveIf(lines, i-1)
			if err != nil {
				return nil, err
			}
			rules = append(rules, rule)
			i = newI
			continue
		}

		// Top-level actions (including multi-line vacation/notify)
		action, consumed, err := parseSieveActionMultiLine(lines, i-1)
		if err == nil {
			rules = append(rules, sieveRule{
				condition: sieveCondition{test: "true"},
				actions:   []sieveAction{action},
			})
			i = consumed
		}
	}

	return rules, nil
}

func parseSieveIf(lines []string, startLine int) (sieveRule, int, error) {
	rule := sieveRule{}
	line := strings.TrimSpace(lines[startLine])

	// Parse condition from "if <condition> {"
	condStr := strings.TrimPrefix(line, "if ")
	condStr = strings.TrimSuffix(condStr, "{")
	condStr = strings.TrimSpace(condStr)

	rule.condition = parseSieveCondition(condStr)

	// Parse actions inside the block
	i := startLine + 1
	for i < len(lines) {
		actionLine := strings.TrimSpace(lines[i])

		if actionLine == "}" {
			i++
			break
		}

		if strings.TrimSpace(actionLine) == "stop;" {
			rule.stop = true
			i++
			continue
		}

		action, consumed, err := parseSieveActionMultiLine(lines, i)
		if err == nil {
			rule.actions = append(rule.actions, action)
			i = consumed
		} else {
			i++
		}
	}

	return rule, i, nil
}

func parseSieveCondition(cond string) sieveCondition {
	cond = strings.TrimSpace(cond)

	if strings.HasPrefix(cond, "header ") {
		return parseHeaderCondition(cond)
	}
	if strings.HasPrefix(cond, "address ") {
		return parseAddressCondition(cond)
	}
	if strings.HasPrefix(cond, "size ") {
		return parseSizeCondition(cond)
	}
	if strings.HasPrefix(cond, "body ") {
		return parseBodyCondition(cond)
	}
	if strings.HasPrefix(cond, "envelope ") {
		return parseEnvelopeCondition(cond)
	}
	if strings.HasPrefix(cond, "not ") {
		inner := parseSieveCondition(strings.TrimPrefix(cond, "not "))
		inner.negate = true
		return inner
	}

	return sieveCondition{test: "true"}
}

// parseMatchType extracts the match comparator from a condition string.
// Supports :contains, :is, :matches, and :regex.
func parseMatchType(cond string) string {
	if strings.Contains(cond, ":regex") {
		return ":regex"
	}
	if strings.Contains(cond, ":contains") {
		return ":contains"
	}
	if strings.Contains(cond, ":is") {
		return ":is"
	}
	if strings.Contains(cond, ":matches") {
		return ":matches"
	}
	return ":contains" // default
}

func parseHeaderCondition(cond string) sieveCondition {
	sc := sieveCondition{test: "header"}
	sc.match = parseMatchType(cond)

	// Extract header name and values (simplified parsing)
	// Format: header :contains "HeaderName" ["val1", "val2"]
	parts := extractQuotedStrings(cond)
	if len(parts) > 0 {
		sc.header = parts[0]
	}
	if len(parts) > 1 {
		sc.values = parts[1:]
	}

	return sc
}

func parseAddressCondition(cond string) sieveCondition {
	sc := sieveCondition{test: "address"}
	sc.match = parseMatchType(cond)

	parts := extractQuotedStrings(cond)
	if len(parts) > 0 {
		sc.header = parts[0]
	}
	if len(parts) > 1 {
		sc.values = parts[1:]
	}

	return sc
}

// parseBodyCondition parses: body :contains "text"
func parseBodyCondition(cond string) sieveCondition {
	sc := sieveCondition{test: "body"}
	sc.match = parseMatchType(cond)

	parts := extractQuotedStrings(cond)
	if len(parts) > 0 {
		sc.values = parts
	}

	return sc
}

// parseEnvelopeCondition parses: envelope :is "from" "sender@example.com"
func parseEnvelopeCondition(cond string) sieveCondition {
	sc := sieveCondition{test: "envelope"}
	sc.match = parseMatchType(cond)

	parts := extractQuotedStrings(cond)
	if len(parts) > 0 {
		sc.header = parts[0] // "from" or "to"
	}
	if len(parts) > 1 {
		sc.values = parts[1:]
	}

	return sc
}

func parseSizeCondition(cond string) sieveCondition {
	sc := sieveCondition{test: "size"}

	if strings.Contains(cond, ":over") {
		sc.sizeOp = ":over"
	} else if strings.Contains(cond, ":under") {
		sc.sizeOp = ":under"
	}

	// Parse size value (simplified: look for number with optional suffix)
	for _, word := range strings.Fields(cond) {
		var val int64
		if n, err := fmt.Sscanf(word, "%dM", &val); err == nil && n == 1 {
			sc.sizeVal = val * 1024 * 1024
		} else if n, err := fmt.Sscanf(word, "%dK", &val); err == nil && n == 1 {
			sc.sizeVal = val * 1024
		} else if n, err := fmt.Sscanf(word, "%d", &val); err == nil && n == 1 {
			sc.sizeVal = val
		}
	}

	return sc
}

// parseSieveActionMultiLine parses a Sieve action starting at lines[startLine].
// It handles multi-line actions like vacation and notify that span multiple lines.
// Returns the parsed action, the next line index to process, and any error.
func parseSieveActionMultiLine(lines []string, startLine int) (sieveAction, int, error) {
	line := strings.TrimSpace(lines[startLine])

	// Check for vacation action (may span multiple lines).
	if strings.HasPrefix(line, "vacation") {
		return parseVacationAction(lines, startLine)
	}

	// Check for notify action (may span multiple lines).
	if strings.HasPrefix(line, "notify") {
		return parseNotifyAction(lines, startLine)
	}

	// Fall back to single-line action parser.
	action, err := parseSieveAction(line)
	return action, startLine + 1, err
}

func parseSieveAction(line string) (sieveAction, error) {
	line = strings.TrimSuffix(strings.TrimSpace(line), ";")

	if line == "keep" {
		return sieveAction{command: "keep"}, nil
	}
	if line == "discard" {
		return sieveAction{command: "discard"}, nil
	}
	if strings.HasPrefix(line, "fileinto ") {
		arg := extractFirstQuoted(strings.TrimPrefix(line, "fileinto "))
		return sieveAction{command: "fileinto", arg: arg}, nil
	}
	if strings.HasPrefix(line, "redirect ") {
		arg := extractFirstQuoted(strings.TrimPrefix(line, "redirect "))
		return sieveAction{command: "redirect", arg: arg}, nil
	}
	if strings.HasPrefix(line, "reject ") {
		arg := extractFirstQuoted(strings.TrimPrefix(line, "reject "))
		return sieveAction{command: "reject", arg: arg}, nil
	}

	return sieveAction{}, fmt.Errorf("unknown action: %s", line)
}

// parseVacationAction parses a vacation action which may span multiple lines.
// Format: vacation :days 7 :subject "Out of Office" "I am on vacation.";
// The message body is the final quoted string (or the last quoted string
// after :subject). The action may be on a single line or spread across lines
// ending with a semicolon.
func parseVacationAction(lines []string, startLine int) (sieveAction, int, error) {
	// Collect the full statement up to the semicolon.
	full, endLine := collectStatement(lines, startLine)

	action := sieveAction{
		command:      "vacation",
		vacationDays: 7, // default per RFC 5230
	}

	// Parse :days N
	if idx := strings.Index(full, ":days"); idx >= 0 {
		rest := full[idx+len(":days"):]
		rest = strings.TrimSpace(rest)
		var days int
		if _, err := fmt.Sscanf(rest, "%d", &days); err == nil && days > 0 {
			action.vacationDays = days
		}
	}

	// Extract all quoted strings.
	quoted := extractQuotedStrings(full)

	// Parse :subject - the quoted string immediately after :subject is the subject.
	if idx := strings.Index(full, ":subject"); idx >= 0 {
		// Find which quoted string follows :subject.
		afterSubject := full[idx+len(":subject"):]
		subjectQuoted := extractQuotedStrings(afterSubject)
		if len(subjectQuoted) > 0 {
			action.vacationSubject = subjectQuoted[0]
		}
	}

	// The vacation message body is the last quoted string that is not the subject.
	if len(quoted) > 0 {
		lastQuoted := quoted[len(quoted)-1]
		if lastQuoted != action.vacationSubject || len(quoted) == 1 {
			action.vacationBody = lastQuoted
		} else if len(quoted) > 1 {
			// If the last quoted string equals the subject (unlikely),
			// use the one before it if available.
			action.vacationBody = quoted[len(quoted)-1]
		}
	}

	// Handle the case where subject and body are both quoted:
	// vacation :subject "subj" "body";
	// quoted = ["subj", "body"], subject = "subj", body should be "body"
	if action.vacationSubject != "" && len(quoted) >= 2 {
		action.vacationBody = quoted[len(quoted)-1]
	}

	return action, endLine, nil
}

// parseNotifyAction parses a notify action.
// Format: notify :method "mailto:admin@example.com" :message "New mail from ${from}";
func parseNotifyAction(lines []string, startLine int) (sieveAction, int, error) {
	full, endLine := collectStatement(lines, startLine)

	action := sieveAction{command: "notify"}

	// Parse :method
	if idx := strings.Index(full, ":method"); idx >= 0 {
		afterMethod := full[idx+len(":method"):]
		methodQuoted := extractQuotedStrings(afterMethod)
		if len(methodQuoted) > 0 {
			action.notifyMethod = methodQuoted[0]
		}
	}

	// Parse :message
	if idx := strings.Index(full, ":message"); idx >= 0 {
		afterMessage := full[idx+len(":message"):]
		messageQuoted := extractQuotedStrings(afterMessage)
		if len(messageQuoted) > 0 {
			action.notifyMessage = messageQuoted[0]
		}
	}

	// Variable substitution in notify message.
	// Replace ${from}, ${to}, ${subject} with actual header values.
	// This is deferred to execution time via metadata, but we store the
	// template as-is. The downstream notify handler expands variables.

	return action, endLine, nil
}

// collectStatement collects a Sieve statement that may span multiple lines,
// terminated by a semicolon. Returns the full statement (without the trailing
// semicolon) and the next line index to process.
func collectStatement(lines []string, startLine int) (string, int) {
	var b strings.Builder
	i := startLine
	for i < len(lines) {
		line := strings.TrimSpace(lines[i])
		i++
		b.WriteString(line)
		b.WriteByte(' ')
		if strings.HasSuffix(line, ";") {
			break
		}
	}
	full := strings.TrimSpace(b.String())
	full = strings.TrimSuffix(full, ";")
	full = strings.TrimSpace(full)
	return full, i
}

func extractQuotedStrings(s string) []string {
	var result []string
	inQuote := false
	current := strings.Builder{}
	inBracket := false

	for _, ch := range s {
		switch {
		case ch == '"' && !inQuote:
			inQuote = true
		case ch == '"' && inQuote:
			inQuote = false
			result = append(result, current.String())
			current.Reset()
		case inQuote:
			current.WriteRune(ch)
		case ch == '[':
			inBracket = true
		case ch == ']':
			inBracket = false
		default:
			_ = inBracket
		}
	}

	return result
}

// ValidateSieve checks if a Sieve script is syntactically valid.
func ValidateSieve(script string) error {
	_, err := parseSieve(script)
	if err != nil {
		return fmt.Errorf("invalid sieve script: %w", err)
	}
	return nil
}

func extractFirstQuoted(s string) string {
	start := strings.IndexByte(s, '"')
	if start < 0 {
		return strings.TrimSpace(s)
	}
	end := strings.IndexByte(s[start+1:], '"')
	if end < 0 {
		return s[start+1:]
	}
	return s[start+1 : start+1+end]
}
