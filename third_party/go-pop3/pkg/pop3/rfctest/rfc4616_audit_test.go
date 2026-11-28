package rfctest

import (
	"encoding/base64"
	"strings"
	"testing"
)

// RFC 4616 §2: servers MUST accept authzid, authcid and passwd up to and
// including 255 octets, and a UTF-8 character may be as long as 4 octets.
// Each case fills all three fields to exactly 255 octets with 2-, 3- and
// 4-octet characters, so the limit is counted in octets, not characters, and
// the fields reach the Backend intact.
// checklist: POP3-PLAIN-01
func TestRFC4616MultibyteFieldLimits(t *testing.T) {
	for name, field := range map[string]string{
		"two-octet":   strings.Repeat("é", 127) + "a",
		"three-octet": strings.Repeat("€", 85),
		"four-octet":  strings.Repeat("😀", 63) + "abc",
	} {
		t.Run(name, func(t *testing.T) {
			if len(field) != 255 {
				t.Fatalf("fixture field is %d octets, want 255", len(field))
			}
			b := &maxPlainBackend{backend: backend{password: "secret"}, id: field, pass: field}
			w := connect(t, b)
			expect(t, w.cmd("AUTH PLAIN"), "+ ")
			expect(t, w.cmd(plain(field, field, field)), "+OK Authentication successful")
			expect(t, w.cmd("STAT"), "+OK 1 42")
			if b.calls != 1 {
				t.Fatalf("AuthenticatePlain calls = %d, want 1", b.calls)
			}
		})
	}
}

// RFC 4616 §2: every field is 1*SAFE, UTF-8 without NUL. Overlong forms,
// surrogates, code points above U+10FFFF, stray continuation and truncated
// sequences are outside the ABNF in each field, and an extra NUL makes a
// fourth field. None reach the Backend, and a fresh AUTH then succeeds.
// checklist: POP3-PLAIN-02 POP3-PLAIN-03
func TestRFC4616RejectsMalformedUTF8(t *testing.T) {
	good := plain("", "alice", "secret")
	for name, bad := range map[string]string{
		"overlong-nul": "\xc0\x80",
		"overlong-a":   "\xc1\xa1",
		"surrogate":    "\xed\xa0\x80",
		"above-max":    "\xf4\x90\x80\x80",
		"continuation": "\x80",
		"truncated":    "\xe2\x82",
		"latin1":       "\xe9",
	} {
		for _, field := range []string{"authzid", "authcid", "passwd"} {
			t.Run(name+"/"+field, func(t *testing.T) {
				authzid, authcid, pass := "", "alice", "secret"
				switch field {
				case "authzid":
					authzid = "alice" + bad
				case "authcid":
					authcid += bad
				default:
					pass += bad
				}
				b := &saslBackend{backend: backend{password: "secret"}}
				w := connect(t, b)
				expect(t, w.cmd("AUTH PLAIN "+plain(authzid, authcid, pass)), "-ERR Invalid PLAIN response")
				b.expectCalls(t)
				expect(t, w.cmd("AUTH PLAIN "+good), "+OK Authentication successful")
			})
		}
	}
	t.Run("nul-in-passwd", func(t *testing.T) {
		b := &saslBackend{backend: backend{password: "secret"}}
		w := connect(t, b)
		message := base64.StdEncoding.EncodeToString([]byte("\x00alice\x00sec\x00ret"))
		expect(t, w.cmd("AUTH PLAIN "+message), "-ERR Invalid PLAIN response")
		b.expectCalls(t)
		expect(t, w.cmd("AUTH PLAIN "+good), "+OK Authentication successful")
	})
}
