package mail

import (
	"runtime"
	"strings"
	"testing"
)

// bytesAllocated is what one run allocates, the least of a few runs so that anything else
// the process allocates in between doesn't count.
func bytesAllocated(work func()) uint64 {
	var before, after runtime.MemStats
	best := ^uint64(0)
	for range 3 {
		runtime.ReadMemStats(&before)
		work()
		runtime.ReadMemStats(&after)
		best = min(best, after.TotalAlloc-before.TotalAlloc)
	}
	return best
}

// downgradeShapes are the shapes that could make an address or parameter scan repeat
// itself.
var downgradeShapes = map[string]struct{ prefix, unit string }{
	"unclosed domain literals":        {"To: J\xf6 ", "["},
	"spaced unclosed domain literals": {"To: J\xf6 ", "[ "},
	"addresses in unclosed literals":  {"To: J\xf6 ", "a@[ ,"},
	"closed domain literals":          {"To: J\xf6 ", "[a]"},
	"unclosed quotes":                 {"To: J\xf6 ", `\"`},
	"unclosed angle addresses":        {"To: J\xf6 ", "< "},
	"unclosed comments":               {"To: J\xf6 ", "( "},
	"addresses":                       {"To: J\xf6 <a@example.com>, ", "a@example.com, "},
	"8-bit addresses":                 {"To: ", "J\xf6 <j\xf6@example.com>, "},
	"members of a group":              {"To: g:", "j\xf6@example.com, "},
	"folded address field":            {"To: J\xf6 <a@example.com>", "\r\n "},
	"parameters to quote":             {"Content-Type: text/plain; p=\"\xf6\"; ", "a=b c; "},
	"unbalanced parameters":           {"Content-Type: text/plain; p=\"\xf6\"; ", `a="; `},
	"repeated parameters":             {"Content-Type: text/plain; p=\"\xf6\"; ", "a=1; "},
	"comments in a parameter":         {"Content-Type: text/plain; p=\"\xf6\" ", "(x)"},
}

func downgradeShape(prefix, unit string, size int) string {
	return prefix + strings.Repeat(unit, size/len(unit))
}

// TestDowngradeHeaders_IsLinear proves a downgrade allocates in proportion to the
// header's size: about 4x on 4x the input, where building on all that came before would
// be about 16x. It counts bytes, not time, because time on a shared machine also measures
// what else is running: this conversion once read 10.5x against a limit of 9x by the clock
// and then passed unchanged. Scans that allocate nothing are held by
// TestAddressScan_LooksAtEachOctetOnce.
func TestDowngradeHeaders_IsLinear(t *testing.T) {
	const size, maxRatio = 32 << 10, 8
	for name, field := range downgradeShapes {
		t.Run(name, func(t *testing.T) {
			allocated := func(size int) uint64 {
				raw := downgradeShape(field.prefix, field.unit, size) + "\r\n\r\nbody\r\n"
				return bytesAllocated(func() { DowngradeHeaders(raw) })
			}
			small, big := allocated(size), allocated(4*size)
			if ratio := float64(big) / float64(max(small, 1)); ratio > maxRatio {
				t.Errorf("4x the input allocated %.1fx as much (%d, then %d bytes), want at most %dx", ratio, small, big, maxRatio)
			}
		})
	}
}

// TestAddressScan_LooksAtEachOctetOnce counts what the address scan looks at, whatever
// its input, since a scan repeating itself over an unclosed delimiter allocates nothing
// and takes all the time.
func TestAddressScan_LooksAtEachOctetOnce(t *testing.T) {
	const size = 128 << 10
	for name, field := range downgradeShapes {
		value, isAddress := strings.CutPrefix(downgradeShape(field.prefix, field.unit, size), "To:")
		if !isAddress {
			continue
		}
		t.Run(name, func(t *testing.T) {
			var scan addressScan
			scan.field(len("To:"), value)
			if limit := 2 * len(value); scan.steps > limit {
				t.Errorf("scan took %d steps over %d octets, want at most %d", scan.steps, len(value), limit)
			}
		})
	}
}

// TestAddressTokens_DomainLiteralsMatchRescanning holds the token split to a scan for
// ']' from each '[', over every short value of the octets that decide one.
func TestAddressTokens_DomainLiteralsMatchRescanning(t *testing.T) {
	atomEnd := func(value string) int {
		end := 0
		for end < len(value) && !isFoldingSpace(value[end]) && strings.IndexByte(`()"<,:;`, value[end]) < 0 {
			if value[end] == '[' {
				if closing := strings.IndexByte(value[end:], ']'); closing >= 0 {
					end += closing
				}
			}
			end++
		}
		return end
	}
	var visit func(prefix []byte)
	visit = func(prefix []byte) {
		value := string(prefix)
		var want []string
		for at := 0; at < len(value); {
			end := at + atomEnd(value[at:])
			if end == at {
				end++
				for isFoldingSpace(value[at]) && end < len(value) && isFoldingSpace(value[end]) {
					end++
				}
			}
			want = append(want, value[at:end])
			at = end
		}
		tokens, ok := addressTokens(value)
		if !ok {
			t.Fatalf("addressTokens(%q) did not read", value)
		}
		if len(tokens) != len(want) {
			t.Fatalf("addressTokens(%q) = %v, want %q", value, tokens, want)
		}
		for i, token := range tokens {
			if token.text != want[i] {
				t.Fatalf("addressTokens(%q) = %v, want %q", value, tokens, want)
			}
		}
		if len(prefix) == 8 {
			return
		}
		for _, c := range []byte("[]a ,:") {
			visit(append(prefix, c))
		}
	}
	visit(nil)
}
