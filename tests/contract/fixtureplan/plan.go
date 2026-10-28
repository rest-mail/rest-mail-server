// Package fixtureplan declares the identities shared by disposable contract
// setup, E2E principal proofs and Vertrag hooks. Live credentials and IDs are
// produced by setup, never stored in this declaration.
package fixtureplan

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

//go:embed defaults.json
var defaults []byte

type Principal struct {
	Kind        string `json:"kind"`
	Address     string `json:"address,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Owner       string `json:"owner,omitempty"`
	Password    bool   `json:"password,omitempty"`
	Account     string `json:"account,omitempty"`
	Name        string `json:"name,omitempty"`
}

type QueueEntry struct {
	Sender           string `json:"sender"`
	Recipient        string `json:"recipient"`
	Domain           string `json:"domain"`
	Subject          string `json:"subject"`
	Body             string `json:"body"`
	Status           string `json:"status"`
	NextAttemptHours int    `json:"next_attempt_hours"`
	ExpiresHours     int    `json:"expires_hours"`
}

type Plan struct {
	Domain     string                `json:"domain"`
	Principals map[string]Principal  `json:"principals"`
	Queue      map[string]QueueEntry `json:"queue"`
}

// Default returns a fresh copy so a phase cannot mutate the next phase's plan.
func Default() (Plan, error) { return Parse(defaults) }

// Parse rejects drift in the declaration before any fixture writes begin.
func Parse(data []byte) (Plan, error) {
	var plan Plan
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return Plan{}, fmt.Errorf("decode contract fixture plan: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return Plan{}, fmt.Errorf("contract fixture plan must be one JSON object")
	}
	if err := plan.validate(); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func (p Plan) validate() error {
	if p.Domain != "restmail.test" {
		return fmt.Errorf("contract fixture plan requires the disposable restmail.test domain")
	}
	for _, name := range []string{"admin", "ordinary", "owned", "app_password", "none"} {
		if _, ok := p.Principals[name]; !ok {
			return fmt.Errorf("contract fixture plan is missing principal %q", name)
		}
	}
	addresses := map[string]bool{}
	for name, principal := range p.Principals {
		switch principal.Kind {
		case "existing", "account":
			if local, domain, ok := strings.Cut(principal.Address, "@"); !ok || local == "" || domain != p.Domain || strings.ContainsAny(principal.Address, "\r\n\t ") {
				return fmt.Errorf("principal %q is outside the disposable domain", name)
			}
			if addresses[principal.Address] {
				return fmt.Errorf("principal %q duplicates another account identity", name)
			}
			addresses[principal.Address] = true
			if principal.Owner != "" {
				owner, ok := p.Principals[principal.Owner]
				if !ok || owner.Kind != "existing" {
					return fmt.Errorf("principal %q has no existing owner", name)
				}
			}
		case "app_password":
			account, ok := p.Principals[principal.Account]
			if !ok || account.Kind != "account" || !account.Password || principal.Name == "" {
				return fmt.Errorf("principal %q needs a named app password on a password account", name)
			}
		case "anonymous":
			if principal.Address != "" || principal.Account != "" || principal.Password {
				return fmt.Errorf("anonymous principal %q cannot hold a credential", name)
			}
		default:
			return fmt.Errorf("principal %q has an unknown fixture kind", name)
		}
	}
	if p.Principals["admin"].Kind != "existing" || p.Principals["ordinary"].Kind != "account" || !p.Principals["ordinary"].Password || p.Principals["ordinary"].Owner != "admin" || p.Principals["owned"].Kind != "account" || p.Principals["owned"].Password || p.Principals["owned"].Owner != "admin" || p.Principals["app_password"].Account != "ordinary" || p.Principals["none"].Kind != "anonymous" {
		return fmt.Errorf("contract principal kinds must match their setup and authorization proofs")
	}
	for _, name := range []string{"retry", "bounce"} {
		if _, ok := p.Queue[name]; !ok {
			return fmt.Errorf("contract fixture plan is missing the %s queue entry", name)
		}
	}
	for name, entry := range p.Queue {
		local, domain, ok := strings.Cut(entry.Recipient, "@")
		if !ok || local == "" || domain != entry.Domain || !strings.HasSuffix(domain, ".invalid") || entry.Sender != p.Principals["admin"].Address || entry.Subject == "" || strings.ContainsAny(entry.Recipient+entry.Subject, "\r\n") {
			return fmt.Errorf("queue fixture %q must use the fixture admin and a reserved .invalid destination", name)
		}
		if entry.Status != "deferred" || entry.NextAttemptHours < 1 || entry.ExpiresHours <= entry.NextAttemptHours || entry.ExpiresHours > 168 {
			return fmt.Errorf("queue fixture %q must stay deferred until after setup, and expire within a week", name)
		}
	}
	return nil
}
