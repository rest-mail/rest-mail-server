package filters

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/internal/rules"
)

func init() {
	pipeline.DefaultRegistry.Register("mail_rules", NewNativeRulesWithPolicy(SieveRedirectPolicy{}))
}

type nativeRulesFilter struct {
	rules    []rules.Rule
	redirect SieveRedirectPolicy
}

func NewNativeRulesWithPolicy(policy SieveRedirectPolicy) pipeline.FilterFactory {
	return func(raw []byte) (pipeline.Filter, error) {
		var rs []rules.Rule
		if err := json.Unmarshal(raw, &rs); err != nil {
			return nil, err
		}
		if len(rs) > rules.MaxRules {
			return nil, fmt.Errorf("too many native rules")
		}
		for _, r := range rs {
			if r.ID == 0 || r.Version == 0 {
				return nil, fmt.Errorf("native rule needs identity and version")
			}
			if err := rules.Validate(r.Definition); err != nil {
				return nil, err
			}
		}
		return &nativeRulesFilter{rules: rs, redirect: policy}, nil
	}
}
func (f *nativeRulesFilter) Name() string              { return "mail_rules" }
func (f *nativeRulesFilter) Type() pipeline.FilterType { return pipeline.FilterTypeTransform }
func (f *nativeRulesFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	out := *email
	out.Metadata = make(map[string]string, len(email.Metadata))
	for k, v := range email.Metadata {
		out.Metadata[k] = v
	}
	out.RuleEffects = nil // never inherit another recipient's effects
	out.RuleMatches = nil
	exec := &metadataExecutor{email: &out, redirect: f.redirect, localDomains: recipientDomains(&out)}
	for _, r := range f.rules {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !r.Enabled {
			continue
		}
		matched := true
		for _, c := range r.Conditions {
			found := false
			visit := func(a pipeline.Address) {
				if !found {
					found = rules.Match(c, []string{a.Address})
				}
			}
			switch c.Field {
			case "subject":
				found = rules.Match(c, []string{out.Headers.Subject})
			case "from":
				if !out.Headers.From.Visit(visit) {
					found = false
				}
			case "to":
				if !out.Headers.To.Visit(visit) {
					found = false
				}
			}
			if !found {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		out.RuleMatches = append(out.RuleMatches, r.ID)
		for _, a := range r.Actions {
			effect := rules.Effect{ID: uuid.NewString(), RuleID: r.ID, RuleVersion: r.Version, Type: a.Type, Value: a.Value, Undoable: a.Type != "forward"}
			switch a.Type {
			case "fileinto":
				effect.Before = out.Metadata["deliver_to_folder"]
				if effect.Before == "" {
					effect.Before = "INBOX"
				}
				if rmail.FolderNameProblem(effect.Before) != "" {
					effect.Before = "INBOX"
				}
				effect.Before = rmail.CanonicalFolder(effect.Before)
				effect.Value = rmail.CanonicalFolder(a.Value)
				if effect.Before == effect.Value {
					continue
				}
				exec.FileInto(effect.Value, true)
			case "flag":
				before := false
				for _, flag := range strings.Fields(out.Metadata["imap_flags"]) {
					if strings.EqualFold(flag, a.Value) {
						before = true
					}
				}
				if before {
					continue
				}
				effect.Before = strconv.FormatBool(before)
				exec.Flag("addflag", []string{a.Value})
			case "forward":
				count := len(exec.redirectTargets)
				exec.Redirect(a.Value, true)
				if len(exec.redirectTargets) == count {
					continue
				}
			}
			out.RuleEffects = append(out.RuleEffects, effect)
		}
	}
	if len(exec.redirectTargets) > 0 {
		// Append to any preceding domain redirects; native forwards always retain a
		// local message and are committed with that message by the delivery consumer.
		var prior []string
		if encoded := out.Metadata["redirect_to"]; encoded != "" {
			if err := json.Unmarshal([]byte(encoded), &prior); err != nil {
				return nil, err
			}
		}
		out.Metadata["redirect_to"] = encodeRedirectTargets(append(prior, exec.redirectTargets...))
		delete(out.Metadata, "redirect_suppress_keep")
	}
	return &pipeline.FilterResult{Type: pipeline.FilterTypeTransform, Action: pipeline.ActionContinue, Message: &out, Log: pipeline.FilterLog{Filter: "mail_rules", Result: "pass", Detail: fmt.Sprintf("%d rule effects", len(out.RuleEffects))}}, nil
}
