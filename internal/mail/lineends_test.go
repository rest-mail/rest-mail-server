package mail

import (
	"encoding/base64"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"
)

func forData(t *testing.T, raw string) string {
	t.Helper()
	var out strings.Builder
	if err := ForData(&out, raw); err != nil {
		t.Fatalf("ForData(%q): %v", raw, err)
	}
	return out.String()
}

func TestForDataMakesEveryLineEndCRLF(t *testing.T) {
	for name, c := range map[string]struct{ raw, want string }{
		"mixed":               {"A: b\r\n\r\none\ntwo\r\n", "A: b\r\n\r\none\r\ntwo\r\n"},
		"all LF":              {"A: b\n\none\ntwo\n", "A: b\r\n\r\none\r\ntwo\r\n"},
		"LF head, CRLF body":  {"A: b\n\none\r\ntwo\r\n", "A: b\r\n\r\none\r\ntwo\r\n"},
		"CR CR LF":            {"A: b\r\n\r\none\r\r\ntwo\r\n", "A: b\r\n\r\none\r\ntwo\r\n"},
		"bare CR":             {"A: b\r\n\r\none\rtwo\r\n", "A: b\r\n\r\none\r\ntwo\r\n"},
		"CR at the end":       {"A: b\r\n\r\none\r", "A: b\r\n\r\none\r\n"},
		"no line end at end":  {"A: b\n\none", "A: b\r\n\r\none"},
		"high octets stay":    {"A: b\n\n\xe9\xff\n", "A: b\r\n\r\n\xe9\xff\r\n"},
		"a dot stays one dot": {"A: b\r\n\r\none\n.\ntwo\r\n", "A: b\r\n\r\none\r\n.\r\ntwo\r\n"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := forData(t, c.raw); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
			if HasBareLineEnds(c.want) {
				t.Fatalf("%q still has a bare line end", c.want)
			}
		})
	}
}

func TestHasBareLineEnds(t *testing.T) {
	for raw, want := range map[string]bool{
		"": false, "a\r\nb\r\n": false, "a\nb": true, "a\rb": true, "a\r\r\nb": true, "a\r": true, "\n": true,
	} {
		if got := HasBareLineEnds(raw); got != want {
			t.Fatalf("HasBareLineEnds(%q) = %v", raw, got)
		}
	}
}

// octets holds what line ends can be: every sequence DATA would change.
const octets = "\x00\x01\n\x02\r\x03\r\r\n\x04\n\n\xff\r\n\x05"

func multipartWith(partHead, partBody string) string {
	return "From: a@b.test\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"b1\"\r\n\r\n" +
		"preamble\n--b1\r\nContent-Type: text/plain\r\n\r\nsee\nattached\r\n--b1\n" + partHead + "\r\n" + partBody + "\r\n--b1--\nepilogue\n"
}

func TestForDataNeverChangesABinaryPart(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte(octets))
	got := forData(t, multipartWith("Content-Type: application/octet-stream\r\nContent-Transfer-Encoding: 8bit\r\n", octets))
	want := "From: a@b.test\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"b1\"\r\n\r\n" +
		"preamble\r\n--b1\r\nContent-Type: text/plain\r\n\r\nsee\r\nattached\r\n--b1\r\n" +
		"Content-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n\r\n" + encoded + "\r\n\r\n--b1--\r\nepilogue\r\n"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}

	t.Run("one DATA carries goes as it is", func(t *testing.T) {
		clean := "\x00\x01\xff\r\n\x02"
		got := forData(t, multipartWith("Content-Type: image/png\r\nContent-Transfer-Encoding: binary\r\n", clean))
		if !strings.Contains(got, "Content-Transfer-Encoding: binary\r\n\r\n"+clean+"\r\n--b1--") {
			t.Fatalf("changed: %q", got)
		}
	})
	t.Run("declared binary, whatever its type", func(t *testing.T) {
		got := forData(t, multipartWith("Content-Type: text/plain\r\nContent-Transfer-Encoding: binary\r\n", "a\nb"))
		if !strings.Contains(got, base64.StdEncoding.EncodeToString([]byte("a\nb"))) {
			t.Fatalf("changed: %q", got)
		}
	})
	t.Run("declared text holding a NUL", func(t *testing.T) {
		got := forData(t, "Subject: x\n\na\x00\nb")
		want := "Subject: x\r\nMIME-Version: 1.0\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString([]byte("a\x00\nb")) + "\r\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
	t.Run("a folded encoding field goes whole", func(t *testing.T) {
		got := forData(t, "Content-Type: image/png\r\nContent-Transfer-Encoding:\r\n 8bit\r\nX: y\r\n\r\na\nb")
		if strings.Contains(got, "8bit") || !strings.Contains(got, "X: y\r\nMIME-Version: 1.0\r\nContent-Transfer-Encoding: base64\r\n\r\n") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("a part inside a nested message", func(t *testing.T) {
		got := forData(t, "Content-Type: message/rfc822\r\n\r\nSubject: in\nContent-Type: image/png\n\na\nb")
		if !strings.Contains(got, "Subject: in\r\nContent-Type: image/png\r\nMIME-Version: 1.0\r\nContent-Transfer-Encoding: base64\r\n\r\n"+base64.StdEncoding.EncodeToString([]byte("a\nb"))) {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("a nested multipart", func(t *testing.T) {
		inner := "Content-Type: multipart/related; boundary=b2\n\n--b2\nContent-Type: image/png\n\na\nb\n--b2--\n"
		got := forData(t, "Content-Type: multipart/mixed; boundary=b1\n\n--b1\n"+inner+"--b1--\n")
		if !strings.Contains(got, "--b2\r\nContent-Type: image/png\r\nContent-Transfer-Encoding: base64\r\n\r\n"+base64.StdEncoding.EncodeToString([]byte("a\nb"))+"\r\n\r\n--b2--\r\n") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("a digest part is a message by default", func(t *testing.T) {
		got := forData(t, "Content-Type: multipart/digest; boundary=b1\n\n--b1\n\nContent-Type: image/png\n\na\nb\n--b1--\n")
		if !strings.Contains(got, base64.StdEncoding.EncodeToString([]byte("a\nb"))) {
			t.Fatalf("got %q", got)
		}
	})
}

func TestForDataLeavesWhatIsEncodedToItsLineEnds(t *testing.T) {
	got := forData(t, "Content-Type: image/png\nContent-Transfer-Encoding: BASE64\n\nAAAA\nAAAA\n")
	if got != "Content-Type: image/png\r\nContent-Transfer-Encoding: BASE64\r\n\r\nAAAA\r\nAAAA\r\n" {
		t.Fatalf("got %q", got)
	}
}

func TestForDataRefusesWhatItCannotConvert(t *testing.T) {
	long := strings.Repeat("x", 999)
	for name, raw := range map[string]string{
		"binary under multipart/signed":      "Content-Type: multipart/signed; boundary=b1\r\n\r\n--b1\r\nContent-Type: image/png\r\n\r\na\nb\r\n--b1--\r\n",
		"binary under multipart/encrypted":   "Content-Type: multipart/encrypted; boundary=b1\r\n\r\n--b1\r\nContent-Type: image/png\r\n\r\na\nb\r\n--b1--\r\n",
		"an encoding nothing can read":       "Content-Type: image/png\r\nContent-Transfer-Encoding: x-private\r\n\r\na\nb",
		"a multipart with no boundary":       "Content-Type: multipart/mixed\r\n\r\na\nb",
		"past the depth limit":               strings.Repeat("Content-Type: message/rfc822\r\n\r\n", maxEntityDepth+1) + "a\nb",
		"a header line CRLF takes over":      "X: " + long[3:] + "\nA: b\r\n\r\nbody\r\n",
		"an encoded line CRLF takes over":    "Content-Transfer-Encoding: base64\r\n\r\n" + long + "\n",
		"a long line under multipart/signed": "Content-Type: multipart/signed; boundary=b1\r\n\r\n--b1\r\n\r\n" + long + "\nb\r\n--b1--\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			if err := ForData(io.Discard, raw); !errors.Is(err, ErrDataCannotCarry) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	t.Run("text under multipart/signed still goes", func(t *testing.T) {
		got := forData(t, "Content-Type: multipart/signed; boundary=b1\r\n\r\n--b1\r\n\r\na\nb\r\n--b1--\r\n")
		if !strings.Contains(got, "\r\n\r\na\r\nb\r\n--b1--") {
			t.Fatalf("got %q", got)
		}
	})
}

// RFC 5321 section 4.5.3.1.6 counts CRLF in its 1000 octets. A bare LF is one octet shorter, so a
// line within the limit can't become one over it: a text part goes as base64 instead.
func TestForDataCreatesNoLineOverTheLimit(t *testing.T) {
	longest := func(s string) int {
		most := 0
		for _, line := range strings.Split(s, "\r\n") {
			most = max(most, len(line)+2)
		}
		return most
	}
	t.Run("998 and a bare LF fits", func(t *testing.T) {
		line := strings.Repeat("x", 998)
		got := forData(t, "A: b\r\n\r\n"+line+"\nend\r\n")
		if got != "A: b\r\n\r\n"+line+"\r\nend\r\n" || longest(got) != maxTextLine {
			t.Fatalf("got %d", longest(got))
		}
	})
	t.Run("999 and a bare LF goes as base64", func(t *testing.T) {
		line := strings.Repeat("x", 999)
		got := forData(t, "A: b\r\n\r\n"+line+"\nend\r\n")
		if longest(got) > maxTextLine || !strings.Contains(got, "Content-Transfer-Encoding: base64\r\n") {
			t.Fatalf("got %d octets: %q", longest(got), got[:60])
		}
		encoded := strings.ReplaceAll(got[strings.Index(got, "\r\n\r\n")+4:], "\r\n", "")
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if string(decoded) != line+"\r\nend\r\n" {
			t.Fatalf("decoded %q", decoded)
		}
	})
	t.Run("999 and CR CR LF gets shorter", func(t *testing.T) {
		line := strings.Repeat("x", 997)
		got := forData(t, "A: b\r\n\r\n"+line+"\r\r\nend\n")
		if longest(got) > maxTextLine || strings.Contains(got, "base64") {
			t.Fatalf("got %d", longest(got))
		}
	})
	t.Run("a line already over goes as it is", func(t *testing.T) {
		line := strings.Repeat("x", 1500)
		got := forData(t, "A: b\r\n\r\n"+line+"\nend\n")
		if got != "A: b\r\n\r\n"+line+"\r\nend\r\n" {
			t.Fatal("changed more than its line end")
		}
	})
}

// ForData writes as it reads: what it allocates doesn't grow with the message (#1437).
func TestForDataHoldsNoCopyOfTheMessage(t *testing.T) {
	const size = 8 << 20
	text := strings.Repeat("a line of text\n", size/15)
	binary := strings.Repeat("\x00\xff\n", size/3)
	message := "From: a@b.test\r\nContent-Type: multipart/mixed; boundary=b1\r\n\r\n--b1\r\n\r\n" + text +
		"\r\n--b1\r\nContent-Type: image/png\r\n\r\n" + binary + "\r\n--b1--\r\n"
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if err := ForData(io.Discard, message); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("%d octets allocated %d", len(message), allocated)
	if allocated > 256<<10 {
		t.Fatalf("%d octets allocated %d", len(message), allocated)
	}
}

func FuzzForDataLeavesNoBareLineEnd(f *testing.F) {
	f.Add("A: b\r\n\r\none\n.\ntwo\r.\rthree\r\r\n")
	f.Add(multipartWith("Content-Type: image/png\r\n", octets))
	f.Fuzz(func(t *testing.T, raw string) {
		var out strings.Builder
		if err := ForData(&out, raw); err != nil {
			return
		}
		if HasBareLineEnds(out.String()) {
			t.Fatalf("ForData(%q) = %q", raw, out.String())
		}
	})
}
