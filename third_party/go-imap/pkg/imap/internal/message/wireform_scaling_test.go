package message

import (
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// assertLinear fails when work allocates quadratically more on four times the input:
// about 4x when it builds in proportion, about 16x when it builds on all that came
// before. It counts bytes, not time, because time on a shared machine also measures
// what else is running. This conversion once read 25.6x against a limit of 9x by the
// clock while it executed exactly twice the statements per doubling. A scan that
// repeats itself allocates nothing, so TestEncodeHeaderValue_ScansInProportion counts
// those steps.
func assertLinear(t *testing.T, input func(size int) string, work func(string)) {
	t.Helper()
	const size, maxRatio = 32 << 10, 8
	before, after := bytesAllocated(input(size), work), bytesAllocated(input(4*size), work)
	if ratio := float64(after) / float64(max(before, 1)); ratio > maxRatio {
		t.Errorf("4x the input allocated %.1fx as much (%d, then %d bytes), want at most %dx", ratio, before, after, maxRatio)
	}
}

// bytesAllocated is what one run allocates, the least of a few runs so that anything
// else the process allocates in between doesn't count, and neither does filling a pool.
func bytesAllocated(input string, work func(string)) uint64 {
	var before, after runtime.MemStats
	best := ^uint64(0)
	for range 3 {
		runtime.ReadMemStats(&before)
		work(input)
		runtime.ReadMemStats(&after)
		best = min(best, after.TotalAlloc-before.TotalAlloc)
	}
	return best
}

func repeated(unit string, size int) string {
	return strings.Repeat(unit, size/len(unit))
}

// TestWireForm_HeaderConversionIsLinear covers each shape that makes a header scan
// repeat itself: an opening delimiter that never closes, a field folded many times,
// and parameters or comments in great number.
func TestWireForm_HeaderConversionIsLinear(t *testing.T) {
	field := func(prefix, unit string) func(int) string {
		return func(size int) string { return prefix + repeated(unit, size) + "\r\n\r\nbody\r\n" }
	}
	cases := map[string]func(int) string{
		"escaped quotes":         field("Subject: \xf6 ", `\"`),
		"spaced escaped quotes":  field("Subject: \xf6 ", `\" `),
		"8-bit type":             field("Content-Type: appl\xf6/pdf; a=", `\"`),
		"8-bit quoted parameter": field("Content-Type: text/plain; a=\"\xf6", `\"`),
		"unclosed comments":      field("Subject: \xf6 ", `(`),
		"unclosed angle":         field("To: J\xf6 ", `<`),
		"unclosed encoded words": field("Subject: \xf6 ", `=?UTF-8?Q?`),
		"backslashes":            field("Subject: \xf6 ", `\`),
		"folded lines":           field("Subject: \xf6", "\r\n x"),
		"folded escaped quotes":  field("Subject: \xf6", "\r\n \\\""),
		"repeated parameters":    field("Content-Type: text/plain; p=\"\xf6\"; ", "a=1; "),
		"comments after a value": field("Content-Type: text/plain; p=\"\xf6\" ", "(x)"),
		"many fields":            field("", "Subject: \xf6 \\\"\r\n"),
		"folded transfer encoding": func(size int) string {
			return "Content-Type: application/pdf\r\nContent-Transfer-Encoding: x" + repeated("\r\n y", size) + "\r\n\r\n\xf6\r\n"
		},
		"folded field beside an encoded body": func(size int) string {
			return "Content-Type: application/pdf\r\nX-Other: a" + repeated("\r\n y", size) + "\r\n\r\n\xf6\r\n"
		},
		"many parts": func(size int) string {
			return "Content-Type: multipart/mixed; boundary=b\r\n\r\n" + repeated("--b\r\nSubject: \xf6\r\n\r\n\xf6\r\n", size) + "--b--\r\n"
		},
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			assertLinear(t, input, func(raw string) { WireForm(raw, false) })
		})
	}
}

// sevenBitBoundaryHead and sevenBitBoundaryUnits are what POP3 adds to a downgrade: many
// delimiter lines, lines that only look like one, lines holding the new spelling, bare
// LFs, and delimiters with no line to stand on.
const sevenBitBoundaryHead = "Content-Type: multipart/mixed; boundary=\"\xf6\"\r\n\r\n"

var sevenBitBoundaryUnits = map[string]string{
	"delimiters":      "--\xf6\r\n\r\nx\r\n",
	"near misses":     "--\xf6x\r\n",
	"new spelling":    "--=F6\r\n",
	"bare line feeds": "--\xf6\nx\n",
	"no line breaks":  "--\xf6",
}

func TestSevenBitBoundary_IsLinear(t *testing.T) {
	for name, unit := range sevenBitBoundaryUnits {
		t.Run(name, func(t *testing.T) {
			assertLinear(t, func(size int) string { return sevenBitBoundaryHead + repeated(unit, size) }, func(raw string) { SevenBitBoundary(raw) })
		})
	}
}

// TestSevenBitBoundary_ScansInProportion counts the octets a rewrite looks at. Searching
// the body again from each delimiter would allocate nothing and look at all of it each
// time, which assertLinear can't see.
func TestSevenBitBoundary_ScansInProportion(t *testing.T) {
	const size = 32 << 10
	for name, unit := range sevenBitBoundaryUnits {
		t.Run(name, func(t *testing.T) {
			raw := sevenBitBoundaryHead + repeated(unit, size)
			var scan boundaryScan
			scan.sevenBitBoundary(raw)
			if low, limit := len(raw)/2, 6*len(raw); scan.steps < low || scan.steps > limit {
				t.Errorf("rewrite took %d steps over %d octets, want %d to %d", scan.steps, len(raw), low, limit)
			}
		})
	}
}

// TestParameterWithoutFirstSegment_IsLinear: laterSegments sorts each parameter's
// segments once, and looks at nothing else.
func TestParameterWithoutFirstSegment_IsLinear(t *testing.T) {
	assertLinear(t, func(size int) string {
		var b strings.Builder
		b.WriteString("Content-Type: application/pdf; a=\xf6")
		for n := 1; b.Len() < size; n++ {
			b.WriteString("; name*" + strconv.Itoa(n) + "=x")
		}
		return b.String() + "\r\n\r\nbody\r\n"
	}, func(raw string) { WireForm(raw, false) })
}

// TestParameterField_IsLinear covers what a downgrade reaches without WireForm.
func TestParameterField_IsLinear(t *testing.T) {
	assertLinear(t, func(size int) string {
		return `text/plain; p="` + "\xf6" + `" ` + repeated("(x)", size)
	}, func(value string) {
		if _, ok := ParameterField("Content-Type", value); !ok {
			t.Fatal("parameters not rewritten")
		}
	})
}

// TestEncodeHeaderValue_ScansInProportion counts the octets a conversion looks at. A
// token is read a few times, and an unclosed quote is scanned for its end once; scanning
// for it from every '"' would look at the rest of the value each time.
func TestEncodeHeaderValue_ScansInProportion(t *testing.T) {
	const size = 32 << 10
	for name, unit := range map[string]string{
		"escaped quotes":        `\"`,
		"spaced escaped quotes": `\" `,
		"unclosed quotes":       `" \"`,
		"closed quotes":         `"a b" `,
		"a quote per token":     `a" `,
		"8-bit escaped quotes":  "\xf6\\\" ",
		"folded escaped quotes": "\r\n \\\"",
		"backslashes":           `\`,
		"8-bit words":           "\xf6 ",
	} {
		t.Run(name, func(t *testing.T) {
			value := " \xf6 " + repeated(unit, size)
			tokens := headerTokens{value: value, unclosed: len(value)}
			var sink wireCounter
			encodeHeaderTokens(&sink, len("Subject:"), &tokens)
			if limit := 4 * len(value); tokens.steps > limit {
				t.Errorf("scan took %d steps over %d octets, want at most %d", tokens.steps, len(value), limit)
			}
		})
	}
}

// rescanToken is headerToken as it was before it remembered an unclosed quote: every
// '"' scans the rest of the value. It states what headerTokens has to return.
func rescanToken(s string) string {
	i := 0
	for i < len(s) && !isHeaderSpace(s[i]) {
		if s[i] == '"' {
			for j := i + 1; j < len(s); j++ {
				if s[j] == '\\' {
					j++
				} else if s[j] == '"' {
					i = j
					break
				}
			}
		}
		i++
	}
	return s[:i]
}

// everyString calls visit with every string of up to length octets from alphabet.
func everyString(alphabet string, length int, visit func(string)) {
	var build func(prefix []byte)
	build = func(prefix []byte) {
		visit(string(prefix))
		if len(prefix) == length {
			return
		}
		for i := range len(alphabet) {
			build(append(prefix, alphabet[i]))
		}
	}
	build(nil)
}

// TestHeaderTokens_MatchRescanning proves remembering an unclosed quote changes no
// token, over every short value of the octets that decide one, and read the way
// encodeHeaderValue reads: in order, from each token and each offset after it.
func TestHeaderTokens_MatchRescanning(t *testing.T) {
	everyString(`"\ x`, 9, func(value string) {
		tokens := headerTokens{value: value, unclosed: len(value)}
		for at := 0; at < len(value); at++ {
			if isHeaderSpace(value[at]) {
				continue
			}
			want := rescanToken(value[at:])
			if got := tokens.at(at); got != want {
				t.Fatalf("token at %d of %q = %q, want %q", at, value, got, want)
			}
			if got := tokens.at(at); got != want {
				t.Fatalf("second read at %d of %q = %q, want %q", at, value, got, want)
			}
		}
	})
}

// TestHeaderBlockFields_MatchJoiningLines holds the slicing form of headerBlockFields
// and HeaderFieldValues to what each line joined onto the last would give.
func TestHeaderBlockFields_MatchJoiningLines(t *testing.T) {
	joinLines := func(header string) []string {
		var fields []string
		for len(header) > 0 {
			end := len(header)
			if i := strings.Index(header, "\r\n"); i >= 0 {
				end = i + 2
			}
			line := header[:end]
			if len(fields) > 0 && (line[0] == ' ' || line[0] == '\t') {
				fields[len(fields)-1] += line
			} else {
				fields = append(fields, line)
			}
			header = header[end:]
		}
		return fields
	}
	everyString("\r\n x\t", 7, func(header string) {
		if got, want := headerBlockFields(header), joinLines(header); !reflect.DeepEqual(got, want) {
			t.Fatalf("headerBlockFields(%q) = %q, want %q", header, got, want)
		}
	})

	for raw, want := range map[string][]string{
		"A: 1\r\n\r\n":                    {" 1"},
		"A: 1\r\n 2\r\n\t3\r\nB: x\r\n":   {" 1 2\t3"},
		"A: 1\r\nB: x\r\nA: 2\r\n 3\r\n":  {" 1", " 2 3"},
		" 0\r\nA: 1\r\n":                  {" 1"},
		"B: x\r\n\r\nA: not a header\r\n": nil,
		"":                                nil,
	} {
		if got := HeaderFieldValues(raw, "a"); !reflect.DeepEqual(got, want) {
			t.Errorf("HeaderFieldValues(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestWireFormSize_MatchesAndIsLinear counts what WireForm sends, on the shapes that
// repeat a scan, and costs no more than it does.
func TestWireFormSize_MatchesAndIsLinear(t *testing.T) {
	cases := map[string]string{
		"escaped quotes": "Subject: \xf6 " + repeated(`\"`, 8<<10) + "\r\n\r\nbody\r\n",
		"folded lines":   "Subject: \xf6" + repeated("\r\n x", 8<<10) + "\r\n\r\nbody\r\n",
		"many fields":    repeated("Subject: \xf6 \\\"\r\n", 8<<10) + "\r\n",
		"many parts":     "Content-Type: multipart/mixed; boundary=b\r\n\r\n" + repeated("--b\r\nSubject: \xf6\r\n\r\n\xf6\r\n", 8<<10) + "--b--\r\n",
		"binary":         "Content-Type: application/pdf\r\nContent-Transfer-Encoding: binary\r\n\r\n" + repeated("\xf6", 8<<10) + "\r\n",
		"text":           "Content-Type: text/plain\r\n\r\n" + repeated("\xf6 \r\n=\r", 8<<10),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			for _, sessionUTF8 := range []bool{false, true} {
				if got, want := WireFormSize(raw, sessionUTF8), len(WireForm(raw, sessionUTF8)); got != want {
					t.Fatalf("utf8=%v: counted %d, WireForm sent %d", sessionUTF8, got, want)
				}
			}
		})
	}
	input := func(size int) string {
		return "Content-Type: multipart/mixed; boundary=b\r\n\r\n" + repeated("--b\r\nSubject: \xf6\r\n\r\n\xf6\r\n", size) + "--b--\r\n"
	}
	assertLinear(t, input, func(raw string) { WireFormSize(raw, false) })
}

// TestEntityHeader_SharesItsBuffer: net/mail allocates a 4 KiB buffer per read unless
// it is given one, which a message of many small parts would pay for each part.
func TestEntityHeader_SharesItsBuffer(t *testing.T) {
	const reads, wholeBuffer = 200, 4096
	entityHeader("Content-Type: text/plain\r\n\r\n")
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range reads {
		if entityHeader("Content-Type: text/plain\r\n\r\n").Get("Content-Type") != "text/plain" {
			t.Fatal("header not read")
		}
	}
	runtime.ReadMemStats(&after)
	if each := (after.TotalAlloc - before.TotalAlloc) / reads; each >= wholeBuffer*3/4 {
		t.Errorf("each read allocated %d bytes, want well under the %d of a buffer", each, wholeBuffer)
	}
}

// TestNULReplacement_HoldsNoCopy: a space for a NUL is written where the NUL was, so
// measuring text full of them holds nothing that grows with it, and SpaceNUL builds the
// message once (#1437).
func TestNULReplacement_HoldsNoCopy(t *testing.T) {
	const size = 1 << 20
	text := "Content-Type: text/plain; charset=utf-8\r\n\r\n" + repeated("a\x00", size) + "\r\n"
	if got := bytesAllocated(text, func(raw string) { WireFormSize(raw, false) }); got > 64<<10 {
		t.Errorf("measuring %d bytes of text allocated %d", len(text), got)
	}
	if got := bytesAllocated(text, func(raw string) { SpaceNUL(raw) }); got > uint64(len(text))+64<<10 {
		t.Errorf("SpaceNUL of %d bytes allocated %d", len(text), got)
	}
	data := "Content-Type: application/pdf\r\nContent-Transfer-Encoding: binary\r\n\r\n" + repeated("a\x00", size) + "\r\n"
	if got := bytesAllocated(data, func(raw string) { WireFormSize(raw, false) }); got > 64<<10 {
		t.Errorf("measuring %d bytes of data allocated %d", len(data), got)
	}
	if got := bytesAllocated(data, func(raw string) { SpaceNUL(raw) }); got > uint64(len(data))+64<<10 {
		t.Errorf("SpaceNUL of %d bytes of data allocated %d", len(data), got)
	}
}

// TestNULReplacement_IsLinear covers the shapes that go through a copy of a field: an
// 8-bit field holding NULs, many of them, and many parts.
func TestNULReplacement_IsLinear(t *testing.T) {
	field := func(prefix, unit string) func(int) string {
		return func(size int) string { return prefix + repeated(unit, size) + "\r\n\r\nbody\r\n" }
	}
	cases := map[string]func(int) string{
		"8-bit field":       field("Subject: \xf6 ", "\x00"),
		"7-bit field":       field("Subject: ", "\x00"),
		"many fields":       field("", "Subject: \xf6\x00\r\n"),
		"8-bit parameter":   field("Content-Type: text/plain; a=\"\xf6", "\x00"),
		"encoded body type": field("Content-Type: application/pdf\r\nX-Other: \x00", "\r\n y\x00"),
		"many parts": func(size int) string {
			return "Content-Type: multipart/mixed; boundary=b\r\n\r\n" + repeated("--b\r\nSubject: \x00\r\n\r\n\x00\r\n", size) + "--b--\r\n"
		},
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			assertLinear(t, input, func(raw string) { WireForm(raw, false) })
			assertLinear(t, input, func(raw string) { SpaceNUL(raw) })
		})
	}
}
