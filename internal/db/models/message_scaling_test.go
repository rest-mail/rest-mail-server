package models

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestMessage_BeforeCreate_IsLinearOnHostileHeaders proves storing a message costs
// no more than its size, since anyone who can send mail chooses these headers and
// BeforeCreate measures their wire form at delivery. A linear scan takes about 4x as
// long on 4x the input and a quadratic one about 16x. It compares two sizes instead
// of holding a time limit, so a slow machine doesn't fail it.
func TestMessage_BeforeCreate_IsLinearOnHostileHeaders(t *testing.T) {
	store := func(raw string) time.Duration {
		best := time.Duration(1<<63 - 1)
		for range 5 {
			runtime.GC()
			message := &Message{RawMessage: raw, RawSize: len(raw)}
			start := time.Now()
			if err := message.BeforeCreate(nil); err != nil {
				t.Fatalf("BeforeCreate: %v", err)
			}
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
		"escaped quotes in invalid UTF-8": {"Subject: \xf6 ", `\"`},
		"8-bit type":                      {"Content-Type: appl\xf6/pdf; a=", `\"`},
		"folded lines":                    {"Subject: \xf6", "\r\n x"},
		"folded unknown field":            {"X-Other: \xf6", "\r\n x"},
		"comments after a value":          {"Content-Type: text/plain; p=\"\xf6\" ", "(x)"},
	} {
		t.Run(name, func(t *testing.T) {
			message := func(size int) string {
				return field.prefix + strings.Repeat(field.unit, size/len(field.unit)) + "\r\n\r\nbody\r\n"
			}
			small, big := message(size), message(4*size)
			var ratio float64
			for range 3 {
				before, after := store(small), store(big)
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

// TestMessage_BeforeCreate_DoesNotCopyTheSource proves measuring a stored message costs no
// copy of it, whichever column holds the source.
func TestMessage_BeforeCreate_DoesNotCopyTheSource(t *testing.T) {
	raw := "Subject: big\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n\r\n" + strings.Repeat(strings.Repeat("A", 76)+"\r\n", 1<<20/78)
	for name, message := range map[string]*Message{
		"text":   {RawMessage: raw, RawSize: len(raw)},
		"octets": {RawOctets: []byte(raw), RawSize: len(raw)},
	} {
		t.Run(name, func(t *testing.T) {
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			if err := message.BeforeCreate(nil); err != nil {
				t.Fatal(err)
			}
			runtime.ReadMemStats(&after)
			if got := after.TotalAlloc - before.TotalAlloc; got > uint64(len(raw))/2 {
				t.Errorf("BeforeCreate of %d bytes allocated %d", len(raw), got)
			}
			if message.DowngradedSize != len(raw) || message.IMAPSize != len(raw) || message.IMAPUTF8Size != len(raw) {
				t.Errorf("sizes %d %d %d, want %d", message.DowngradedSize, message.IMAPSize, message.IMAPUTF8Size, len(raw))
			}
		})
	}
}
