package mail

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestDowngradeHeaders_IsLinear proves a downgrade costs no more than the header's
// size, for each shape that could make an address or parameter scan repeat itself. A
// linear scan takes about 4x as long on 4x the input and a quadratic one about 16x. It
// compares two sizes instead of holding a time limit, so a slow machine doesn't fail it.
func TestDowngradeHeaders_IsLinear(t *testing.T) {
	fastest := func(raw string) time.Duration {
		best := time.Duration(1<<63 - 1)
		for range 5 {
			runtime.GC()
			start := time.Now()
			DowngradeHeaders(raw)
			took := time.Since(start)
			best = min(best, took)
			if took > time.Second {
				break
			}
		}
		return best
	}
	const size, maxRatio = 32 << 10, 9
	for name, field := range map[string]struct{ prefix, unit string }{
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
	} {
		t.Run(name, func(t *testing.T) {
			message := func(size int) string {
				return field.prefix + strings.Repeat(field.unit, size/len(field.unit)) + "\r\n\r\nbody\r\n"
			}
			small, big := message(size), message(4*size)
			var ratio float64
			for range 3 {
				before, after := fastest(small), fastest(big)
				ratio = float64(after) / float64(max(before, time.Microsecond))
				if ratio <= maxRatio || after > 2*time.Second {
					break
				}
			}
			if ratio > maxRatio {
				t.Errorf("4x the input took %.1fx as long, want at most %dx", ratio, maxRatio)
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
