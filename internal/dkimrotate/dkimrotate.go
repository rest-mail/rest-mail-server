// Package dkimrotate holds the rules a DKIM key rotation follows (#151): which
// selector the next key goes under, when a key is due to be replaced, and
// whether the new key's record can be seen in DNS yet. The worker that runs a
// rotation — generate, publish, wait, switch, retire — is built on these.
package dkimrotate

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	dkim "github.com/rest-mail/go-dkim"
)

// SelectorPrefix starts every selector a rotation creates: restmail-1,
// restmail-2, and so on.
const SelectorPrefix = "restmail-"

// NextSelector is the selector the key after current goes under. A key that
// rotation made, restmail-<n>, is followed by restmail-<n+1>; any other
// selector (the provisioner's "default", or one an operator chose) is
// followed by restmail-1.
func NextSelector(current string) string {
	n, ok := selectorNumber(current)
	if !ok {
		return SelectorPrefix + "1"
	}
	return SelectorPrefix + strconv.Itoa(n+1)
}

func selectorNumber(selector string) (int, bool) {
	digits, ok := strings.CutPrefix(selector, SelectorPrefix)
	if !ok || digits == "" || digits[0] == '0' {
		return 0, false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n == math.MaxInt {
		return 0, false
	}
	return n, true
}

// Due reports whether a key installed at createdAt has reached interval and
// should be replaced. A key with no known installation time (one saved before
// key ages were recorded) is due: nothing says it is young. An interval of
// zero or less turns rotation off.
func Due(createdAt *time.Time, interval time.Duration, now time.Time) bool {
	if interval <= 0 {
		return false
	}
	if createdAt == nil {
		return true
	}
	return now.Sub(*createdAt) >= interval
}

// ErrNotPublished is why Published fails when the selector's record is
// missing or carries another key.
var ErrNotPublished = errors.New("DKIM record not published")

// LookupTXT resolves a name's TXT records, one string per record, as
// net.Resolver.LookupTXT does.
type LookupTXT func(ctx context.Context, name string) ([]string, error)

// Published confirms that selector._domainkey.domain resolves, through
// lookup, to a DKIM record carrying the same public key as want (a record
// value as dkim.RecordValue renders it). Signing must not move to a new
// selector until this holds, or receivers fetch no key, or the old one, and
// the mail fails DKIM.
//
// Only the key type and key are compared: a published record may add tags
// (t=, h=, n=) or space them differently and still be the same key. A record
// that revokes the key (an empty p=) never matches.
func Published(ctx context.Context, lookup LookupTXT, domain, selector, want string) error {
	wantTags := recordTags(want)
	wantKey := wantTags["p"]
	if wantKey == "" {
		return fmt.Errorf("the expected DKIM record has no public key")
	}
	name := dkim.RecordName(selector, domain)
	txts, err := lookup(ctx, name)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrNotPublished, name, err)
	}
	for _, txt := range txts {
		tags := recordTags(txt)
		if v, ok := tags["v"]; ok && v != "DKIM1" {
			continue
		}
		if keyType(tags) == keyType(wantTags) && tags["p"] == wantKey {
			return nil
		}
	}
	if len(txts) == 0 {
		return fmt.Errorf("%w: %s has no TXT record", ErrNotPublished, name)
	}
	return fmt.Errorf("%w: %s does not carry the new key", ErrNotPublished, name)
}

// keyType is the record's k= tag, which RFC 6376 §3.6.1 defaults to rsa.
func keyType(tags map[string]string) string {
	if k := tags["k"]; k != "" {
		return k
	}
	return "rsa"
}

// recordTags splits a DKIM record (RFC 6376 §3.2 tag=value list) into its
// tags, or nil when it is invalid. Whitespace inside a value is dropped, as it is for the base64 p=.
func recordTags(record string) map[string]string {
	tags := make(map[string]string)
	for _, part := range strings.Split(record, ";") {
		name, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, seen := tags[name]; seen {
			return nil // a duplicated tag makes the whole record invalid
		}
		tags[name] = strings.Join(strings.Fields(value), "")
	}
	return tags
}
