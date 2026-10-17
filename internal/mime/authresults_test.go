package mime

import "testing"

// TestStripAuthResults checks which Authentication-Results fields the
// incoming-header scrub removes: every one claiming our authserv-id, however it
// is spelled or folded, and every one whose authserv-id cannot be read, while
// other servers' results, other fields and the body are kept byte for byte.
func TestStripAuthResults(t *testing.T) {
	const tail = "From: a@victim.example\r\nSubject: hi\r\n\r\nAuthentication-Results: restmail; spf=pass\r\n"
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "Authentication-Results: restmail; spf=pass; dkim=pass; dmarc=pass\r\n" + tail, tail},
		{"case", "authentication-results: RestMail; spf=pass\r\n" + tail, tail},
		{"space before colon", "Authentication-Results : restmail; spf=pass\r\n" + tail, tail},
		{"comment", "Authentication-Results: (ours) restmail (really); spf=pass\r\n" + tail, tail},
		{"nested comment", "Authentication-Results: (a (b) c) restmail; spf=pass\r\n" + tail, tail},
		{"quoted", "Authentication-Results: \"rest\\mail\"; spf=pass\r\n" + tail, tail},
		{"version", "Authentication-Results: restmail 1; spf=pass\r\n" + tail, tail},
		{"no space", "Authentication-Results:restmail;spf=pass\r\n" + tail, tail},
		{"folded", "Authentication-Results:\r\n restmail;\r\n\tspf=pass smtp.mailfrom=victim.example;\r\n\tdkim=pass header.d=victim.example\r\n" + tail, tail},
		{"bare LF", "Authentication-Results: restmail; spf=pass\nFrom: a@victim.example\n\nbody\n", "From: a@victim.example\n\nbody\n"},
		{"unclosed comment", "Authentication-Results: (restmail; spf=pass\r\n" + tail, tail},
		{"empty", "Authentication-Results:\r\n" + tail, tail},
		{"no authserv-id", "Authentication-Results: ; spf=pass\r\n" + tail, tail},
		{"among others", "Received: from x\r\nAuthentication-Results: restmail; spf=pass\r\nX-A: 1\r\nAuthentication-Results: restmail; dkim=pass\r\n" + tail, "Received: from x\r\nX-A: 1\r\n" + tail},
		{"another server's", "Authentication-Results: mx.example.net; spf=pass\r\n" + tail, "Authentication-Results: mx.example.net; spf=pass\r\n" + tail},
		{"lookalike", "Authentication-Results: restmail.example; spf=pass\r\n" + tail, "Authentication-Results: restmail.example; spf=pass\r\n" + tail},
		{"named in a comment only", "Authentication-Results: mx.example.net (restmail); spf=pass\r\n" + tail, "Authentication-Results: mx.example.net (restmail); spf=pass\r\n" + tail},
		{"ARC", "ARC-Authentication-Results: i=1; restmail; spf=pass\r\n" + tail, "ARC-Authentication-Results: i=1; restmail; spf=pass\r\n" + tail},
		{"none", tail, tail},
		{"no body", "Authentication-Results: restmail; spf=pass", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := StripAuthResults(c.in, "restmail"); got != c.want {
				t.Errorf("StripAuthResults(%q)\n got %q\nwant %q", c.in, got, c.want)
			}
		})
	}
}
