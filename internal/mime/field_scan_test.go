package mime

import (
	"bufio"
	"bytes"
	"slices"
	"strings"
	"testing"
)

// colonScanShapes are fields whose colon comes late or never, read by a reader that skips the
// fields it doesn't want, as a part's header and the top of a stored message are.
var colonScanShapes = map[string]func(size int) string{
	"folds before any colon": func(size int) string { return "X" + strings.Repeat("\r\n x", size/4) + "\r\n\r\n" },
	"folds then a colon":     func(size int) string { return "X" + strings.Repeat("\r\n x", size/4) + ": y\r\n\r\n" },
	"a long line, no colon":  func(size int) string { return strings.Repeat("x", size) + "\r\n\r\n" },
	"empty folds":            func(size int) string { return "X" + strings.Repeat("\r\n ", size/3) + "\r\n\r\n" },
	"many fields":            func(size int) string { return strings.Repeat("X-A: b\r\n", size/8) + "\r\n" },
}

// TestFieldColonScan_LooksAtEachOctetOnce counts the octets looked at for a field's colon. A
// field folded N times before its colon was looked at whole after each fold: 1 MiB of them took
// 2.7 s to open a part. Looking again at what was looked at allocates nothing, so only a count
// can see it.
func TestFieldColonScan_LooksAtEachOctetOnce(t *testing.T) {
	for name, shape := range colonScanShapes {
		t.Run(name, func(t *testing.T) {
			for _, size := range []int{32 << 10, 128 << 10} {
				header := shape(size)
				r := &fieldReader{sourceSize: len(header), wanted: isPartField}
				_, _ = r.readPartFields(bufio.NewReader(strings.NewReader(header)))
				if r.colonSteps > len(header) {
					t.Errorf("%d octets: looked at %d for colons, want at most %d", len(header), r.colonSteps, len(header))
				}
			}
		})
	}
}

// TestFieldColonScan_AsksAboutTheNameBeforeTheFirstColon holds the skipping reader to the whole
// field: what it asks wanted about is what comes before the field's first colon, read with its
// folds, and the fields it reads end where a reader that keeps everything finds them ending. It
// reads through a 16-octet buffer, so a line arrives in pieces. Each header ends in a blank line, so
// no field is cut short by the end of the input.
func TestFieldColonScan_AsksAboutTheNameBeforeTheFirstColon(t *testing.T) {
	read := func(header string, wanted func([]byte) bool) (fields []string, err error) {
		r := &fieldReader{sourceSize: len(header), wanted: wanted}
		br := bufio.NewReaderSize(strings.NewReader(header), 16)
		for {
			field, err := r.readField(br)
			if len(field) == 0 {
				return fields, err
			}
			fields = append(fields, string(field))
		}
	}
	check := func(header string) {
		header += "\r\n\r\n"
		whole, wholeErr := read(header, nil)
		var want []string
		for _, field := range whole {
			if colon := strings.IndexByte(field, ':'); colon >= 0 {
				want = append(want, string(bytes.Trim([]byte(field[:colon]), " \t")))
			}
		}
		var asked []string
		skipped, skippedErr := read(header, func(key []byte) bool {
			asked = append(asked, string(key))
			return false
		})
		if !slices.Equal(asked, want) {
			t.Fatalf("%q: asked about %q, want %q", header, asked, want)
		}
		if len(skipped) != len(whole) || (skippedErr == nil) != (wholeErr == nil) {
			t.Fatalf("%q: skipping read %q (%v), keeping read %q (%v)", header, skipped, skippedErr, whole, wholeErr)
		}
		for i, field := range skipped {
			if colon := strings.IndexByte(whole[i], ':'); colon >= 0 && field != whole[i][:colon+1] {
				t.Fatalf("%q: skipping read field %d as %q, want %q", header, i, field, whole[i][:colon+1])
			}
		}
	}
	long := strings.Repeat("y", 40)
	var visit func(prefix []byte)
	visit = func(prefix []byte) {
		s := string(prefix)
		check(s)
		check(long + s)
		check("X" + s + long + ":" + s)
		if len(prefix) == 6 {
			return
		}
		for _, c := range []byte("x: \r\n\t") {
			visit(append(prefix, c))
		}
	}
	visit(nil)
}
