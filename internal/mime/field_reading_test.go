package mime

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"math/rand"
	stdmime "mime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rest-mail/go-imap/pkg/imap/wireform"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// imapValue is one thing in a BODYSTRUCTURE: a list when isList, otherwise a string, with NIL an
// empty one.
type imapValue struct {
	isList bool
	list   []imapValue
	text   string
}

// readIMAPValue reads a value off the front of what a BODYSTRUCTURE answers.
func readIMAPValue(t *testing.T, s string) (imapValue, string) {
	t.Helper()
	s = strings.TrimLeft(s, " ")
	if s == "" {
		t.Fatal("BODYSTRUCTURE ended early")
	}
	switch c := s[0]; {
	case c == '(':
		s = s[1:]
		value := imapValue{isList: true}
		for !strings.HasPrefix(strings.TrimLeft(s, " "), ")") {
			var item imapValue
			item, s = readIMAPValue(t, s)
			value.list = append(value.list, item)
		}
		return value, strings.TrimLeft(s, " ")[1:]
	case c == '"':
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			switch s[i] {
			case '\\':
				i++
				b.WriteByte(s[i])
			case '"':
				return imapValue{text: b.String()}, s[i+1:]
			default:
				b.WriteByte(s[i])
			}
		}
		t.Fatalf("a string never closes in %q", s)
	case c == '{':
		end := strings.Index(s, "}\r\n")
		size, err := strconv.Atoi(s[1:end])
		if err != nil {
			t.Fatal(err)
		}
		from := end + 3
		return imapValue{text: s[from : from+size]}, s[from+size:]
	}
	end := strings.IndexAny(s, " )")
	if end < 0 {
		end = len(s)
	}
	if s[:end] == "NIL" {
		return imapValue{}, s[end:]
	}
	return imapValue{text: s[:end]}, s[end:]
}

func (v imapValue) parameter(name string) string {
	for i := 0; i+1 < len(v.list); i += 2 {
		if strings.EqualFold(v.list[i].text, name) {
			return v.list[i+1].text
		}
	}
	return ""
}

// aLeaf is what a reader says of one part that holds no parts: its type, whether it's an attachment,
// and what that's called.
type aLeaf struct {
	mediaType string
	attached  bool
	filename  string
}

func (l aLeaf) String() string {
	return fmt.Sprintf("%s attached=%v %q", l.mediaType, l.attached, l.filename)
}

// sentOctets is a parameter's octets where go-imap spells ones that aren't UTF-8 as encoded words, so
// a client is told the reading a row is, in the only spelling it can take.
func sentOctets(value string) string {
	if !strings.HasPrefix(value, "=?UNKNOWN-8BIT?") {
		return value
	}
	decoder := stdmime.WordDecoder{CharsetReader: func(_ string, input io.Reader) (io.Reader, error) { return input, nil }}
	octets, err := decoder.DecodeHeader(value)
	if err != nil {
		return value
	}
	return octets
}

// clientLeaves is what a mail client is told of a part, sorted, classified by the rule filters are
// (walk), so what's compared is what a filter would have to be handed.
func clientLeaves(node imapValue, root bool) []aLeaf {
	if node.list[0].isList {
		// A multipart with no parts is described to a client as holding one empty text part, which is
		// no part anyone sent, and the only size 0 text here.
		if only := node.list[0]; !node.list[1].isList && len(only.list) > 6 && only.list[0].text == "TEXT" && only.list[6].text == "0" {
			return nil
		}
		var leaves []aLeaf
		for _, child := range node.list {
			if !child.isList {
				break
			}
			leaves = append(leaves, clientLeaves(child, false)...)
		}
		return leaves
	}
	mediaType := strings.ToLower(node.list[0].text + "/" + node.list[1].text)
	// Whatever comes before them, a leaf ends with its MD5, disposition, language and location.
	disposition := node.list[len(node.list)-3]
	filename := ""
	dispositionType := ""
	if disposition.isList {
		dispositionType = disposition.list[0].text
		filename = disposition.list[1].parameter("filename")
	}
	if filename == "" {
		filename = node.list[2].parameter("name")
	}
	filename = sanitizeHeaderValue(parameterText(sentOctets(filename)))
	if root {
		return []aLeaf{{mediaType: mediaType}}
	}
	if mediaType == "text/calendar" {
		return []aLeaf{{mediaType: mediaType}}
	}
	attached := strings.EqualFold(dispositionType, "attachment") || (filename != "" && !strings.HasPrefix(mediaType, "text/"))
	if !attached {
		filename = ""
	}
	return []aLeaf{{mediaType, attached, filename}}
}

// filterLeaves is what the parsed message hands a filter of the same parts.
func filterLeaves(email *pipeline.EmailJSON) []aLeaf {
	var leaves []aLeaf
	var walk func(body pipeline.Body)
	walk = func(body pipeline.Body) {
		if strings.HasPrefix(body.ContentType, "multipart/") || len(body.Parts) > 0 {
			for _, part := range body.Parts {
				walk(part)
			}
			return
		}
		leaves = append(leaves, aLeaf{mediaType: body.ContentType})
	}
	walk(email.Body)
	for _, attachment := range email.Attachments {
		leaves = append(leaves, aLeaf{attachment.ContentType, true, attachment.Filename})
	}
	return leaves
}

func sortedLeaves(leaves []aLeaf) []string {
	text := make([]string, 0, len(leaves))
	for _, leaf := range leaves {
		text = append(text, leaf.String())
	}
	slices.Sort(text)
	return text
}

// differently says how what a filter is handed of raw differs from what a mail client is told, or "".
func differently(t *testing.T, raw string) string {
	t.Helper()
	structure, _ := readIMAPValue(t, wireform.BodyStructure(raw))
	want := sortedLeaves(clientLeaves(structure, !structure.list[0].isList))
	parsed, err := Parse([]byte(raw))
	if err != nil {
		return "Parse: " + err.Error()
	}
	got := sortedLeaves(filterLeaves(parsed))
	if !slices.Equal(got, want) {
		return fmt.Sprintf("Parse hands filters %q, a client is told %q", got, want)
	}
	stream, err := ParseStream(context.Background(), bytes.NewReader([]byte(raw)), StreamOptions{Store: entityStore, MaxText: math.MaxInt64})
	if err != nil {
		return "ParseStream: " + err.Error()
	}
	if got := sortedLeaves(filterLeaves(stream)); !slices.Equal(got, want) {
		return fmt.Sprintf("ParseStream hands filters %q, a client is told %q", got, want)
	}
	return ""
}

// typeDifferently says how parseContentType reads a field apart from a message that holds it.
func typeDifferently(t *testing.T, field string) string {
	t.Helper()
	structure, _ := readIMAPValue(t, wireform.BodyStructure("From: a@b.test\r\nContent-Type: "+field+"\r\n\r\n"))
	mediaType, params, _ := parseContentType(field)
	var wantType string
	var parameters imapValue
	if structure.list[0].isList {
		at := 0
		for structure.list[at].isList {
			at++
		}
		wantType, parameters = "multipart/"+strings.ToLower(structure.list[at].text), structure.list[at+1]
	} else {
		wantType, parameters = strings.ToLower(structure.list[0].text+"/"+structure.list[1].text), structure.list[2]
	}
	if mediaType != wantType {
		return fmt.Sprintf("type %q, a client is told %q", mediaType, wantType)
	}
	// A value that isn't UTF-8 is spelled in encoded words for a client, and a text part naming no
	// charset is told RFC 2046 §4.1.2's default. Those are spellings of the same reading.
	spelled := func(octets string) string {
		if utf8.ValidString(octets) {
			return octets
		}
		return strings.ReplaceAll(wireform.EncodeWords(octets), "\r\n ", " ")
	}
	if want := parameters.parameter("charset"); spelled(params["charset"]) != want && (params["charset"] != "" || want != "US-ASCII") {
		return fmt.Sprintf("charset %q, a client is told %q", params["charset"], want)
	}
	if strings.HasPrefix(wantType, "multipart/") {
		if want := parameters.parameter("boundary"); spelled(params["boundary"]) != want {
			return fmt.Sprintf("boundary %q, a client is told %q", params["boundary"], want)
		}
	}
	if want := parameterText(sentOctets(parameters.parameter("name"))); params["name"] != want {
		return fmt.Sprintf("name %q, a client is told %q", params["name"], want)
	}
	return ""
}

// aFaultyField is a parameter field with something in it the two readers have read differently.
func aFaultyField(random *rand.Rand, start string) string {
	words := []string{
		"boundary=a", `boundary="a"`, "BOUNDARY=a", "boundary=b", `boundary="b`, "boundary*0=a", "boundary*1=b", "boundary*=utf-8''a", "boundary*=iso-8859-1''a", "boundary*0*=us-ascii''a",
		"boundary=a (comment)", "(comment) boundary=a", "boundary=(comment) a", `boundary="=?utf-8?Q?a?="`, "boundary=\xc3\xa9", "boundary=", `boundary=""`, `boundary="a "`,
		"x=1", "x=2", "x=caf\xc3\xa9", "x=caf\xe9", `x="b`, "x=(c)", "x", "=x", "*0=x", "x*=y", ";",
		"charset=utf-8", "charset=iso-8859-1", "charset*=utf-8''iso-8859-1", "charset*0=utf;charset*1=-8", `charset="=?utf-8?Q?utf-8?="`, "charset=utf-8 (c)", "charset=\xc3\xa9",
		"name=a.txt", "name=a.exe (comment)", "name=a\xc3\xa9.exe", "name=a\xe9.exe", "name*=iso-8859-1''a%E9.exe", "name*0=a;name*1=.exe", "name*0*=utf-8''a%20b;name*1=.exe", "name=\"=?utf-8?Q?a.exe?=\"",
		"filename=a.txt", "filename=a.exe (comment)", "filename=(comment) a.exe", "filename=a\xc3\xa9.exe", "filename=a\xe9.exe", "filename*=iso-8859-1''a%E9.exe", "filename*=utf-8''a%E9.exe", "filename*=windows-1252''a%E9.exe", "filename*=x-none''a%E9.exe",
		"filename*0=a;filename*1=.exe", "filename*1=.exe", "filename*0*=iso-8859-1''a%E9;filename*1*=%E9", "filename=\"=?utf-8?Q?a.exe?=\"", "filename=\"=?iso-8859-1?Q?a=E9.exe?=\"", "filename=\"a.exe\" (comment)", `filename="a\"b.exe"`, "filename=", `filename=""`, "filename=a b.exe",
	}
	var b strings.Builder
	b.WriteString(start)
	count := 1 + random.Intn(5)
	if random.Intn(5) == 0 {
		count = 18 + random.Intn(8)
	}
	for range count {
		b.WriteString("; ")
		b.WriteString(words[random.Intn(len(words))])
	}
	field := []byte(b.String())
	if random.Intn(3) == 0 {
		alphabet := []byte(";=\"\\,<>@ \t-*'%aB0.?()/\xc3\xa9\xff")
		at := random.Intn(len(field))
		field = append(field[:at:at], append([]byte{alphabet[random.Intn(len(alphabet))]}, field[at:]...)...)
	}
	return string(field)
}

// fieldRounds is how many fields each kind is mutated through.
func fieldRounds() int {
	if testing.Short() {
		return 1000
	}
	return 10000
}

// A NUL an encoded word spells in a Subject or a sender's name reads as the space a mail client is sent
// for it (#1968).
func TestNULInAnEncodedWordReadsAsASpace(t *testing.T) {
	for _, tc := range []struct{ field, want string }{
		{"=?utf-8?Q?a=00b?=", "a b"},
		{"=?utf-8?B?YQBi?=", "a b"},
		{"=?iso-8859-1?Q?a=00b?=", "a b"},
		{"=?windows-1252?Q?a=00b?=", "a b"},
		{"=?utf-16be?B?AGEAAABi?=", "a b"},
		{"=?utf-16be?B?AGEAYg==?=", "ab"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			parsed, err := Parse([]byte("From: " + tc.field + " <a@b.test>\r\nSubject: " + tc.field + "\r\n\r\nbody"))
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Headers.Subject != tc.want {
				t.Errorf("Subject %q, want %q", parsed.Headers.Subject, tc.want)
			}
			if first, ok := parsed.Headers.From.First(); !ok || first.Name != tc.want || first.Address != "a@b.test" {
				t.Errorf("From %+v (%v), want the name %q", first, ok, tc.want)
			}
			if all := parsed.Headers.From.All(); len(all) != 1 || all[0].Name != tc.want {
				t.Errorf("From %+v, want the name %q", all, tc.want)
			}
		})
	}
	const kept = "=?x-none?Q?a=00b?="
	parsed, err := Parse([]byte("Subject: " + kept + "\r\n\r\nbody"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "=?x-none?Q?a_b?="; parsed.Headers.Subject != want {
		t.Errorf("Subject %q, want %q as a client is sent it", parsed.Headers.Subject, want)
	}
}

// What a filter is handed of a message is what a mail client is told of it, for any field a sender can
// write: that a part can't be hidden from a filter by a field one of them reads and the other doesn't.
// It reads each field in the place it matters, with the real readers on both sides.
func TestFieldsReadAsBodyStructureTellsAClient(t *testing.T) {
	const head = "From: a@b.test\r\nTo: c@d.test\r\nSubject: s\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\nMIME-Version: 1.0\r\n"
	const parts = "\r\n--a\r\nContent-Type: text/plain\r\n\r\nvisible\r\n--a\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=a.txt\r\n\r\nATTACHED\r\n--a--\r\n"
	mixed := func(part string) string {
		return head + "Content-Type: multipart/mixed; boundary=a\r\n\r\n--a\r\nContent-Type: text/plain\r\n\r\nvisible\r\n--a\r\n" + part + "\r\n\r\nATTACHED\r\n--a--\r\n"
	}
	for _, kind := range []struct {
		name  string
		start []string
		build func(field string) string
	}{
		{"a message's type", []string{"multipart/mixed", "MULTIPART / Mixed", "multipart/mixed (c)", "(c) multipart/mixed", "text/plain", "application/octet-stream", "bad", "multipart"},
			func(field string) string { return head + "Content-Type: " + field + "\r\n" + parts }},
		{"a part's type", []string{"application/octet-stream", "text/plain", "application/octet-stream (c)", "multipart/mixed", "text / plain", "bad"},
			func(field string) string { return mixed("Content-Type: " + field) }},
		{"a part's disposition", []string{"attachment", "ATTACHMENT", "attachment (c)", "(c) attachment", "inline", "attachment/x", "form-data", "bad/"},
			func(field string) string {
				return mixed("Content-Type: application/octet-stream\r\nContent-Disposition: " + field)
			}},
	} {
		t.Run(kind.name, func(t *testing.T) {
			random := rand.New(rand.NewSource(1968))
			wrong := 0
			for round := range fieldRounds() {
				field := aFaultyField(random, kind.start[random.Intn(len(kind.start))])
				raw := kind.build(field)
				if why := differently(t, raw); why != "" {
					wrong++
					if wrong <= 25 {
						t.Errorf("round %d: %q:\n%s", round, field, why)
					}
				}
			}
			if wrong > 25 {
				t.Errorf("%d in all", wrong)
			}
		})
	}
	t.Run("a field's parameters", func(t *testing.T) {
		random := rand.New(rand.NewSource(1968))
		wrong := 0
		for round := range fieldRounds() {
			field := aFaultyField(random, []string{"multipart/mixed", "text/plain", "application/octet-stream"}[random.Intn(3)])
			if why := typeDifferently(t, field); why != "" {
				wrong++
				if wrong <= 25 {
					t.Errorf("round %d: %q:\n%s", round, field, why)
				}
			}
		}
		if wrong > 25 {
			t.Errorf("%d in all", wrong)
		}
	})
}
