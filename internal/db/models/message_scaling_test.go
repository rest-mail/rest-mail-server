package models

import (
	"runtime"
	"strings"
	"testing"
)

// TestMessage_BeforeCreate_IsLinearOnHostileHeaders proves storing a message costs
// no more than its size, since anyone who can send mail chooses these headers and
// BeforeCreate measures their wire form at delivery. A store that builds in proportion
// allocates about 4x as much on 4x the input, and one that joins each piece onto all
// before it about 16x. It counts bytes, not time, because time on a shared machine
// also measures what else is running. A scan that repeats itself allocates nothing, so
// TestEncodeHeaderValue_ScansInProportion in go-imap counts those steps.
func TestMessage_BeforeCreate_IsLinearOnHostileHeaders(t *testing.T) {
	store := func(raw string) uint64 {
		var before, after runtime.MemStats
		best := ^uint64(0)
		for range 3 {
			message := &Message{RawMessage: raw, RawSize: len(raw)}
			runtime.ReadMemStats(&before)
			if err := message.BeforeCreate(nil); err != nil {
				t.Fatalf("BeforeCreate: %v", err)
			}
			runtime.ReadMemStats(&after)
			best = min(best, after.TotalAlloc-before.TotalAlloc)
		}
		return best
	}
	const size, maxRatio = 32 << 10, 8
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
			before, after := store(message(size)), store(message(4*size))
			ratio := float64(after) / float64(max(before, 1))
			if ratio > maxRatio {
				t.Errorf("4x the input allocated %.1fx as much (%d, then %d bytes), want at most %dx", ratio, before, after, maxRatio)
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
