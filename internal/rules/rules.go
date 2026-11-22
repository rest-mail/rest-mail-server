// Package rules defines the bounded native account rule language. Sieve is an
// export of this representation, never its execution engine.
package rules

import (
	"fmt"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
)

const MaxRules = 100

type Condition struct {
	Field    string `json:"field" enums:"subject,from,to"`
	Operator string `json:"operator" enums:"contains,is"`
	Value    string `json:"value" minLength:"1" maxLength:"1024"`
}
type Action struct {
	Type  string `json:"type" enums:"fileinto,flag,forward"`
	Value string `json:"value" minLength:"1" maxLength:"1024"`
}

// Definition matches all conditions, then performs actions in order. Positions
// sort ascending; equal positions sort by stable ID. Forward keeps a local copy.
type Definition struct {
	Name       string      `json:"name" minLength:"1" maxLength:"200"`
	Enabled    bool        `json:"enabled"`
	Position   int         `json:"position" minimum:"0" maximum:"1000000"`
	Conditions []Condition `json:"conditions" minItems:"1" maxItems:"20" gorm:"serializer:json;type:jsonb"`
	Actions    []Action    `json:"actions" minItems:"1" maxItems:"20" gorm:"serializer:json;type:jsonb"`
}
type Rule struct {
	ID      uint `json:"id"`
	Version uint `json:"version"`
	Definition
}

// Effect is an immutable delivery snapshot except for UndoneAt. Rule IDs remain
// meaningful after deletion; before values restore the exact field rules changed.
type Effect struct {
	ID          string     `json:"id"`
	RuleID      uint       `json:"rule_id"`
	RuleVersion uint       `json:"rule_version"`
	Type        string     `json:"type" enums:"fileinto,flag,forward"`
	Value       string     `json:"value"`
	Before      string     `json:"before"`
	Undoable    bool       `json:"undoable"`
	UndoneAt    *time.Time `json:"undone_at,omitempty"`
}

func Validate(d Definition) error {
	if strings.TrimSpace(d.Name) == "" || utf8.RuneCountInString(d.Name) > 200 {
		return fmt.Errorf("name must contain 1 to 200 characters")
	}
	if d.Position < 0 || d.Position > 1000000 {
		return fmt.Errorf("position must be 0 to 1000000")
	}
	if len(d.Conditions) < 1 || len(d.Conditions) > 20 || len(d.Actions) < 1 || len(d.Actions) > 20 {
		return fmt.Errorf("rules need 1 to 20 conditions and actions")
	}
	for _, c := range d.Conditions {
		if c.Field != "subject" && c.Field != "from" && c.Field != "to" {
			return fmt.Errorf("unsupported condition field %q", c.Field)
		}
		if c.Operator != "is" && c.Operator != "contains" {
			return fmt.Errorf("unsupported condition operator %q", c.Operator)
		}
		if c.Value == "" || utf8.RuneCountInString(c.Value) > 1024 || strings.ContainsAny(c.Value, "\x00\r\n") {
			return fmt.Errorf("condition value must be 1 to 1024 characters without line breaks or NUL")
		}
	}
	for _, a := range d.Actions {
		if a.Value == "" || utf8.RuneCountInString(a.Value) > 1024 || strings.ContainsAny(a.Value, "\x00\r\n") {
			return fmt.Errorf("action value must be 1 to 1024 characters without line breaks or NUL")
		}
		switch a.Type {
		case "fileinto":
			if p := rmail.FolderNameProblem(a.Value); p != "" {
				return fmt.Errorf("fileinto: %s", p)
			}
		case "flag":
			if a.Value != `\Seen` && a.Value != `\Flagged` && a.Value != `\Draft` {
				return fmt.Errorf("flag must be \\Seen, \\Flagged or \\Draft")
			}
		case "forward":
			addr, err := mail.ParseAddress(a.Value)
			if err != nil || addr.Address != a.Value || !strings.Contains(a.Value, "@") {
				return fmt.Errorf("forward must name one plain email address")
			}
		default:
			return fmt.Errorf("unsupported action %q", a.Type)
		}
	}
	return nil
}

// Match uses the same i;ascii-casemap comparison emitted by Sieve. Address
// conditions match individual parsed header addresses, not display names.
func Match(c Condition, values []string) bool {
	needle := asciiLower(c.Value)
	for _, v := range values {
		v = asciiLower(v)
		if c.Operator == "is" && v == needle || c.Operator == "contains" && strings.Contains(v, needle) {
			return true
		}
	}
	return false
}
func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, s)
}
func quote(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }

// Export includes a stable identity marker even for disabled rules. It never
// includes a disabled action as executable Sieve. No generated script is stored.
func Export(rs []Rule) string {
	var b strings.Builder
	b.WriteString("# REST Mail structured rules; edit through the rules API.\nrequire [\"fileinto\", \"imap4flags\", \"copy\"];\n")
	for _, r := range rs {
		fmt.Fprintf(&b, "# restmail-rule %d version %d enabled %t\n", r.ID, r.Version, r.Enabled)
		if !r.Enabled {
			continue
		}
		b.WriteString("if allof (")
		for i, c := range r.Conditions {
			if i > 0 {
				b.WriteString(", ")
			}
			verb, field := "header", "Subject"
			if c.Field != "subject" {
				verb, field = "address", c.Field
			}
			fmt.Fprintf(&b, "%s :%s %s %s", verb, c.Operator, quote(field), quote(c.Value))
		}
		b.WriteString(") {\n")
		for _, a := range r.Actions {
			switch a.Type {
			case "fileinto":
				b.WriteString("  fileinto " + quote(rmail.CanonicalFolder(a.Value)) + ";\n")
			case "flag":
				b.WriteString("  addflag " + quote(a.Value) + ";\n")
			case "forward":
				b.WriteString("  redirect :copy " + quote(a.Value) + ";\n")
			}
		}
		b.WriteString("}\n")
	}
	return b.String()
}

func FlagColumn(flag string) string {
	switch flag {
	case `\Seen`:
		return "is_read"
	case `\Flagged`:
		return "is_flagged"
	case `\Draft`:
		return "is_draft"
	}
	return ""
}
func Bool(s string) bool { v, _ := strconv.ParseBool(s); return v }
