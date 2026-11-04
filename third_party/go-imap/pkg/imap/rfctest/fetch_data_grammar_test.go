package rfctest

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// grammarFixture selects a mailbox holding raw as UID 10.
func grammarFixture(t *testing.T, raw string) *client {
	t.Helper()
	box := newFetchMailbox()
	box.raw[10] = raw
	return fetchClient(t, box, "EXAMINE")
}

// fetchBoth fetches raw's BODY and BODYSTRUCTURE. The grammar reader refuses a
// response the §9 productions refuse.
func fetchBoth(t *testing.T, raw string) (body, structure *wireBody) {
	t.Helper()
	f := fetched(t, grammarFixture(t, raw), "FETCH 1 (BODY BODYSTRUCTURE)")
	return f.body, f.bodyStructure
}

// fetchEnvelope fetches raw's ENVELOPE through the same reader.
func fetchEnvelope(t *testing.T, raw string) *wireEnvelope {
	t.Helper()
	return fetched(t, grammarFixture(t, raw), "FETCH 1 ENVELOPE").envelope
}

// wireShapeFixtures hold one message of each media shape a client meets,
// including the ones whose headers cannot be believed.
var wireShapeFixtures = []struct {
	name, raw string
	// kind and path name the top-level alternative of body the message must take.
	kind string
}{
	{"no content type", "Subject: x\r\n\r\nhi\r\n", "text"},
	{"text with charset", "Content-Type: text/html; charset=utf-8\r\n\r\n<p>caf\xc3\xa9</p>\r\n", "text"},
	{"application", "Content-Type: application/pdf; name=a.pdf\r\nContent-Transfer-Encoding: base64\r\n\r\nQUJD\r\n", "basic"},
	{"image", "Content-Type: image/png\r\nContent-Transfer-Encoding: base64\r\n\r\nQUJD\r\n", "basic"},
	{"message rfc822", "Content-Type: message/rfc822\r\n\r\nSubject: in\r\n\r\nbody\r\n", "message"},
	{"message rfc822 in capitals", "Content-Type: MESSAGE/RFC822\r\n\r\nSubject: in\r\n\r\nbody\r\n", "message"},
	{"message rfc822 empty", "Content-Type: message/rfc822\r\n\r\n", "message"},
	{"message rfc822 unreadable", "Content-Type: message/rfc822\r\n\r\nnot a header\r\n\r\nbody\r\n", "message"},
	{"message rfc822 base64", "Content-Type: message/rfc822\r\nContent-Transfer-Encoding: base64\r\n\r\nQUJD\r\n", "basic"},
	{"message partial", "Content-Type: message/partial; id=a; number=1\r\n\r\nbody\r\n", "basic"},
	{"message external-body", "Content-Type: message/external-body; access-type=URL; url=x\r\n\r\n", "basic"},
	{"multipart", "Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\n\r\none\r\n--b\r\nContent-Type: image/gif\r\n\r\ntwo\r\n--b--\r\n", "multipart"},
	{"multipart without parts", "Content-Type: multipart/mixed; boundary=b\r\n\r\nno delimiter here\r\n", "multipart"},
	{"multipart without boundary", "Content-Type: multipart/mixed\r\n\r\nbody\r\n", "text"},
	{"multipart digest", "Content-Type: multipart/digest; boundary=b\r\n\r\n--b\r\n\r\nSubject: in\r\n\r\nbody\r\n--b--\r\n", "multipart"},
	{"multipart nested", "Content-Type: multipart/mixed; boundary=a\r\n\r\n--a\r\nContent-Type: multipart/alternative; boundary=b\r\n\r\n--b\r\n\r\none\r\n--b--\r\n--a\r\nContent-Type: message/rfc822\r\n\r\nSubject: in\r\nContent-Type: multipart/mixed; boundary=c\r\n\r\n--c\r\n\r\ntwo\r\n--c--\r\n--a--\r\n", "multipart"},
	{"unknown encoding", "Content-Type: text/plain\r\nContent-Transfer-Encoding: x-private\r\n\r\nhi\r\n", "basic"},
	{"malformed content type", "Content-Type: /;;;\r\n\r\nhi\r\n", "text"},
	{"bare type", "Content-Type: text\r\n\r\nhi\r\n", "text"},
	{"unreadable header", "not a header\r\n\r\nbody\r\n", "text"},
	{"empty message", "", "text"},
	{"only a header", "Subject: x\r\n\r\n", "text"},
}

// walkBody visits body and everything inside it.
func walkBody(body *wireBody, visit func(*wireBody)) {
	visit(body)
	for _, part := range body.parts {
		walkBody(part, visit)
	}
	if body.inner != nil {
		walkBody(body.inner, visit)
	}
}

// TestBodyAlternatives proves §9's choice of body production. A type of
// MESSAGE/RFC822 takes body-type-msg, with its envelope, nested body and line
// count, because body-type-basic's "MESSAGE subtype MUST NOT be RFC822". TEXT
// takes body-type-text with its lines. A multipart holds at least one body, then
// its subtype, and nothing else is reported as MULTIPART.
//
// checklist: IMAP-INV-ABNF-04
func TestBodyAlternatives(t *testing.T) {
	for _, tc := range wireShapeFixtures {
		t.Run(tc.name, func(t *testing.T) {
			body, structure := fetchBoth(t, tc.raw)
			for form, top := range map[string]*wireBody{"BODY": body, "BODYSTRUCTURE": structure} {
				if top.kind != tc.kind {
					t.Fatalf("%s took the %s alternative, want %s: %s", form, top.kind, tc.kind, top)
				}
				walkBody(top, func(b *wireBody) {
					switch b.kind {
					case "basic":
						if strings.EqualFold(b.mediaType, "TEXT") || strings.EqualFold(b.mediaType, "MULTIPART") {
							t.Fatalf("%s has basic %s: %s", form, b.mediaType, top)
						}
					case "message":
						if b.envelope == nil || b.inner == nil {
							t.Fatalf("%s has a message without its parts: %s", form, top)
						}
					case "multipart":
						if len(b.parts) == 0 || b.subtype == "" {
							t.Fatalf("%s has an empty multipart: %s", form, top)
						}
					}
				})
			}
		})
	}
}

// TestBodyLineCounts pins the counts the two line-bearing alternatives carry:
// octets and lines of the part as stored, for the message form of the whole
// encapsulated message and not its body alone.
//
// checklist: IMAP-INV-ABNF-04
func TestBodyLineCounts(t *testing.T) {
	const inner = "Subject: in\r\n\r\none\r\ntwo\r\n"
	_, structure := fetchBoth(t, "Content-Type: message/rfc822\r\n\r\n"+inner)
	if structure.octets != uint32(len(inner)) || structure.lines != 4 {
		t.Fatalf("message counts %d %d, want %d 4: %s", structure.octets, structure.lines, len(inner), structure)
	}
	if structure.inner.kind != "text" || structure.inner.lines != 2 || structure.inner.octets != 10 {
		t.Fatalf("inner body: %s", structure.inner)
	}
}

// TestBodyExtensionForms proves §9's extension rules on every shape. BODY has
// no extension data at any depth, because it is "non-extensible". BODYSTRUCTURE
// has all four defined fields in order, at every depth, and no field beyond
// them. RFC 3501 defines no body-extension value, so the reader refuses one.
//
// checklist: IMAP-INV-ABNF-05
func TestBodyExtensionForms(t *testing.T) {
	for _, tc := range wireShapeFixtures {
		t.Run(tc.name, func(t *testing.T) {
			body, structure := fetchBoth(t, tc.raw)
			walkBody(body, func(b *wireBody) {
				if b.extension != nil {
					t.Fatalf("BODY carries extension data: %s", body)
				}
			})
			walkBody(structure, func(b *wireBody) {
				if b.extension == nil || b.extension.count != 4 {
					t.Fatalf("BODYSTRUCTURE lacks its four extension fields: %s", structure)
				}
			})
		})
	}
}

// TestBodyExtensionOrder uses a distinct value in each extension field, so a
// swap cannot go unseen. A one-part body has MD5 first and a multipart has its
// parameters first.
//
// checklist: IMAP-INV-ABNF-05
func TestBodyExtensionOrder(t *testing.T) {
	const fields = "Content-Disposition: inline; a=b\r\nContent-Language: en\r\nContent-Location: http://x/\r\n"
	_, structure := fetchBoth(t, "Content-MD5: md5\r\n"+fields+"\r\nhi\r\n")
	if got, want := structure.extension.String(), ` "md5" ("INLINE" ("A" "b")) "en" "http://x/"`; got != want {
		t.Fatalf("one part: %s, want %s", got, want)
	}
	_, structure = fetchBoth(t, "Content-Type: multipart/mixed; boundary=b\r\n"+fields+"\r\n--b\r\n\r\none\r\n--b--\r\n")
	if got, want := structure.extension.String(), ` ("BOUNDARY" "b") ("INLINE" ("A" "b")) "en" "http://x/"`; got != want {
		t.Fatalf("multipart: %s, want %s", got, want)
	}
}

// TestBodyFieldAlternatives walks each §9 body field through its alternatives:
// parameters as pairs or NIL with an empty value kept, id and description as NIL
// or a string, an encoding defaulted to 7BIT, a disposition with and without
// parameters, a language as NIL, one string or a list, and MD5 and location as
// NIL or a string. A value the quoted form cannot carry comes as a literal, and
// the reader takes either.
//
// checklist: IMAP-INV-ABNF-06
func TestBodyFieldAlternatives(t *testing.T) {
	for _, tc := range []struct{ name, header, want string }{
		{"every field absent", "", `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 4 1 NIL NIL NIL NIL)`},
		{"parameters", "Content-Type: text/plain; charset=utf-8; format=flowed\r\n", `("TEXT" "PLAIN" ("CHARSET" "utf-8" "FORMAT" "flowed") NIL NIL "7BIT" 4 1 NIL NIL NIL NIL)`},
		{"no parameters", "Content-Type: application/x-a\r\n", `("APPLICATION" "X-A" NIL NIL NIL "7BIT" 4 NIL NIL NIL NIL)`},
		{"empty parameter", "Content-Type: application/x-a; name=\"\"\r\n", `("APPLICATION" "X-A" ("NAME" "") NIL NIL "7BIT" 4 NIL NIL NIL NIL)`},
		{"id and description", "Content-ID: <i@x>\r\nContent-Description: words\r\n", `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") "<i@x>" "words" "7BIT" 4 1 NIL NIL NIL NIL)`},
		{"8-bit description", "Content-Description: caf\xc3\xa9\r\n", `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL "café" "7BIT" 4 1 NIL NIL NIL NIL)`},
		{"encoding in capitals", "Content-Transfer-Encoding: Quoted-Printable\r\n", `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "QUOTED-PRINTABLE" 4 1 NIL NIL NIL NIL)`},
		{"disposition without parameters", "Content-Disposition: inline\r\n", `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 4 1 NIL ("INLINE" NIL) NIL NIL)`},
		{"disposition with parameters", "Content-Disposition: attachment; filename=a.txt\r\n", `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 4 1 NIL ("ATTACHMENT" ("FILENAME" "a.txt")) NIL NIL)`},
		{"disposition unreadable", "Content-Disposition: ;;;\r\n", `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 4 1 NIL NIL NIL NIL)`},
		{"one language", "Content-Language: en\r\n", `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 4 1 NIL NIL "en" NIL)`},
		{"language list", "Content-Language: en, de\r\n", `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 4 1 NIL NIL ("en" "de") NIL)`},
		{"language empty", "Content-Language: ,\r\n", `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 4 1 NIL NIL NIL NIL)`},
		{"md5 and location", "Content-MD5: abc=\r\nContent-Location: http://x/\r\n", `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "7BIT" 4 1 "abc=" NIL NIL "http://x/")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, structure := fetchBoth(t, tc.header+"\r\nhi\r\n")
			if got := structure.String(); got != tc.want {
				t.Fatalf("\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

// TestBodyStringFraming puts hostile octets in every string field of a body, at
// the top, in a multipart child and in an encapsulated message. §§4.3 and 9
// allow a quoted string only for 7-bit text without CR or LF, so anything else
// must be a literal, and a literal holding what looks like a response must stay
// data. The reader refuses wrong framing. Every value must also come back whole.
//
// checklist: IMAP-BS-10
func TestBodyStringFraming(t *testing.T) {
	values := []struct {
		name, header, want string
		// noChild leaves out the multipart child. The Go reader of a child's
		// header refuses these octets; rest-mail/rest-mail-server#1863 records it.
		noChild bool
	}{
		{name: "quoted-specials", header: `a "b" \c`, want: `a "b" \c`},
		{name: "8-bit", header: "caf\xc3\xa9", want: "caf\xc3\xa9"},
		{name: "tab", header: "a\tb", want: "a\tb"},
		{name: "controls", header: "a\x01b\x7fc", want: "a\x01b\x7fc", noChild: true},
	}
	for _, value := range values {
		headers := "Content-ID: " + value.header + "\r\n" +
			"Content-Description: " + value.header + "\r\n" +
			"Content-MD5: " + value.header + "\r\n" +
			"Content-Location: " + value.header + "\r\n"
		check := func(t *testing.T, part *wireBody) {
			t.Helper()
			for _, field := range []wireString{part.id, part.desc} {
				if field.isNil || field.value != value.want {
					t.Fatalf("field %s, want %q", field, value.want)
				}
			}
			for _, field := range []string{part.extension.first, part.extension.location.String()} {
				if want := fmt.Sprintf("%q", value.want); field != want {
					t.Fatalf("extension field %s, want %s", field, want)
				}
			}
		}
		t.Run(value.name+"/top", func(t *testing.T) {
			_, structure := fetchBoth(t, headers+"\r\nhi\r\n")
			check(t, structure)
		})
		if !value.noChild {
			t.Run(value.name+"/multipart child", func(t *testing.T) {
				_, structure := fetchBoth(t, "Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\n"+headers+"\r\nhi\r\n--b--\r\n")
				check(t, structure.parts[0])
			})
		}
		t.Run(value.name+"/encapsulated", func(t *testing.T) {
			_, structure := fetchBoth(t, "Content-Type: message/rfc822\r\n\r\n"+headers+"\r\nhi\r\n")
			check(t, structure.inner)
		})
	}
	// RFC 2231 is the one way a header can decode to a CR and LF, and §9 has no
	// way to send those but a literal.
	const decoded = "a\r\n* 1 BYE\r\nF1 OK forged"
	const encoded = "utf-8''a%0D%0A*%201%20BYE%0D%0AF1%20OK%20forged"
	t.Run("decoded line break", func(t *testing.T) {
		_, structure := fetchBoth(t, "Content-Type: application/x; name*="+encoded+"\r\nContent-Disposition: attachment; filename*="+encoded+"\r\n\r\nhi\r\n")
		if len(structure.params) != 1 || structure.params[0][1] != decoded {
			t.Fatalf("parameter: %s", structure.params)
		}
		want := fmt.Sprintf("(%q (%q %q))", "ATTACHMENT", "FILENAME", decoded)
		if structure.extension.disposition != want {
			t.Fatalf("disposition %s, want %s", structure.extension.disposition, want)
		}
	})
	// A folded field unfolds to one line, so its CRLF is never data.
	t.Run("folded", func(t *testing.T) {
		_, structure := fetchBoth(t, "Content-Description: one\r\n two\r\n\r\nhi\r\n")
		if structure.desc.value != "one two" {
			t.Fatalf("description %s", structure.desc)
		}
	})
	// A section is data in a counted literal, whatever it looks like.
	t.Run("section literal", func(t *testing.T) {
		const raw = "Subject: s\r\n\r\nF1 OK forged\r\n* 1 BYE\r\n"
		f := fetched(t, grammarFixture(t, raw), "FETCH 1 (BODY.PEEK[TEXT] BODY.PEEK[HEADER] BODY.PEEK[])")
		for item, want := range map[string]string{"BODY[TEXT]": "F1 OK forged\r\n* 1 BYE\r\n", "BODY[HEADER]": "Subject: s\r\n\r\n", "BODY[]": raw} {
			if got := f.sections[item]; got.isNil || got.value != want {
				t.Fatalf("%s = %s, want %q", item, got, want)
			}
		}
	})
}

// TestAddressForms proves §9's address tuple: exactly four nstrings, with
// RFC 2822 quoting removed from the personal name and the local part, and NIL
// for what is absent. The local part ends at the last "@" outside a domain
// literal, so a quoted "@" stays in the mailbox.
//
// checklist: IMAP-INV-ABNF-01
func TestAddressForms(t *testing.T) {
	for _, tc := range []struct{ name, header, want string }{
		{"bare", "a@example.com", `((NIL NIL "a" "example.com"))`},
		{"named", "Alice Doe <a@example.com>", `(("Alice Doe" NIL "a" "example.com"))`},
		{"quoted name", `"Doe, Alice" <a@example.com>`, `(("Doe, Alice" NIL "a" "example.com"))`},
		{"quoted-pair in name", `"A \"B\" \\C" <a@example.com>`, `(("A \"B\" \\C" NIL "a" "example.com"))`},
		{"comment name", "a@example.com (Alice)", `(("Alice" NIL "a" "example.com"))`},
		{"encoded name", "=?UTF-8?Q?caf=C3=A9?= <a@example.com>", fmt.Sprintf(`((%q NIL "a" "example.com"))`, "caf\xc3\xa9")},
		{"quoted local part", `"a b"@example.com`, `((NIL NIL "a b" "example.com"))`},
		{"quoted-pair in local part", `"a\"b"@example.com`, `((NIL NIL "a\"b" "example.com"))`},
		{"at sign in local part", `"a@b"@example.com`, `((NIL NIL "a@b" "example.com"))`},
		{"at signs in local part", `"a@b@c"@example.com`, `((NIL NIL "a@b@c" "example.com"))`},
		{"domain literal", "a@[192.0.2.1]", `((NIL NIL "a" "[192.0.2.1]"))`},
		{"several", "a@example.com, B <b@example.com>", `((NIL NIL "a" "example.com")("B" NIL "b" "example.com"))`},
		{"group", "Team: a@example.com, b@example.com;", `((NIL NIL "Team" NIL)(NIL NIL "a" "example.com")(NIL NIL "b" "example.com")(NIL NIL NIL NIL))`},
		{"empty group", "Team:;", `((NIL NIL "Team" NIL)(NIL NIL NIL NIL))`},
		{"at sign in group", `Team: "a@b"@example.com;`, `((NIL NIL "Team" NIL)(NIL NIL "a@b" "example.com")(NIL NIL NIL NIL))`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envelope := fetchEnvelope(t, "To: "+tc.header+"\r\n\r\nbody")
			if got := envelope.to.String(); got != tc.want {
				t.Fatalf("\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

// TestEnvelopeSourceRoutes proves addr-adl: "Holds route from [RFC-2822]
// route-addr if non-NIL" (§9). A route-addr is still a legal address to read
// (RFC 2822 §4.4), so a route keeps its place and its address, and a neighbour
// without one keeps NIL. Before this, one route lost the whole header.
//
// checklist: IMAP-ENV-11 IMAP-INV-ABNF-01
func TestEnvelopeSourceRoutes(t *testing.T) {
	for _, tc := range []struct{ name, header, want string }{
		{"one hop", "<@a.example:joe@c.example>", `((NIL "@a.example" "joe" "c.example"))`},
		{"two hops", "<@a.example,@b.example:joe@c.example>", `((NIL "@a.example,@b.example" "joe" "c.example"))`},
		{"named", "Joe Doe <@a.example:joe@c.example>", `(("Joe Doe" "@a.example" "joe" "c.example"))`},
		{"spaces and comments", "Joe <@a.example (first) , @b.example:joe@c.example>", `(("Joe" "@a.example,@b.example" "joe" "c.example"))`},
		{"empty list members", "<,@a.example,,@b.example:joe@c.example>", `((NIL "@a.example,@b.example" "joe" "c.example"))`},
		{"next to a plain address", "x@example.com, <@a.example:joe@c.example>, y@example.com", `((NIL NIL "x" "example.com")(NIL "@a.example" "joe" "c.example")(NIL NIL "y" "example.com"))`},
		{"in a group", "Team: <@a.example:joe@c.example>, y@example.com;", `((NIL NIL "Team" NIL)(NIL "@a.example" "joe" "c.example")(NIL NIL "y" "example.com")(NIL NIL NIL NIL))`},
		{"quoted local part", `<@a.example:"j o@e"@c.example>`, `((NIL "@a.example" "j o@e" "c.example"))`},
		{"same address twice", "<@a.example:joe@c.example>, <@b.example:joe@c.example>", `((NIL "@a.example" "joe" "c.example")(NIL "@b.example" "joe" "c.example"))`},
		{"colon in a quoted name", `"a:b" <@a.example:joe@c.example>`, `(("a:b" "@a.example" "joe" "c.example"))`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envelope := fetchEnvelope(t, "To: "+tc.header+"\r\n\r\nbody")
			if got := envelope.to.String(); got != tc.want {
				t.Fatalf("\n got %s\nwant %s", got, tc.want)
			}
			inner, _ := fetchBoth(t, "Content-Type: message/rfc822\r\n\r\nTo: "+tc.header+"\r\n\r\nbody")
			if got := inner.envelope.to.String(); got != tc.want {
				t.Fatalf("encapsulated:\n got %s\nwant %s", got, tc.want)
			}
		})
	}
	t.Run("defaults carry the route", func(t *testing.T) {
		envelope := fetchEnvelope(t, "From: <@a.example:joe@c.example>\r\n\r\nbody")
		const want = `((NIL "@a.example" "joe" "c.example"))`
		for name, field := range map[string]wireAddresses{"from": envelope.from, "sender": envelope.sender, "reply-to": envelope.replyTo} {
			if got := field.String(); got != want {
				t.Fatalf("%s %s, want %s", name, got, want)
			}
		}
	})
	t.Run("unfinished route", func(t *testing.T) {
		if got := fetchEnvelope(t, "To: <@a.example joe@c.example>\r\n\r\nbody").to; got != nil {
			t.Fatalf("to %s, want NIL", got)
		}
	})
	// A sender chooses the header, so reading one must stay linear in its size.
	// A scan that restarts at each "<" takes minutes here, not milliseconds.
	for _, unit := range []string{"<@a:", "<[", "<(", `<@a:"`, "<@a", "<@a,"} {
		t.Run("long unfinished "+unit, func(t *testing.T) {
			header := strings.Repeat(unit, 1<<20/len(unit))
			if got := fetchEnvelope(t, "To: "+header+"\r\n\r\nbody").to; got != nil {
				t.Fatalf("to %s, want NIL", got)
			}
		})
	}
	t.Run("many routes", func(t *testing.T) {
		const count = 5000
		header := strings.TrimSuffix(strings.Repeat("<@a.example:joe@c.example>, ", count), ", ")
		to := fetchEnvelope(t, "To: "+header+"\r\n\r\nbody").to
		if len(to) != count || to[count-1].route.value != "@a.example" {
			t.Fatalf("%d addresses, want %d", len(to), count)
		}
	})
}

// TestEnvelopeFields proves §9's envelope: ten fields in their order, the scalar
// ones nstrings that keep "" apart from NIL, and the address ones NIL or a list
// holding at least one address. Sender and Reply-To default to From, "the same
// value as the from member" when "absent ... or are present but empty" (§7.4.2).
//
// checklist: IMAP-INV-ABNF-14
func TestEnvelopeFields(t *testing.T) {
	const from = `((NIL NIL "f" "example.com"))`
	for _, tc := range []struct{ name, raw, want string }{
		{"each field its own value",
			"Date: d\r\nSubject: s\r\nFrom: f@example.com\r\nSender: se@example.com\r\nReply-To: r@example.com\r\nTo: t@example.com\r\nCc: c@example.com\r\nBcc: b@example.com\r\nIn-Reply-To: i\r\nMessage-ID: m\r\n\r\nx",
			`("d" "s" ((NIL NIL "f" "example.com")) ((NIL NIL "se" "example.com")) ((NIL NIL "r" "example.com")) ((NIL NIL "t" "example.com")) ((NIL NIL "c" "example.com")) ((NIL NIL "b" "example.com")) "i" "m")`},
		{"nothing", "X-Other: y\r\n\r\nx", `(NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL)`},
		{"empty scalars", "Date:\r\nSubject:\r\nIn-Reply-To:\r\nMessage-ID:\r\n\r\nx", `("" "" NIL NIL NIL NIL NIL NIL "" "")`},
		{"blank address fields", "From: \r\nTo:  \r\nCc:\r\n\r\nx", `(NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL)`},
		{"address field of only commas", "To: ,\r\nCc: , ,\r\n\r\nx", `(NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL)`},
		{"address field of only a comment", "To: (nobody)\r\n\r\nx", `(NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL)`},
		{"absent defaults", "From: f@example.com\r\n\r\nx", "(NIL NIL " + from + " " + from + " " + from + " NIL NIL NIL NIL NIL)"},
		{"empty defaults", "From: f@example.com\r\nSender:\r\nReply-To: \r\n\r\nx", "(NIL NIL " + from + " " + from + " " + from + " NIL NIL NIL NIL NIL)"},
		{"no From to default to", "Sender:\r\nReply-To:\r\n\r\nx", `(NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL)`},
		{"defaults reach only Sender and Reply-To", "From: f@example.com\r\nTo:\r\nCc:\r\nBcc:\r\n\r\nx", "(NIL NIL " + from + " " + from + " " + from + " NIL NIL NIL NIL NIL)"},
		{"empty group is a list", "To: Team:;\r\n\r\nx", `(NIL NIL NIL NIL NIL ((NIL NIL "Team" NIL)(NIL NIL NIL NIL)) NIL NIL NIL NIL)`},
		{"literal scalars", "Subject: caf\xc3\xa9\r\nMessage-ID: <\xc3\xa9@x>\r\n\r\nx", fmt.Sprintf("(NIL %q NIL NIL NIL NIL NIL NIL NIL %q)", "caf\xc3\xa9", "<\xc3\xa9@x>")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fetchEnvelope(t, tc.raw).String(); got != tc.want {
				t.Fatalf("\n got %s\nwant %s", got, tc.want)
			}
			structure, _ := fetchBoth(t, "Content-Type: message/rfc822\r\n\r\n"+tc.raw)
			if got := structure.envelope.String(); got != tc.want {
				t.Fatalf("encapsulated:\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

// messageDataBox holds two messages with flags that change, for the rows about
// what message data may vary.
func messageDataBox() *fetchMailbox {
	box := newFetchMailbox()
	box.messages = append(box.messages, imap.Message{UID: 20, Size: len(fetchRaw), Date: box.messages[0].Date, Seen: true, Flagged: true})
	box.raw[20] = fetchRaw
	return box
}

// TestMessageData proves §9's message-data and msg-att: every FETCH names a
// message from 1 up and holds at least one item, FLAGS may change and the static
// items may not. "the static data ... MUST NOT change for any particular message"
// is the host's to keep, so the test holds the host's data fixed and shows the
// engine adds no change of its own, even when the fetch sets \Seen.
//
// checklist: IMAP-INV-ABNF-18
func TestMessageData(t *testing.T) {
	const static = "(UID RFC822.SIZE INTERNALDATE ENVELOPE BODY BODYSTRUCTURE)"
	box := messageDataBox()
	c := fetchClient(t, box, "SELECT")

	first := c.exchange("A1", "FETCH 1:* %s", static)
	if len(first) != 3 || first[0].number != 1 || first[1].number != 2 || first[2].kind != "OK" {
		t.Fatalf("FETCH: %+v", first)
	}
	for _, response := range first[:2] {
		if len(response.fetch.items) == 0 {
			t.Fatalf("empty msg-att: %+v", response)
		}
		if response.fetch.flags == nil || response.fetch.uid == 0 || response.fetch.size == 0 || response.fetch.internalDate == "" {
			t.Fatalf("missing metadata: %+v", response.fetch)
		}
	}

	seen := c.exchange("A2", "FETCH 1 BODY[TEXT]")
	if len(seen) != 2 || !slices.Contains(seen[0].fetch.flags, `\Seen`) {
		t.Fatalf("implicit Seen: %+v", seen)
	}

	second := c.exchange("A3", "FETCH 1 %s", static)
	before, after := first[0].fetch, second[0].fetch
	if slices.Contains(before.flags, `\Seen`) || !slices.Contains(after.flags, `\Seen`) {
		t.Fatalf("FLAGS did not change: %v then %v", before.flags, after.flags)
	}
	if before.uid != after.uid || before.size != after.size || before.internalDate != after.internalDate ||
		before.envelope.String() != after.envelope.String() ||
		before.body.String() != after.body.String() || before.bodyStructure.String() != after.bodyStructure.String() {
		t.Fatalf("static items changed:\n%+v\n%+v", before, after)
	}

	// A STORE's FETCH has only dynamic data and a sequence number.
	stored := c.exchange("A4", `STORE 2 +FLAGS (\Answered)`)
	if len(stored) != 2 || stored[0].number != 2 || !slices.Contains(stored[0].fetch.flags, `\Answered`) {
		t.Fatalf("STORE: %+v", stored)
	}

	// No item is no msg-att: an empty request cannot make an empty response.
	empty := c.exchange("A5", "FETCH 1 ()")
	if last := empty[len(empty)-1]; len(empty) != 1 || last.kind != "BAD" {
		t.Fatalf("FETCH with no item: %+v", empty)
	}
}

// TestMailboxData proves the other numbered and attribute forms of §9: EXISTS
// and RECENT take a number and may be 0, SEARCH takes only nz-numbers and may
// list none, and STATUS holds attribute and number pairs.
//
// checklist: IMAP-INV-ABNF-18
func TestMailboxData(t *testing.T) {
	t.Run("selected", func(t *testing.T) {
		box := messageDataBox()
		c := selectionClient(t, box)
		responses := c.exchange("A1", "SELECT INBOX")
		kinds := map[string]uint32{}
		for _, response := range responses {
			kinds[response.kind] = response.number
		}
		if got, ok := kinds["EXISTS"]; !ok || got != 2 {
			t.Fatalf("EXISTS: %+v", responses)
		}
		if _, ok := kinds["RECENT"]; !ok {
			t.Fatalf("RECENT: %+v", responses)
		}
		searched := c.exchange("A2", "SEARCH ALL")
		if len(searched) != 2 || !slices.Equal(searched[0].numbers, []uint32{1, 2}) {
			t.Fatalf("SEARCH: %+v", searched)
		}
		none := c.exchange("A3", `SEARCH KEYWORD absent`)
		if len(none) != 2 || none[0].kind != "SEARCH" || len(none[0].numbers) != 0 {
			t.Fatalf("empty SEARCH: %+v", none)
		}
		status := c.exchange("A4", "STATUS INBOX (MESSAGES RECENT UIDNEXT UIDVALIDITY UNSEEN)")
		if len(status) != 2 || status[0].name != "INBOX" {
			t.Fatalf("STATUS: %+v", status)
		}
		for _, name := range []string{"MESSAGES", "RECENT", "UIDNEXT", "UIDVALIDITY", "UNSEEN"} {
			if _, ok := status[0].status[name]; !ok {
				t.Fatalf("STATUS lacks %s: %+v", name, status[0])
			}
		}
		if status[0].status["MESSAGES"] != 2 || status[0].status["UNSEEN"] != 1 || status[0].status["UIDNEXT"] != 21 {
			t.Fatalf("STATUS values: %+v", status[0].status)
		}
	})
	t.Run("empty mailbox", func(t *testing.T) {
		box := messageDataBox()
		box.messages = nil
		c := selectionClient(t, box)
		responses := c.exchange("A1", "SELECT INBOX")
		zero := 0
		for _, response := range responses {
			if (response.kind == "EXISTS" || response.kind == "RECENT") && response.number == 0 {
				zero++
			}
		}
		if zero != 2 {
			t.Fatalf("EXISTS and RECENT of an empty mailbox: %+v", responses)
		}
		if none := c.exchange("A2", "FETCH 1:* FLAGS"); len(none) != 1 || none[0].kind != "OK" {
			t.Fatalf("FETCH on an empty mailbox: %+v", none)
		}
		if searched := c.exchange("A3", "SEARCH ALL"); len(searched) != 2 || len(searched[0].numbers) != 0 {
			t.Fatalf("SEARCH on an empty mailbox: %+v", searched)
		}
	})
}

// TestUIDCommandsReportUID proves §6.4.8: "any FETCH response caused by" a UID
// command "MUST include the UID", "even if the UID data item was not requested".
// Each command runs after another client changed a flag. The FETCH a UID command
// sends carries UID, and the ones that send none do not read the change, so no
// FETCH without UID can come from them. The FETCH that NOOP and IDLE send for
// that change is not caused by a UID command;
// rest-mail/rest-mail-server#1688 changes it.
//
// checklist: IMAP-INV-UID-06
func TestUIDCommandsReportUID(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		fetches       int
	}{
		{"FETCH of another item", "UID FETCH 10:20 RFC822.SIZE", 2},
		{"FETCH of FLAGS", "UID FETCH 10:20 FLAGS", 2},
		{"FETCH of a section", "UID FETCH 10 BODY.PEEK[HEADER]", 1},
		{"FETCH that sets Seen", "UID FETCH 10 BODY[TEXT]", 1},
		{"FETCH of a macro", "UID FETCH 10:20 FAST", 2},
		{"STORE", `UID STORE 10:20 +FLAGS (\Answered)`, 2},
		{"STORE replacing", `UID STORE 10 FLAGS (\Answered)`, 1},
		{"STORE removing", `UID STORE 10 -FLAGS (\Answered)`, 1},
		{"STORE keyword", `UID STORE 10 +FLAGS (Later)`, 1},
		{"SEARCH", "UID SEARCH ALL", 0},
		{"COPY", "UID COPY 10:20 INBOX", 0},
		{"silent STORE", `UID STORE 10 +FLAGS.SILENT (\Answered)`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			box := messageDataBox()
			c := fetchClient(t, box, "SELECT")
			box.messages[1].Flagged = false

			responses := c.exchange("U1", "%s", tc.command)
			if last := responses[len(responses)-1]; last.kind != "OK" {
				t.Fatalf("%s: %+v", tc.command, last)
			}
			var fetches int
			for _, response := range responses {
				if response.kind != "FETCH" {
					continue
				}
				fetches++
				if !response.fetch.has("UID") {
					t.Fatalf("FETCH without UID: %+v", response.fetch)
				}
				if want := uint32(10); response.number == 1 && response.fetch.uid != want {
					t.Fatalf("UID %d of message 1, want %d", response.fetch.uid, want)
				}
				if want := uint32(20); response.number == 2 && response.fetch.uid != want {
					t.Fatalf("UID %d of message 2, want %d", response.fetch.uid, want)
				}
			}
			if fetches != tc.fetches {
				t.Fatalf("%d FETCH responses, want %d: %+v", fetches, tc.fetches, responses)
			}
		})
	}
}
