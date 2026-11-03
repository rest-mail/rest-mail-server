package message

import (
	"fmt"
	"math/big"
	"regexp"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap/internal/wire"
)

// FuzzBodyPartial compares arbitrary suffixes with the RFC 3501 §9 grammar
// expressed independently as a regexp plus arbitrary-precision bounds. It also
// generates valid uint32 ranges and checks octets against an iterative oracle.
func FuzzBodyPartial(f *testing.F) {
	for _, suffix := range []string{
		"", "<0.1>", "<000.1>", "<4294967295.4294967295>",
		"<4294967296.1>", "<0.4294967296>", "<0>", "<0.0>", "<0.01>",
		"<+0.1>", "<0.+1>", "<0.1.2>", "<0.1>junk", "<0.\x001>",
	} {
		f.Add(suffix, uint32(0), uint32(1))
	}
	grammar := regexp.MustCompile(`^<([0-9]+)\.([1-9][0-9]*)>$`)
	f.Fuzz(func(t *testing.T, suffix string, start, count uint32) {
		if len(suffix) > 4096 {
			t.Skip()
		}
		wantOK := suffix == ""
		var origin, size big.Int
		if groups := grammar.FindStringSubmatch(suffix); groups != nil {
			origin.SetString(groups[1], 10)
			size.SetString(groups[2], 10)
			wantOK = origin.BitLen() <= 32 && size.BitLen() <= 32
		}
		got, ok := wire.ParseBodyPartial(suffix)
		if ok != wantOK {
			t.Fatalf("ParseBodyPartial(%q) = %#v, %t; grammar accepts %t", suffix, got, ok, wantOK)
		}
		if ok && suffix != "" && (got == nil || uint64(got.Start) != origin.Uint64() || uint64(got.Count) != size.Uint64()) {
			t.Fatalf("ParseBodyPartial(%q) = %#v; want %s.%s", suffix, got, &origin, &size)
		}
		if suffix == "" && got != nil {
			t.Fatalf("empty suffix produced %#v", got)
		}

		if count == 0 {
			count = 1
		}
		generated := fmt.Sprintf("<%d.%d>", start, count)
		got, ok = wire.ParseBodyPartial(generated)
		if !ok || got == nil || got.Start != start || got.Count != count {
			t.Fatalf("valid generated range %q rejected: %#v, %t", generated, got, ok)
		}
		const payload = "caf\xc3\xa9\r\n0123456789"
		var want []byte
		for i := range len(payload) {
			if uint64(i) >= uint64(start) && uint64(i)-uint64(start) < uint64(count) {
				want = append(want, payload[i])
			}
		}
		if sliced := partialOctets(payload, got.Start, got.Count); sliced != string(want) {
			t.Fatalf("partialOctets(%q, %d, %d) = %q, want %q", payload, start, count, sliced, want)
		}
	})
}
