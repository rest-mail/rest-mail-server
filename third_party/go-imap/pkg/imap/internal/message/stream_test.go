package message

import (
	"fmt"
	"strings"
	"testing"
)

var scanRaws = []string{
	"", "\r", "\r\n", "\r\n\r\n", "\r\n\r\nbody", "\n\n", "a\r\n\r\n", "a\r\n\r", "a\r\n\nb\r\n\r\nc",
	"Subject: x\r\n\r\nbody\r\n", "Subject: x\nY: z\n\nbody\n", "Subject: x\r\n\r\r\n\r\nb",
	"Subject: x\r\nY: \xc3\xa9\r\n\r\nb", "Subject: x\r\n\r\na\x00b", "\x00", "\xff",
	"Subject: only\r\n", "Subject: x\r\n\r\n\r\n\r\n",
}

// A Scan fed a message in pieces of any size finds the same header boundary
// and the same octet properties on each side of it as the whole-message path.
func TestScanAgreesAtEveryCut(t *testing.T) {
	for _, raw := range scanRaws {
		end, _ := headerEnd(raw)
		octets := func(part string) spanOctets {
			return spanOctets{high: has8bit(part), nul: strings.Contains(part, "\x00")}
		}
		header, body := octets(raw[:end]), octets(raw[end:])
		for step := 1; step <= max(len(raw), 1); step++ {
			s := NewScan()
			for i := 0; i < len(raw); i += step {
				_, _ = s.Write([]byte(raw[i:min(i+step, len(raw))]))
			}
			if s.HeaderEnd() != int64(end) || s.header != header || s.body != body ||
				s.NUL() != (header.nul || body.nul) || s.Size() != int64(len(raw)) {
				t.Errorf("%q in %d-octet pieces: end %d, header %+v, body %+v; want %d, %+v, %+v",
					raw, step, s.HeaderEnd(), s.header, s.body, end, header, body)
			}
		}
	}
}

// A span item names and sends what BodySection and BinarySection send for it, and
// an item they answer from a MIME parse is no span.
func TestSpanItemIsTheSection(t *testing.T) {
	items := []string{
		"BODY[]", "body.peek[]", "BODY[HEADER]", "BODY.PEEK[text]", "BODY[]<0.1>", "BODY[]<3.4>",
		"BODY.PEEK[TEXT]<1.2>", "BODY[HEADER]<0.100>", "BODY[]<4294967295.1>", "BODY[]<0.4294967295>",
		"BINARY[]", "BINARY.PEEK[]<2.3>", "BINARY.SIZE[]", "binary.peek[]",
	}
	for _, raw := range scanRaws {
		load := func() (string, bool) { return raw, true }
		end, _ := headerEnd(raw)
		// Counts one either side of the section's length, which partialOctets keeps or cuts.
		edges := []string{}
		for _, n := range []int{len(raw) - 1, len(raw), len(raw) + 1} {
			if n > 0 {
				edges = append(edges, fmt.Sprintf("BODY[]<0.%d>", n), fmt.Sprintf("BINARY.PEEK[]<1.%d>", n),
					fmt.Sprintf("BODY[TEXT]<0.%d>", n), fmt.Sprintf("BODY[]<%d.1>", n))
			}
		}
		for _, item := range append(items, edges...) {
			it, ok := ParseSpanItem(item)
			if !ok {
				t.Fatalf("%s is no span", item)
			}
			name, start, count := it.Range(int64(len(raw)), int64(end))
			got := raw[start : start+count]
			var wantName, want string
			var wantPeek bool
			if it.Binary {
				var size bool
				wantName, want, size, wantPeek, _, _ = BinarySection(item, load)
				if size != it.Size {
					t.Errorf("%s: size %v", item, it.Size)
				}
			} else {
				wantName, want, wantPeek, _ = BodySection(item, load)
			}
			if name != wantName || got != want || it.Peek != wantPeek {
				t.Errorf("%q %s: %s %q peek %v; want %s %q peek %v", raw, item, name, got, it.Peek, wantName, want, wantPeek)
			}
		}
	}
	for item, want := range map[string]SpanItem{
		"RFC822":        {name: "RFC822", kind: SpanWhole},
		"rfc822.header": {name: "RFC822.HEADER", kind: SpanHeader, Peek: true},
		"RFC822.TEXT":   {name: "RFC822.TEXT", kind: SpanText},
	} {
		if got, ok := ParseSpanItem(item); !ok || got != want {
			t.Errorf("%s: %+v %v", item, got, ok)
		}
	}
	for _, item := range []string{
		"BODY[1]", "BODY[HEADER.FIELDS (SUBJECT)]", "BODY[1.MIME]", "BINARY[1]", "BINARY.SIZE[1]",
		"BODY[]<1>", "BODY", "BODYSTRUCTURE", "ENVELOPE", "RFC822.SIZE", "FLAGS",
	} {
		if _, ok := ParseSpanItem(item); ok {
			t.Errorf("%s is a span", item)
		}
	}
}
