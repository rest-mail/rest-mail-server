package pipeline

// ReasonCode is a bounded, low-cardinality classification of WHY a message
// reached a non-continue (rejected/quarantined/deferred/discarded) terminal
// outcome. It is derived deterministically from the terminal pipeline step's
// (filter name, action, FilterLog.Result) — NEVER from a filter's free-text
// Detail, which embeds unbounded values (domains, IPs, timestamps, scores).
//
// The enum is FIXED. A terminal that matches no specific security category
// collapses to ReasonCustomReject (a deliberate reject with no standard
// category) or ReasonOther (any other unmapped terminal), so the `reason_code`
// metric label — and the PR3 MessageTrace column that reuses this same mapper —
// can never grow unbounded regardless of how many custom filters exist.
type ReasonCode string

const (
	ReasonDMARCReject      ReasonCode = "dmarc_reject"
	ReasonDMARCQuarantine  ReasonCode = "dmarc_quarantine"
	ReasonSPFFail          ReasonCode = "spf_fail"
	ReasonGreylistDefer    ReasonCode = "greylist_defer"
	ReasonSizeExceeded     ReasonCode = "size_exceeded"
	ReasonRateLimited      ReasonCode = "rate_limited"
	ReasonVirusDetected    ReasonCode = "virus_detected"
	ReasonSpamThreshold    ReasonCode = "spam_threshold"
	ReasonRecipientUnknown ReasonCode = "recipient_unknown"
	ReasonHeaderInvalid    ReasonCode = "header_invalid"
	ReasonCustomReject     ReasonCode = "custom_reject"
	ReasonOther            ReasonCode = "other"
)

// ReasonForStep classifies a terminal step. It is a thin adapter over Reason
// that reads the step's filter name, terminal action, and structured result.
// Both the metrics observer (today) and the PR3 trace recorder (tomorrow) call
// this on the terminal step so a single deterministic mapping backs the metric
// label and the persisted trace column. Invoke it for a NON-CONTINUE terminal
// step; a continue step has no reject reason and maps to ReasonOther.
func ReasonForStep(step StepResult) ReasonCode {
	return Reason(step.FilterName, step.Action, step.Log.Result)
}

// Reason maps a terminal step's (filterName, action, result) to a bounded
// ReasonCode. The classification is keyed on the built-in filter's identity
// refined by its terminal action. The result (FilterLog.Result) is part of the
// contract and accepted for signature completeness, but the mapping never
// depends on free-text detail — every branch is decided by filter + action.
//
// Resolution order:
//  1. A specific security category for a recognised built-in filter + action.
//  2. Any other REJECT — a recognised policy/programmable built-in with no
//     dedicated code (sender_verify, domain_allowlist, contact_whitelist,
//     sieve, javascript, webhook) or an unknown/custom filter — → custom_reject.
//  3. Everything else (a non-reject terminal without a dedicated code, and
//     continue) → other.
func Reason(filterName string, action Action, result string) ReasonCode {
	switch filterName {
	case "dmarc_check":
		switch action {
		case ActionReject:
			return ReasonDMARCReject
		case ActionQuarantine:
			return ReasonDMARCQuarantine
		}
	case "spf_check":
		// The built-in spf_check never terminates (SPF alone continues; DMARC
		// enforces). Mapped defensively so a custom SPF-enforcing variant — or a
		// future change — lands in the right bounded bucket rather than "other".
		if action != ActionContinue {
			return ReasonSPFFail
		}
	case "size_check":
		if action == ActionReject {
			return ReasonSizeExceeded
		}
	case "greylist":
		if action == ActionDefer {
			return ReasonGreylistDefer
		}
	case "rate_limit":
		if action == ActionDefer {
			return ReasonRateLimited
		}
	case "clamav":
		// clamav's only non-continue action is a reject on a virus hit.
		if action != ActionContinue {
			return ReasonVirusDetected
		}
	case "rspamd":
		// rspamd terminals (reject or soft-reject/greylist defer) are all
		// spam-score driven.
		if action != ActionContinue {
			return ReasonSpamThreshold
		}
	case "recipient_check":
		// A reject is an unknown/over-quota recipient; a defer is a transient DB
		// error (no dedicated code → falls through to other).
		if action == ActionReject {
			return ReasonRecipientUnknown
		}
	case "header_validate":
		if action == ActionReject {
			return ReasonHeaderInvalid
		}
	}

	// No specific security category applied. A deliberate reject from any other
	// filter is a custom_reject; every remaining terminal (a non-reject action
	// without a dedicated code, or continue) is other.
	if action == ActionReject {
		return ReasonCustomReject
	}
	return ReasonOther
}
