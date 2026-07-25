package queue

import "testing"

// TestMessageRequires8BitTransport is the red-green guard for the 8BITMIME relay
// backlog item: the worker must know whether a message may only be relayed to a
// next hop that advertises 8BITMIME (RFC 6152). This is true when the client
// declared BODY=8BITMIME/BINARYMIME, OR when the raw bytes actually contain an
// octet >= 0x80 (a client may under-declare a 7BIT body that is really 8-bit).
func TestMessageRequires8BitTransport(t *testing.T) {
	cases := []struct {
		name     string
		bodyType string
		raw      string
		want     bool
	}{
		{"declared 8BITMIME, ascii body", "8BITMIME", "Subject: hi\r\n\r\nplain\r\n", true},
		{"declared BINARYMIME", "BINARYMIME", "plain\r\n", true},
		{"declared lower-case 8bitmime", "8bitmime", "plain\r\n", true},
		{"declared with surrounding space", " 8BITMIME ", "plain\r\n", true},
		{"declared 7BIT, ascii body", "7BIT", "Subject: hi\r\n\r\nplain ascii\r\n", false},
		{"no declaration, ascii body", "", "Subject: hi\r\n\r\nplain ascii\r\n", false},
		{"no declaration, 8-bit body", "", "Subject: caf\xc3\xa9\r\n\r\nbody\r\n", true},
		{"under-declared 7BIT but actually 8-bit", "7BIT", "body \xe9 accent\r\n", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := messageRequires8BitTransport(c.bodyType, c.raw); got != c.want {
				t.Errorf("messageRequires8BitTransport(%q, %q) = %v, want %v",
					c.bodyType, c.raw, got, c.want)
			}
		})
	}
}
