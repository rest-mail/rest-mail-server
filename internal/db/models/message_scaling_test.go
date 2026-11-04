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
