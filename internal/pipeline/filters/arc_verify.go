package filters

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"

	"github.com/restmail/restmail/internal/pipeline"
)

// arcVerifyFilter verifies ARC (Authenticated Received Chain) headers on inbound
// messages per RFC 8617. It validates chain structure (instance numbering, cv values,
// header set completeness) and adds arc=pass/fail/none to Authentication-Results.
// Full cryptographic signature verification is noted as neutral since it requires
// DNS lookups for public keys (similar to DKIM).
type arcVerifyFilter struct{}

func init() {
	pipeline.DefaultRegistry.Register("arc_verify", NewARCVerify)
}

// NewARCVerify creates a new ARC verification filter.
func NewARCVerify(_ []byte) (pipeline.Filter, error) {
	return &arcVerifyFilter{}, nil
}

func (f *arcVerifyFilter) Name() string             { return "arc_verify" }
func (f *arcVerifyFilter) Type() pipeline.FilterType { return pipeline.FilterTypeTransform }

// arcHeaderSet represents one complete ARC header set at a given instance number.
type arcHeaderSet struct {
	Instance              int
	AuthenticationResults string // ARC-Authentication-Results
	MessageSignature      string // ARC-Message-Signature
	Seal                  string // ARC-Seal
}

// instanceRe matches the i= tag in ARC headers.
var instanceRe = regexp.MustCompile(`\bi=(\d+)\b`)

// cvRe matches the cv= tag in ARC-Seal headers.
var cvRe = regexp.MustCompile(`\bcv=(none|pass|fail)\b`)

func (f *arcVerifyFilter) Execute(_ context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	modified := *email

	// Ensure maps are initialised
	if modified.Headers.Raw == nil {
		modified.Headers.Raw = make(map[string][]string)
	}
	if modified.Headers.Extra == nil {
		modified.Headers.Extra = make(map[string]string)
	}
	if modified.Metadata == nil {
		modified.Metadata = make(map[string]string)
	}

	// Collect ARC headers from Raw
	arcAAR := email.Headers.Raw["Arc-Authentication-Results"]
	arcAMS := email.Headers.Raw["Arc-Message-Signature"]
	arcAS := email.Headers.Raw["Arc-Seal"]

	// If no ARC headers at all, result is "none"
	if len(arcAAR) == 0 && len(arcAMS) == 0 && len(arcAS) == 0 {
		addARCAuthResult(&modified, "none")
		modified.Metadata["arc_status"] = "none"

		return &pipeline.FilterResult{
			Type:    pipeline.FilterTypeTransform,
			Action:  pipeline.ActionContinue,
			Message: &modified,
			Log: pipeline.FilterLog{
				Filter: "arc_verify",
				Result: "none",
				Detail: "no ARC headers present",
			},
		}, nil
	}

	// Parse header sets keyed by instance number
	sets := map[int]*arcHeaderSet{}

	for _, h := range arcAAR {
		inst := parseInstance(h)
		if inst < 1 {
			continue
		}
		if _, ok := sets[inst]; !ok {
			sets[inst] = &arcHeaderSet{Instance: inst}
		}
		sets[inst].AuthenticationResults = h
	}
	for _, h := range arcAMS {
		inst := parseInstance(h)
		if inst < 1 {
			continue
		}
		if _, ok := sets[inst]; !ok {
			sets[inst] = &arcHeaderSet{Instance: inst}
		}
		sets[inst].MessageSignature = h
	}
	for _, h := range arcAS {
		inst := parseInstance(h)
		if inst < 1 {
			continue
		}
		if _, ok := sets[inst]; !ok {
			sets[inst] = &arcHeaderSet{Instance: inst}
		}
		sets[inst].Seal = h
	}

	if len(sets) == 0 {
		addARCAuthResult(&modified, "fail")
		modified.Metadata["arc_status"] = "fail"

		return &pipeline.FilterResult{
			Type:    pipeline.FilterTypeTransform,
			Action:  pipeline.ActionContinue,
			Message: &modified,
			Log: pipeline.FilterLog{
				Filter: "arc_verify",
				Result: "fail",
				Detail: "ARC headers present but no valid instance numbers found",
			},
		}, nil
	}

	// Sort instance numbers to validate chain continuity
	instances := make([]int, 0, len(sets))
	for i := range sets {
		instances = append(instances, i)
	}
	sort.Ints(instances)

	// Validate chain
	result, detail := validateARCChain(instances, sets)

	addARCAuthResult(&modified, result)
	modified.Metadata["arc_status"] = result

	return &pipeline.FilterResult{
		Type:    pipeline.FilterTypeTransform,
		Action:  pipeline.ActionContinue,
		Message: &modified,
		Log: pipeline.FilterLog{
			Filter: "arc_verify",
			Result: result,
			Detail: detail,
		},
	}, nil
}

// validateARCChain checks structural validity of the ARC chain.
func validateARCChain(instances []int, sets map[int]*arcHeaderSet) (string, string) {
	n := len(instances)

	// Check that the chain starts at 1 and has no gaps
	if instances[0] != 1 {
		return "fail", fmt.Sprintf("chain does not start at i=1 (starts at i=%d)", instances[0])
	}
	for idx := 0; idx < n; idx++ {
		expected := idx + 1
		if instances[idx] != expected {
			return "fail", fmt.Sprintf("chain gap: expected i=%d but found i=%d", expected, instances[idx])
		}
	}

	// Verify each set is complete (has all three headers)
	for _, i := range instances {
		s := sets[i]
		if s.AuthenticationResults == "" {
			return "fail", fmt.Sprintf("i=%d missing ARC-Authentication-Results", i)
		}
		if s.MessageSignature == "" {
			return "fail", fmt.Sprintf("i=%d missing ARC-Message-Signature", i)
		}
		if s.Seal == "" {
			return "fail", fmt.Sprintf("i=%d missing ARC-Seal", i)
		}
	}

	// Validate cv= values in each ARC-Seal
	for _, i := range instances {
		s := sets[i]
		cv := parseCVValue(s.Seal)

		if i == 1 {
			// First set must have cv=none
			if cv != "none" {
				return "fail", fmt.Sprintf("i=1 ARC-Seal has cv=%s (expected cv=none)", cv)
			}
		} else {
			// Subsequent sets must have cv=pass
			if cv != "pass" {
				return "fail", fmt.Sprintf("i=%d ARC-Seal has cv=%s (expected cv=pass)", i, cv)
			}
		}
	}

	// Check the most recent ARC-Seal's cv value
	mostRecent := sets[instances[n-1]]
	lastCV := parseCVValue(mostRecent.Seal)
	if n == 1 && lastCV != "none" {
		return "fail", fmt.Sprintf("single-set chain: most recent ARC-Seal cv=%s (expected none)", lastCV)
	}
	if n > 1 && lastCV != "pass" {
		return "fail", fmt.Sprintf("most recent ARC-Seal (i=%d) cv=%s (expected pass)", instances[n-1], lastCV)
	}

	// Structural validation passed. Cryptographic verification is not performed
	// because it requires DNS lookups for public keys (like DKIM verification).
	return "pass", fmt.Sprintf("chain valid: %d set(s), structure verified (crypto verification neutral — requires DNS key lookup)", n)
}

// parseInstance extracts the i= value from an ARC header.
func parseInstance(header string) int {
	m := instanceRe.FindStringSubmatch(header)
	if len(m) < 2 {
		return 0
	}
	val, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return val
}

// parseCVValue extracts the cv= value from an ARC-Seal header.
func parseCVValue(seal string) string {
	m := cvRe.FindStringSubmatch(seal)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// addARCAuthResult appends an arc= authentication result to the email headers.
func addARCAuthResult(email *pipeline.EmailJSON, result string) {
	arcResult := "restmail; arc=" + result

	// Add to Raw (append)
	email.Headers.Raw["Authentication-Results"] = append(
		email.Headers.Raw["Authentication-Results"],
		arcResult,
	)

	// Add to Extra (append to existing if present)
	existing := email.Headers.Extra["Authentication-Results"]
	if existing != "" {
		email.Headers.Extra["Authentication-Results"] = existing + "; " + arcResult
	} else {
		email.Headers.Extra["Authentication-Results"] = arcResult
	}
}

