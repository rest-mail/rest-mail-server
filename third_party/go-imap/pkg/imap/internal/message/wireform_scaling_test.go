package message

import (
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// assertLinear fails when work costs quadratically more on four times the input: a
// linear scan takes about 4x as long and a quadratic one about 16x. It compares two
// sizes instead of holding a time limit, so a slow machine doesn't fail it, and a
// comparison spoiled by a pause elsewhere gets another try.
func assertLinear(t *testing.T, input func(size int) string, work func(string)) {
	t.Helper()
	const size, maxRatio = 32 << 10, 9
	small, big := input(size), input(4*size)
	var ratio float64
	for range 3 {
		before, after := fastest(small, work), fastest(big, work)
		ratio = float64(after) / float64(max(before, time.Microsecond))
		if ratio <= maxRatio || after > 2*time.Second {
			break
		}
	}
	if ratio > maxRatio {
		t.Errorf("4x the input took %.1fx as long, want at most %dx", ratio, maxRatio)
	}
}

// fastest is the best of a few runs, the one least disturbed by anything else. A run
// that is already slow isn't repeated, so a regression fails in seconds, not minutes.
func fastest(input string, work func(string)) time.Duration {
	best := time.Duration(1<<63 - 1)
	for range 5 {
		runtime.GC()
		start := time.Now()
		work(input)
		took := time.Since(start)
		best = min(best, took)
		if took > time.Second {
			break
		}
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
