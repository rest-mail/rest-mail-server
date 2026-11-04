package dkimrotate

import "encoding/base64"

// What Compare finds wrong with the records published at a DKIM selector (#1830).
const (
	// ProblemMissing: the selector has no TXT record.
	ProblemMissing = "missing"
	// ProblemMalformed: no record there is a DKIM key record (RFC 6376 §3.6.1):
	// a duplicated tag, a version other than DKIM1, or no p= carrying base64.
	ProblemMalformed = "malformed"
	// ProblemDifferentKeyType: a DKIM record is there, for another key type (k=).
	ProblemDifferentKeyType = "different_key_type"
	// ProblemDifferentKey: a DKIM record of the right type is there, but its p=
	// is another key, or empty (the key is revoked).
	ProblemDifferentKey = "different_key"
)

// Compare checks the TXT records published at a selector, one string per
// record as net.Resolver.LookupTXT gives them, against want, the record value
// to publish (as PublicRecord or RotationRecord renders it). It gives the record
// it judged by and what is wrong with it, or "" when that record carries the
// same key type and key as want, or is revoked as want is. As in Published, a
// record may add tags or space them differently and still match. When nothing
// matches, the record nearest to want is the one judged: a DKIM record of the
// right type, then one of another type, then the first record there.
func Compare(txts []string, want string) (record, problem string) {
	if len(txts) == 0 {
		return "", ProblemMissing
	}
	wantTags := recordTags(want)
	wantKey := wantTags["p"]
	record, problem = txts[0], ProblemMalformed
	for _, txt := range txts {
		tags := recordTags(txt)
		key, present := tags["p"]
		if tags == nil || !present || (tags["v"] != "" && tags["v"] != "DKIM1") {
			continue
		}
		if _, err := base64.StdEncoding.DecodeString(key); err != nil {
			continue
		}
		// An empty p= revokes whatever type of key it was (RFC 6376 §3.6.1).
		sameType := wantKey == "" || keyType(tags) == keyType(wantTags)
		switch {
		case !sameType:
			if problem == ProblemMalformed {
				record, problem = txt, ProblemDifferentKeyType
			}
		case key == wantKey:
			return txt, ""
		case problem != ProblemDifferentKey:
			record, problem = txt, ProblemDifferentKey
		}
	}
	return record, problem
}
