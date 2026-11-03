package rfctest

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// mimeDepthLimit mirrors the engine's MIME nesting bound: the top-level
// message is depth 0 and each multipart part or MESSAGE/RFC822 encapsulation
// adds one. A multipart or MESSAGE/RFC822 entity at this depth is opaque.
const mimeDepthLimit = 64

// depthLeaf is a base64 text leaf; SEARCH finds "needle" only by decoding it.
const (
	depthLeafHeader = "Content-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n"
	depthLeafBody   = "bmVlZGxl"
)

// multipartChain nests levels multipart/mixed entities, each the sole part of
// the one above, around depthLeaf. It returns the raw message and the body
// octets of the entity at every depth (the leaf is at depth levels).
func multipartChain(levels int) (raw string, bodies []string) {
	bodies = make([]string, levels+1)
	bodies[levels] = depthLeafBody
	header := func(d int) string {
		if d == levels {
			return depthLeafHeader
		}
		return fmt.Sprintf("Content-Type: multipart/mixed; boundary=b%d\r\n", d)
	}
	for d := levels - 1; d >= 0; d-- {
		bodies[d] = fmt.Sprintf("--b%d\r\n%s\r\n%s\r\n--b%d--", d, header(d+1), bodies[d+1], d)
	}
	return "Subject: deep\r\n" + header(0) + "\r\n" + bodies[0] + "\r\n", bodies
}

// multipartChainStructure is the expected BODY or BODYSTRUCTURE of
// multipartChain(levels): entities at the depth limit are APPLICATION/OCTET-STREAM.
func multipartChainStructure(levels int, bodies []string, extension bool) string {
	ext1 := ""
	if extension {
		ext1 = " NIL NIL NIL NIL"
	}
	var b strings.Builder
	var describe func(d int)
	describe = func(d int) {
		switch {
		case d == levels:
			fmt.Fprintf(&b, `("TEXT" "PLAIN" ("CHARSET" "US-ASCII") NIL NIL "BASE64" %d 1%s)`, len(bodies[d]), ext1)
		case d >= mimeDepthLimit:
			fmt.Fprintf(&b, `("APPLICATION" "OCTET-STREAM" NIL NIL NIL "7BIT" %d%s)`, len(bodies[d]), ext1)
		default:
			b.WriteByte('(')
			describe(d + 1)
			b.WriteString(` "MIXED"`)
			if extension {
				fmt.Fprintf(&b, ` ("BOUNDARY" "b%d") NIL NIL NIL`, d)
			}
			b.WriteByte(')')
		}
	}
	describe(0)
	return b.String()
}

func depthMailbox(raw string) *fetchMailbox {
	box := newFetchMailbox()
	box.messages = []imap.Message{{UID: 10, Size: len(raw), Date: time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)}}
	box.raw[10] = raw
	return box
}

func onesPath(n int) string {
	return strings.TrimSuffix(strings.Repeat("1.", n), ".")
}

// TestMIMEDepthLimit checks that excessive MIME nesting (RFC 3501 §7.4.2;
// RFC 2046 §§5.1, 5.2.1) is handled consistently. Every multipart and
// MESSAGE/RFC822 entity above depth 64 is described recursively. One at depth
// 64 is an opaque APPLICATION/OCTET-STREAM leaf in BODY and BODYSTRUCTURE. A
// numeric FETCH section returns that leaf's octets and nothing deeper, and
// SEARCH BODY matches its stored octets without decoding what it contains.
//
// checklist: IMAP-MSG-10
func TestMIMEDepthLimit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		levels int
		found  bool // SEARCH BODY finds the decoded leaf text
	}{
		{"within-limit", mimeDepthLimit, true},
		{"beyond-limit", mimeDepthLimit + 1, false},
	} {
		t.Run("multipart/"+tc.name, func(t *testing.T) {
			raw, bodies := multipartChain(tc.levels)
			structureResponse(t, raw,
				multipartChainStructure(tc.levels, bodies, false),
				multipartChainStructure(tc.levels, bodies, true))

			// BODY[1.1...1] with k parts names the entity at depth k.
			// The deepest reachable entity is the leaf, or the opaque multipart.
			deepest, mimeHeader := tc.levels, depthLeafHeader+"\r\n"
			if !tc.found {
				deepest = mimeDepthLimit
				mimeHeader = fmt.Sprintf("Content-Type: multipart/mixed; boundary=b%d\r\n\r\n", deepest)
			}
			for _, sc := range []struct{ section, payload string }{
				{onesPath(deepest), bodies[deepest]},
				{onesPath(deepest) + ".MIME", mimeHeader},
				{onesPath(deepest + 1), ""},
			} {
				box := depthMailbox(raw)
				c := fetchClient(t, box, "EXAMINE")
				replies, tagged := fetchCommand(t, c, "FETCH 1 BODY.PEEK["+sc.section+"]")
				requireOK(t, tagged, "F1")
				label := fmt.Sprintf("BODY[%s] {%d}", sc.section, len(sc.payload))
				if len(replies) != 1 || !slices.Equal(replies[0].literals, []string{sc.payload}) || !strings.Contains(replies[0].framing, label) {
					t.Fatalf("BODY[%s] = %#v, want %q with payload %q", sc.section, replies, label, sc.payload)
				}
			}

			// Beyond the limit the opaque entity's stored octets, including
			// its inner delimiter, are searched instead of the decoded leaf.
			decoded, stored := "* SEARCH 1", "* SEARCH"
			if !tc.found {
				decoded, stored = stored, decoded
			}
			c := fetchClient(t, depthMailbox(raw), "EXAMINE")
			requireSearch(t, c, `SEARCH BODY "needle"`, decoded)
			requireSearch(t, c, fmt.Sprintf(`SEARCH BODY "--b%d"`, mimeDepthLimit), stored)
			requireSearch(t, c, `SEARCH BODY "`+depthLeafBody+`"`, stored)
		})

		t.Run("message/"+tc.name, func(t *testing.T) {
			// Each MESSAGE/RFC822 encapsulation is one level: the message at
			// depth d declares Content-Type message/rfc822 and its body is the
			// message at depth d+1.
			raw := strings.Repeat("Content-Type: message/rfc822\r\n\r\n", tc.levels) + depthLeafHeader + "\r\n" + depthLeafBody + "\r\n"
			c := fetchClient(t, depthMailbox(raw), "EXAMINE")
			replies, tagged := fetchCommand(t, c, "FETCH 1 (BODY BODYSTRUCTURE)")
			requireOK(t, tagged, "F1")
			if len(replies) != 1 {
				t.Fatalf("FETCH = %#v", replies)
			}
			opaque := 0
			if !tc.found {
				opaque = 2
			}
			if got := strings.Count(replies[0].framing, `("MESSAGE" "RFC822"`); got != 2*mimeDepthLimit {
				t.Fatalf("MESSAGE/RFC822 levels = %d, want %d in %q", got, 2*mimeDepthLimit, replies[0].framing)
			}
			if got := strings.Count(replies[0].framing, `("APPLICATION" "OCTET-STREAM"`); got != opaque {
				t.Fatalf("opaque leaves = %d, want %d", got, opaque)
			}
			if got, want := strings.Count(replies[0].framing, `"BASE64"`), 2-opaque; got != want {
				t.Fatalf("decoded leaves = %d, want %d", got, want)
			}
			want := "* SEARCH"
			if tc.found {
				want = "* SEARCH 1"
			}
			requireSearch(t, c, `SEARCH BODY "needle"`, want)
		})
	}
}
