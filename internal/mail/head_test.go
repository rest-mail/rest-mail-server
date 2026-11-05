package mail

import (
	"math/rand/v2"
	netmail "net/mail"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestRemoveHeadField(t *testing.T) {
	const multipartCRLF = "Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nBcc: in a part\r\n\r\ntext\r\n--b--\r\n"
	const multipartLF = "Content-Type: multipart/mixed; boundary=b\n\n--b\nBcc: in a part\n\ntext\n--b--\n"
	for _, tc := range []struct{ name, raw, want string }{
		{"all CRLF", "A: 1\r\nBcc: s\r\nB: 2\r\n\r\nbody\r\n", "A: 1\r\nB: 2\r\n\r\nbody\r\n"},
		{"all LF", "A: 1\nBcc: s\nB: 2\n\nbody\n", "A: 1\nB: 2\n\nbody\n"},
		{"all CR", "A: 1\rBcc: s\rB: 2\r\rbody\r", "A: 1\rB: 2\r\rbody\r"},
		{"LF head, CRLF blank later", "A: 1\nBcc: s\nB: 2\n\nbody\n\r\n\r\nmore", "A: 1\nB: 2\n\nbody\n\r\n\r\nmore"},
		{"one LF in the head, after the field", "A: 1\r\nBcc: s\nB: 2\r\n\r\nbody", "A: 1\r\nB: 2\r\n\r\nbody"},
		{"one LF in the head, before the field", "A: 1\nBcc: s\r\nB: 2\r\n\r\nbody", "A: 1\nB: 2\r\n\r\nbody"},
		{"bare CR in the head, before the field", "A: 1\rBcc: s\r\nB: 2\r\n\r\nbody", "A: 1\rB: 2\r\n\r\nbody"},
		{"bare CR in the head, after the field", "A: 1\r\nBcc: s\rB: 2\r\n\r\nbody", "A: 1\r\nB: 2\r\n\r\nbody"},
		{"CR CR LF ends the line", "A: 1\r\r\nBcc: s\r\r\nB: 2\r\n\r\nbody", "A: 1\r\r\nB: 2\r\n\r\nbody"},
		{"first field", "Bcc: s\r\nA: 1\r\n\r\nbody", "A: 1\r\n\r\nbody"},
		{"first field, after a space", " Bcc: s\r\nA: 1\r\n\r\nbody", "A: 1\r\n\r\nbody"},
		{"last field", "A: 1\r\nBcc: s\r\n\r\nbody", "A: 1\r\n\r\nbody"},
		{"last field, LF", "A: 1\nBcc: s\n\nbody", "A: 1\n\nbody"},
		{"last line of a message with no body", "A: 1\r\nBcc: s", "A: 1\r\n"},
		{"the only field", "Bcc: s\r\n\r\nbody", "\r\nbody"},
		{"folded", "A: 1\r\nBcc: s,\r\n t,\r\n\tu\r\nB: 2\r\n\r\nbody", "A: 1\r\nB: 2\r\n\r\nbody"},
		{"folded with mixed line ends", "A: 1\r\nBcc: s,\n t,\r u\r\nB: 2\r\n\r\nbody", "A: 1\r\nB: 2\r\n\r\nbody"},
		{"a folded line with only space", "Bcc: s\n \nA: 1\n\nbody", "A: 1\n\nbody"},
		{"several", "Bcc: s\r\nA: 1\nbcc: t\rBCC: u\r\nB: 2\r\n\r\nbody", "A: 1\nB: 2\r\n\r\nbody"},
		{"any case", "bCc: s\n\nbody", "\nbody"},
		{"space before the colon", "A: 1\r\nBcc \t: s\r\n\r\nbody", "A: 1\r\n\r\nbody"},
		{"a line that is no field doesn't end the head", "A: 1\nnot a field\nBcc: s\n\nbody", "A: 1\nnot a field\n\nbody"},
		{"a line with only space doesn't end the head", "A: 1\n \nBcc: s\n\nbody", "A: 1\n \n\nbody"},

		{"no Bcc, mixed", "A: 1\nB: 2\r\nC: 3\r\r\nbody\n", "A: 1\nB: 2\r\nC: 3\r\r\nbody\n"},
		{"another field with the word", "Bcc-Other: s\r\nX-Bcc: t\r\nSubject: Bcc: u\r\n\r\nbody", "Bcc-Other: s\r\nX-Bcc: t\r\nSubject: Bcc: u\r\n\r\nbody"},
		{"folded under another field", "A: 1\r\n Bcc: s\r\n\r\nbody", "A: 1\r\n Bcc: s\r\n\r\nbody"},
		{"in the body, CRLF", "A: 1\r\n\r\nBcc: body\r\n", "A: 1\r\n\r\nBcc: body\r\n"},
		{"in the body, LF", "A: 1\n\nBcc: body\n", "A: 1\n\nBcc: body\n"},
		{"in the body, after an LF head and before a CRLF blank", "A: 1\nBcc: s\n\nBcc: body\n\r\n\r\nmore", "A: 1\n\nBcc: body\n\r\n\r\nmore"},
		{"a part's head, CRLF", "Bcc: s\r\n" + multipartCRLF, multipartCRLF},
		{"a part's head, LF", "Bcc: s\n" + multipartLF, multipartLF},

		// The readings disagree: each of these has a Bcc for a reader that keeps to its line ends.
		{"CRLF reader, past an LF blank", "A: 1\n\nbody\r\nBcc: s\r\n\r\nmore", "A: 1\n\nbody\r\n\r\nmore"},
		{"LF reader, past a CR blank", "A: 1\r\rbody\nBcc: s\n\nmore", "A: 1\r\rbody\n\nmore"},
		{"textproto keeps reading past CR CR LF", "A: 1\n\r\r\nX: 2\nBcc: s\n\nbody", "A: 1\n\r\r\nX: 2\n\nbody"},

		// Taking a field out must not bring another into view.
		{"the CR before and the LF after meet", "A: 1\rBcc: s\n\nBcc: t\n\nbody", "A: 1\r\n\nbody"},
		{"the CR before and the CR LF after meet", "A: 1\rBcc: s\n\r\nBcc: t\r\n\r\nbody", "A: 1\r\r\n\r\nbody"},
		{"a line end left at the start of a line", "A: 1\r\rB: 2\nBcc: s\rBcc: t\n\nbody", "A: 1\r\rB: 2\n\nbody"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RemoveHeadField(tc.raw, "Bcc"); got != tc.want {
				t.Errorf("RemoveHeadField(%q)\n = %q\nwant %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestHeadFieldsReadEveryMix(t *testing.T) {
	const field = "X-Mark: yes,\n\tfolded"
	for _, tc := range []struct {
		name, raw string
		found     bool
	}{
		{"CRLF", "A: 1\r\n" + field + "\r\nB: 2\r\n\r\nbody", true},
		{"LF", "A: 1\n" + field + "\nB: 2\n\nbody", true},
		{"CR", "A: 1\r" + field + "\rB: 2\r\rbody", true},
		{"LF head, CRLF blank later", "A: 1\n" + field + "\n\nbody\n\r\n\r\nmore", true},
		{"one LF", "A: 1\r\n" + field + "\nB: 2\r\n\r\nbody", true},
		{"bare CR", "A: 1\r" + field + "\r\nB: 2\r\n\r\nbody", true},
		{"past an LF blank, for a CRLF reader", "A: 1\n\nbody\r\n" + field + "\r\n\r\nmore", true},
		{"only in the body", "A: 1\r\n\r\n" + field + "\r\n", false},
		{"only in the body, LF", "A: 1\n\n" + field + "\n", false},
		{"folded under another field", "A: 1\n X-Mark: yes\n\nbody", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			found := false
			for f := range HeadFields(tc.raw) {
				if f.Is("x-mark") {
					found = true
					if got := f.Value(); tc.found && got != "yes,\tfolded" {
						t.Errorf("Value() = %q", got)
					}
				}
			}
			if found != tc.found {
				t.Errorf("found %v, want %v in %q", found, tc.found, tc.raw)
			}
		})
	}
}

func TestHeadFieldsStopWhenAsked(t *testing.T) {
	seen := 0
	for range HeadFields("A: 1\nB: 2\nC: 3\n\nbody") {
		seen++
		break
	}
	if seen != 1 {
		t.Fatalf("read %d fields, want 1", seen)
	}
}

// With nothing to remove the message itself comes back, which is what keeps a signed message and
// the memory rules (#1437) safe: no copy is made.
func TestRemoveHeadFieldMakesNoCopyWithNothingToRemove(t *testing.T) {
	raw := strings.Repeat("A: 1\n", 100) + "\n" + strings.Repeat("body\r\n", 100)
	if allocs := testing.AllocsPerRun(10, func() { _ = RemoveHeadField(raw, "Bcc") }); allocs > 2 {
		t.Fatalf("%v allocations, want no copy of the message", allocs)
	}
}

// bccReaders are the ways of reading a head a message can meet downstream, written here apart from
// HeadFields so that the check doesn't test the reader against itself. Each says whether it finds a
// Bcc field.
var bccReaders = map[string]func(string) bool{
	"CRLF alone": func(s string) bool { return splitFindsBcc(strings.Split(s, "\r\n")) },
	"LF, one CR dropped": func(s string) bool {
		lines := strings.Split(s, "\n")
		for i := range lines {
			lines[i] = strings.TrimSuffix(lines[i], "\r")
		}
		return splitFindsBcc(lines)
	},
	"CR, LF, or a run of CRs and an LF": func(s string) bool { return splitFindsBcc(anyLineEnd.Split(s, -1)) },
	"each of CRLF, CR, LF":              func(s string) bool { return splitFindsBcc(eachLineEnd.Split(s, -1)) },
	"net/mail": func(s string) bool {
		msg, err := netmail.ReadMessage(strings.NewReader(s))
		return err == nil && len(msg.Header["Bcc"]) > 0
	},
}

var (
	anyLineEnd  = regexp.MustCompile(`\r*\n|\r`)
	eachLineEnd = regexp.MustCompile(`\r\n|\r|\n`)
)

func splitFindsBcc(lines []string) bool {
	for i, line := range lines {
		if line == "" {
			return false
		}
		if i > 0 && (line[0] == ' ' || line[0] == '\t') {
			continue
		}
		if name, _, found := strings.Cut(line, ":"); found && strings.EqualFold(strings.Trim(name, " \t"), "Bcc") {
			return true
		}
	}
	return false
}

// mutatedHead builds a message from field lines, folds, empty lines and body lines, with a line end
// chosen for each. Lines that must survive hold "keep" and a number, returned in keep.
func mutatedHead(r *rand.Rand) (raw string, keep []string) {
	ends := []string{"\r\n", "\r\n", "\n", "\n", "\r", "\r\r\n", "\n\r"}
	var b strings.Builder
	for i, n := 0, 1+r.IntN(9); i < n; i++ {
		line := ""
		switch r.IntN(10) {
		case 0, 1, 2:
			line = []string{"Bcc: s", "bcc:s", "BCC : s", "Bcc:", "bCc\t: s"}[r.IntN(5)]
		case 3:
			line = []string{" fold", "\tfold", " "}[r.IntN(3)]
		case 4, 5:
			// An empty line.
		default:
			line = "X-" + strconv.Itoa(i) + ": keep" + strconv.Itoa(i)
			keep = append(keep, "keep"+strconv.Itoa(i))
		}
		b.WriteString(line)
		if i < n-1 || r.IntN(4) > 0 {
			b.WriteString(ends[r.IntN(len(ends))])
		}
	}
	return b.String(), keep
}

// Whatever the line ends, no reader finds a Bcc in what RemoveHeadField leaves, nothing else goes,
// and a message no reader finds a Bcc in comes back as it was.
func TestNoReaderFindsABccAfterRemoval(t *testing.T) {
	r := rand.New(rand.NewPCG(1970, 1))
	for range 200_000 {
		raw, keep := mutatedHead(r)
		got := RemoveHeadField(raw, "Bcc")
		foundInRaw := false
		for name, finds := range bccReaders {
			foundInRaw = foundInRaw || finds(raw)
			if finds(got) {
				t.Fatalf("the %s reader finds a Bcc\n  in %q\nfrom %q", name, got, raw)
			}
		}
		if !foundInRaw && got != raw {
			t.Fatalf("no reader finds a Bcc in %q, and it became %q", raw, got)
		}
		for _, line := range keep {
			if !strings.Contains(got, line) {
				t.Fatalf("%q was lost from %q: %q", line, raw, got)
			}
		}
		if !isSubsequence(got, raw) {
			t.Fatalf("%q isn't %q with lines taken out", got, raw)
		}
		if again := RemoveHeadField(got, "Bcc"); again != got {
			t.Fatalf("removing twice changed %q to %q, from %q", got, again, raw)
		}
	}
}

func isSubsequence(short, long string) bool {
	i := 0
	for j := 0; i < len(short) && j < len(long); j++ {
		if short[i] == long[j] {
			i++
		}
	}
	return i == len(short)
}
