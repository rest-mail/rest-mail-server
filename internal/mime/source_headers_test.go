package mime

import (
	"bytes"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
)

func TestClassifySourceHeaders(t *testing.T) {
	for _, test := range []struct {
		name, source, want string
	}{
		{"empty", "", rmail.ClassPerson},
		{"no header section", "\r\nList-Id: <news.example.test>\r\n", rmail.ClassPerson},
		{"plain", "From: a@example.test\r\nSubject: Hi\r\n\r\nhello\r\n", rmail.ClassPerson},
		{"list id", "List-Id: <news.example.test>\r\n\r\n", rmail.ClassNewsletter},
		{"header section ends without a blank line", "List-Id: <news.example.test>\r\n", rmail.ClassNewsletter},
		{"line feed line endings", "List-Id: <news.example.test>\n\nhello\n", rmail.ClassNewsletter},
		{"name case", "lIsT-uNsUbScRiBe: <https://example.test/leave>\r\n\r\n", rmail.ClassNewsletter},
		{"folded", "List-Id: A list\r\n\t<news.example.test>\r\n\r\n", rmail.ClassNewsletter},
		{"folded twice", "List-Unsubscribe: <mailto:leave@example.test>,\r\n  \r\n <https://example.test/leave>\r\n\r\n", rmail.ClassNewsletter},
		{"precedence after other fields", "From: a@example.test\r\nReceived: from a\r\n by b\r\nPrecedence: BULK\r\n\r\n", rmail.ClassNotification},
		{"same field twice is ignored", "List-Id: <a.example.test>\r\nlist-id: <b.example.test>\r\n\r\n", rmail.ClassPerson},
		{"a signal in the body does not count", "From: a@example.test\r\n\r\nList-Id: <news.example.test>\r\n", rmail.ClassPerson},
		{"a signal in a folded value does not count", "Subject: hello\r\n List-Id: <news.example.test>\r\n\r\n", rmail.ClassPerson},
		{"a signal after a malformed field is not seen", "From: a@example.test\r\nnot a field\r\nList-Id: <news.example.test>\r\n\r\n", rmail.ClassPerson},
		{"a signal before a malformed field counts", "List-Id: <news.example.test>\r\nnot a field\r\n\r\n", rmail.ClassNewsletter},
		{"an empty name, which the parser passes", "From: a@example.test\r\n: x\r\nList-Id: <news.example.test>\r\n\r\n", rmail.ClassNewsletter},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := rmail.Classify(SourceHeaders(strings.NewReader(test.source)), ""); got != test.want {
				t.Fatalf("class = %q, want %q", got, test.want)
			}
		})
	}
}

// A row's class comes from its kept fields or from its source, and which of those is up to how long
// a field the sender wrote. So they have to be the same fields, whatever the message is.
func TestSourceHeadersAreTheFieldsAParseKeeps(t *testing.T) {
	check := func(raw []byte) {
		t.Helper()
		parsed, err := ParseHeaders(bytes.NewReader(raw))
		if err != nil {
			return
		}
		got := SourceHeaders(bytes.NewReader(raw))
		if len(got) == 0 && len(parsed.Raw) == 0 {
			return
		}
		if !reflect.DeepEqual(got, parsed.Raw) {
			t.Fatalf("%q:\nfrom the source %q\nkept %q", raw, got, parsed.Raw)
		}
	}
	seeds := entitySeeds()
	for _, seed := range seeds {
		check([]byte(seed))
	}
	check([]byte("From: a@example.test\r\n: x\r\nAuthentication-Results: other\r\nList-Id: <news.example.test>\r\n\r\nb\r\n"))
	check([]byte("X-Long: " + strings.Repeat("yyyyyyy\r\n ", 150_000) + "y\r\nList-Id: <news.example.test>\r\n\r\nb\r\n"))
	random := rand.New(rand.NewSource(1915))
	for range entityMutations() {
		check(mutateEntity(random, seeds[random.Intn(len(seeds))]))
	}
}
