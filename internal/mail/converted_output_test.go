package mail

import (
	"strings"
	"testing"
)

// convertedOutputShapes are the headers a sender can write to make a conversion send the
// most octets for the fewest it stored: many short fields of 8-bit octets, which each
// become an encoded word of their own, beside the hostile shapes the scaling tests use.
func convertedOutputShapes(size int) map[string]string {
	shapes := map[string]string{}
	for name, field := range downgradeShapes {
		shapes[name] = downgradeShape(field.prefix, field.unit, size) + "\r\n\r\nbody\r\n"
	}
	for name, raw := range hostileHeaderShapes(size) {
		shapes[name] = raw
	}
	for name, unit := range map[string]string{
		"8-bit field names with values": "\xf6:\xf6\r\n",
		"8-bit field names alone":       "\xf6:\r\n",
		"lines of one 8-bit octet":      "\xf6\r\n",
		"short fields of 8-bit octets":  "S:\xf6\r\n",
		"short fields of UTF-8":         "S:\u00f6\r\n",
		"short fields with a control":   "T:\x01\xf6\r\n",
	} {
		shapes[name] = strings.Repeat(unit, size/len(unit)) + "\r\nbody\r\n"
	}
	return shapes
}

// TestConvertedSizes_AreBoundedByTheInput proves what a message costs to send converted:
// the octets of FETCH, RETR and the size each reports are at most maxFactor times the
// octets stored, and four times the input is at most about four times the output, so
// no sender makes a few octets stored cost many sent, or sizing more than linear.
// The widest shape found, 8-bit field names with values over IMAP, reads 16.7x; the
// limit is a ceiling over what was measured and is not to be raised.
func TestConvertedSizes_AreBoundedByTheInput(t *testing.T) {
	const size, maxFactor, maxGrowth = 32 << 10, 20, 4.5
	sizes := func(raw string) map[string]int {
		counted := MeasureWireSizes(raw)
		return map[string]int{
			"IMAP":          counted.IMAP,
			"IMAP UTF8":     counted.IMAPUTF8,
			"POP3":          counted.Downgraded,
			"POP3 UTF8":     counted.POP3UTF8,
			"DowngradeHead": len(DowngradeHeaders(raw)),
		}
	}
	small, big := convertedOutputShapes(size), convertedOutputShapes(4*size)
	for name, raw := range small {
		t.Run(name, func(t *testing.T) {
			before, after := sizes(raw), sizes(big[name])
			for kind, got := range before {
				if limit := maxFactor * len(raw); got > limit {
					t.Errorf("%s sends %d octets for %d stored (%.1fx), want at most %dx", kind, got, len(raw), float64(got)/float64(len(raw)), maxFactor)
				}
				if growth := float64(after[kind]) / float64(max(got, 1)); growth > maxGrowth*float64(len(big[name]))/float64(4*len(raw)) {
					t.Errorf("%s: %d times the input sent %.1fx the octets (%d, then %d), want at most %.1fx", kind, 4, growth, got, after[kind], maxGrowth)
				}
			}
		})
	}
}
