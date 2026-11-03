package pipeline

import (
	"strings"

	dkim "github.com/rest-mail/go-dkim/pkg/dkim"
)

// StoredDKIMCovers reports whether a stored received message carries a DKIM signature
// that this server verified as pass and whose h= tag lists every one of fields
// (case-insensitively, RFC 6376 §3.5). header is the message's header section, as stored:
// this server's Authentication-Results first, then the fields as they arrived.
//
// The stored Authentication-Results records one dkim verdict per DKIM-Signature field,
// in the order the fields appear (dkim_verify verifies them in that order), with the
// signature's d= but not which fields it signed. So the i-th verdict is paired with the
// i-th DKIM-Signature field of the stored header, and the field's own h= tag says what that
// signature covered. Every DKIM-Signature field in the stored header was there when the
// message was verified, so a field added by the sender to claim a wider h= gets a verdict
// of its own and cannot borrow another signature's pass. When the verdicts and the fields
// cannot be paired one to one, or a pair's d= differs, nothing counts as covered.
func StoredDKIMCovers(header string, fields ...string) bool {
	results, ok := ParseStoredAuthResults(header)
	if !ok {
		return false
	}
	var verdicts []AuthResult
	for _, r := range results {
		if r.Method == AuthDKIM && r.Property == PropHeaderD {
			verdicts = append(verdicts, r)
		}
	}
	headers, _ := dkim.SplitMessage([]byte(header))
	var signatures []dkim.Header
	for _, h := range headers {
		if strings.EqualFold(h.Name, "DKIM-Signature") {
			signatures = append(signatures, h)
		}
	}
	if len(verdicts) == 0 || len(verdicts) != len(signatures) {
		return false
	}
	for i, verdict := range verdicts {
		if verdict.Result != dkim.ResultPass {
			continue
		}
		tags := dkim.ParseTagList(signatures[i].Value)
		if !strings.EqualFold(tags["d"], verdict.Domain) {
			return false
		}
		if signedAll(tags["h"], fields) {
			return true
		}
	}
	return false
}

// signedAll reports whether the h= value hTag names every field.
func signedAll(hTag string, fields []string) bool {
	signed := map[string]bool{}
	for _, name := range strings.Split(strings.Join(strings.Fields(hTag), ""), ":") {
		signed[strings.ToLower(name)] = true
	}
	for _, field := range fields {
		if !signed[strings.ToLower(field)] {
			return false
		}
	}
	return true
}
