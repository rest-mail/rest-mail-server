package server

import (
	"strings"
	"testing"
)

// partialRaw is the raw message of seq 1 (UID 5) as seeded by seedThree, used to
// compute the exact octet slices a partial fetch must return.
const partialRaw = "Subject: Msg 5\r\n\r\nbody 5\r\n"

// A partial body fetch "BODY[section]<start.count>" must return at most count
// octets beginning at zero-based octet start (fewer if the section is shorter,
// empty if start is past the end), and label the response with the origin octet
// only — "BODY[section]<start>", never "<start.count>" (RFC 3501 §6.4.5,
// §7.4.2). On the pre-fix code the "<start.count>" suffix was parsed-but-ignored:
// the whole section was returned, labelled "BODY[section]" with no origin.
func TestIMAP_FetchBodyPartial_Range(t *testing.T) {
	for _, tc := range []struct {
		name    string
		item    string
		wantLbl string // label expected in the untagged response line
		want    string // exact octets expected in the literal payload
	}{
		{"zero start", "BODY.PEEK[]<0.10>", "BODY[]<0>", partialRaw[0:10]},
		{"non-zero start", "BODY.PEEK[]<5.10>", "BODY[]<5>", partialRaw[5:15]},
		{"start past EOF is empty", "BODY.PEEK[]<100.10>", "BODY[]<100>", ""},
		{"count past EOF clamps to section", "BODY.PEEK[]<20.100>", "BODY[]<20>", partialRaw[20:]},
		{"TEXT section partial", "BODY.PEEK[TEXT]<0.4>", "BODY[TEXT]<0>", "body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockBackend()
			seedThree(m)
			h := newIMAPHarness(t, m)
			h.login("p1")
			h.selectInbox("p2")

			untagged, status := h.command("p3", "FETCH 1 %s", tc.item)
			if !strings.Contains(status, " OK") {
				t.Fatalf("FETCH 1 %s status = %q", tc.item, status)
			}
			resp := strings.Join(untagged, "\n")
			// The response item name must label the origin octet, e.g. BODY[]<0>.
			if !strings.Contains(resp, tc.wantLbl+" {") {
				t.Errorf("FETCH %s: response must label origin octet %q; got: %q", tc.item, tc.wantLbl, resp)
			}
			// The count must NOT appear in the response label (only <start>).
			if strings.Contains(resp, tc.wantLbl+".") {
				t.Errorf("FETCH %s: response label must carry <start> only, not <start.count>; got: %q", tc.item, resp)
			}
			if h.lastLiteral != tc.want {
				t.Errorf("FETCH %s: payload = %q (%d octets), want %q (%d octets)",
					tc.item, h.lastLiteral, len(h.lastLiteral), tc.want, len(tc.want))
			}
		})
	}
}

// A non-partial body fetch must be untouched by the partial-fetch support: the
// whole section is returned and the response label carries no "<origin>"
// (RFC 3501 §7.4.2: "a BODY[] is NEVER truncated").
func TestIMAP_FetchBody_NonPartial_Unlabeled(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newIMAPHarness(t, m)
	h.login("np1")
	h.selectInbox("np2")

	untagged, status := h.command("np3", "FETCH 1 BODY.PEEK[]")
	if !strings.Contains(status, " OK") {
		t.Fatalf("FETCH 1 BODY.PEEK[] status = %q", status)
	}
	resp := strings.Join(untagged, "\n")
	if !strings.Contains(resp, "BODY[] {") {
		t.Errorf("non-partial fetch must label BODY[] with no origin; got: %q", resp)
	}
	if strings.Contains(resp, "BODY[]<") {
		t.Errorf("non-partial fetch must NOT carry an origin octet; got: %q", resp)
	}
	if h.lastLiteral != partialRaw {
		t.Errorf("non-partial payload = %q, want full raw %q", h.lastLiteral, partialRaw)
	}
}
