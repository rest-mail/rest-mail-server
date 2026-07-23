package pipeline

import "testing"

// TestReason_RealTerminalCases pins the deterministic (filter, action, result)
// → reason_code mapping for every terminal a built-in filter can actually
// produce, plus the custom_reject / other fallbacks. Result strings mirror the
// real FilterLog.Result each filter emits (adapters set Result = action).
func TestReason_RealTerminalCases(t *testing.T) {
	cases := []struct {
		name   string
		filter string
		action Action
		result string
		want   ReasonCode
	}{
		// Coded security built-ins (real terminals).
		{"dmarc reject", "dmarc_check", ActionReject, "fail", ReasonDMARCReject},
		{"dmarc quarantine", "dmarc_check", ActionQuarantine, "fail", ReasonDMARCQuarantine},
		{"size reject", "size_check", ActionReject, "reject", ReasonSizeExceeded},
		{"greylist defer", "greylist", ActionDefer, "defer", ReasonGreylistDefer},
		{"rate_limit defer", "rate_limit", ActionDefer, "defer", ReasonRateLimited},
		{"clamav reject", "clamav", ActionReject, "reject", ReasonVirusDetected},
		{"rspamd reject", "rspamd", ActionReject, "reject", ReasonSpamThreshold},
		{"rspamd defer (soft reject/greylist)", "rspamd", ActionDefer, "defer", ReasonSpamThreshold},
		{"recipient reject", "recipient_check", ActionReject, "reject", ReasonRecipientUnknown},
		{"header_validate reject", "header_validate", ActionReject, "reject", ReasonHeaderInvalid},

		// spf_check never terminates in the built-in (always continue); the
		// defensive non-continue mapping still lands in spf_fail.
		{"spf continue -> other", "spf_check", ActionContinue, "pass", ReasonOther},
		{"spf reject (defensive)", "spf_check", ActionReject, "fail", ReasonSPFFail},

		// Recognised policy/programmable built-ins with no dedicated code: a
		// deliberate reject is custom_reject.
		{"sender_verify reject", "sender_verify", ActionReject, "reject", ReasonCustomReject},
		{"domain_allowlist reject", "domain_allowlist", ActionReject, "blocked", ReasonCustomReject},
		{"contact_whitelist reject", "contact_whitelist", ActionReject, "blocked", ReasonCustomReject},
		{"sieve reject", "sieve", ActionReject, "reject", ReasonCustomReject},
		{"javascript reject", "javascript", ActionReject, "reject", ReasonCustomReject},
		{"webhook reject", "webhook", ActionReject, "reject", ReasonCustomReject},

		// Non-reject terminals without a dedicated code collapse to other.
		{"sieve discard -> other", "sieve", ActionDiscard, "discard", ReasonOther},
		{"recipient defer (db error) -> other", "recipient_check", ActionDefer, "defer", ReasonOther},
		{"dmarc discard (unmapped action) -> other", "dmarc_check", ActionDiscard, "fail", ReasonOther},

		// Unknown/custom filters.
		{"unknown reject -> custom_reject", "my_blocklist", ActionReject, "reject", ReasonCustomReject},
		{"unknown defer -> other", "my_thing", ActionDefer, "defer", ReasonOther},
		{"unknown continue -> other", "my_thing", ActionContinue, "pass", ReasonOther},
		{"empty filter reject -> custom_reject", "", ActionReject, "", ReasonCustomReject},
	}

	for _, c := range cases {
		if got := Reason(c.filter, c.action, c.result); got != c.want {
			t.Errorf("%s: Reason(%q,%q,%q) = %q, want %q", c.name, c.filter, c.action, c.result, got, c.want)
		}
	}
}

// TestReasonForStep_UsesFilterActionResult verifies the StepResult adapter reads
// the step's filter name, terminal action, and structured result (never Detail).
func TestReasonForStep_UsesFilterActionResult(t *testing.T) {
	step := StepResult{
		FilterName: "dmarc_check",
		Action:     ActionReject,
		Log:        FilterLog{Filter: "dmarc_check", Result: "fail", Detail: "policy=reject domain=evil.test"},
	}
	if got := ReasonForStep(step); got != ReasonDMARCReject {
		t.Errorf("ReasonForStep = %q, want %q", got, ReasonDMARCReject)
	}

	// Detail must never influence the code: a custom filter rejecting with a
	// detail that happens to contain "dmarc" is still custom_reject.
	misleading := StepResult{
		FilterName: "my_custom",
		Action:     ActionReject,
		Log:        FilterLog{Filter: "my_custom", Result: "reject", Detail: "dmarc size virus spam"},
	}
	if got := ReasonForStep(misleading); got != ReasonCustomReject {
		t.Errorf("ReasonForStep(misleading detail) = %q, want %q", got, ReasonCustomReject)
	}
}
