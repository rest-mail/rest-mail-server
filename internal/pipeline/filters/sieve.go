package filters

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/restmail/restmail/internal/pipeline"
)

// sieveConfig holds the Sieve script for this filter.
type sieveConfig struct {
	Script string `json:"script"`
}

// sieveFilter implements a basic Sieve interpreter for email filtering.
// Supports: keep, fileinto, redirect, discard, reject, and basic conditionals.
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
	test     string // "header", "address", "size", "true"
	header   string
	match    string // ":contains", ":is", ":matches"
	values   []string
	sizeOp   string // ":over", ":under"
	sizeVal  int64
	negate   bool
}

type sieveAction struct {
	command string // "keep", "fileinto", "redirect", "discard", "reject"
	arg     string
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
	}

	if cond.negate {
		return !result
	}
	return result
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
		patternLower := strings.ToLower(pattern)
		switch matchType {
		case ":is":
			if valueLower == patternLower {
				return true
			}
		case ":contains":
			if strings.Contains(valueLower, patternLower) {
				return true
			}
		case ":matches":
			// Simple glob matching
			if globMatch(valueLower, patternLower) {
				return true
			}
		default:
			// Default to contains
			if strings.Contains(valueLower, patternLower) {
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

		// Top-level actions
		action, err := parseSieveAction(line)
		if err == nil {
			rules = append(rules, sieveRule{
				condition: sieveCondition{test: "true"},
				actions:   []sieveAction{action},
			})
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
		i++

		if actionLine == "}" {
			break
		}

		if strings.TrimSpace(actionLine) == "stop;" {
			rule.stop = true
			continue
		}

		action, err := parseSieveAction(actionLine)
		if err == nil {
			rule.actions = append(rule.actions, action)
		}
	}

	return rule, i, nil
}

func parseSieveCondition(cond string) sieveCondition {
	cond = strings.TrimSpace(cond)

	// "header :contains "Subject" ["invoice", "receipt"]"
	if strings.HasPrefix(cond, "header ") {
		return parseHeaderCondition(cond)
	}
	if strings.HasPrefix(cond, "address ") {
		return parseAddressCondition(cond)
	}
	if strings.HasPrefix(cond, "size ") {
		return parseSizeCondition(cond)
	}
	if strings.HasPrefix(cond, "not ") {
		inner := parseSieveCondition(strings.TrimPrefix(cond, "not "))
		inner.negate = true
		return inner
	}

	return sieveCondition{test: "true"}
}

func parseHeaderCondition(cond string) sieveCondition {
	sc := sieveCondition{test: "header"}

	if strings.Contains(cond, ":contains") {
		sc.match = ":contains"
	} else if strings.Contains(cond, ":is") {
		sc.match = ":is"
	} else if strings.Contains(cond, ":matches") {
		sc.match = ":matches"
	} else {
		sc.match = ":contains"
	}

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

	if strings.Contains(cond, ":is") {
		sc.match = ":is"
	} else if strings.Contains(cond, ":contains") {
		sc.match = ":contains"
	} else {
		sc.match = ":is"
	}

	parts := extractQuotedStrings(cond)
	if len(parts) > 0 {
		sc.header = parts[0]
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
