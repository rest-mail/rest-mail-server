package filters

import (
	"math/rand/v2"
	"mime"
	"reflect"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// What the library is given is what All gave it, on lists that read and lists that don't.
func TestSieveAddressListMatchesAll(t *testing.T) {
	seeds := []string{
		"", ",", "a@b", "a@b,", "a@b, c@d", "A <a@b>, B <c@d>", "a@b (c), (d) e@f",
		"=?UTF-8?Q?caf=C3=A9?= <a@b>, =?x-unknown?Q?z?= <c@d>", "caf\xc3\xa9 <a@b>",
		"group: a@b, c@d;", "empty:;", `"q, r" <a@b>`, "<@r:a@b>", "a@[1.2.3.4], b@c", "a\r\n b@c, d@e",
		"=?UTF-8?Q?a=0D=0Ab?= <a@b>", "broken <a@b", "a@b, broken",
	}
	alphabet := []string{",", ";", ":", "<", ">", "(", ")", "@", `"`, "\\", " ", "\r\n ", "=?", "?=", "a", "\xc3\xa9", "[", "]"}
	random := rand.New(rand.NewPCG(1915, 2026))
	dec := &mime.WordDecoder{}
	check := func(field string) {
		t.Helper()
		list := pipeline.ParsedAddresses(field, dec)
		if got, want := sieveAddressList(list), sieveAddresses(list.All()); !reflect.DeepEqual(got, want) {
			t.Fatalf("%q: got %+v, want %+v", field, got, want)
		}
	}
	for _, seed := range seeds {
		check(seed)
		for range 2000 {
			var b strings.Builder
			at := random.IntN(len(seed) + 1)
			b.WriteString(seed[:at])
			b.WriteString(alphabet[random.IntN(len(alphabet))])
			b.WriteString(seed[min(len(seed), at+random.IntN(3)):])
			check(b.String())
		}
	}
	for _, given := range [][]pipeline.Address{nil, {}, {{Name: "n", Address: "a@b"}}, {{Name: "only"}, {Address: "a@b"}}} {
		list := pipeline.Addresses(given...)
		if got, want := sieveAddressList(list), sieveAddresses(list.All()); !reflect.DeepEqual(got, want) {
			t.Fatalf("%+v: got %+v, want %+v", given, got, want)
		}
	}
}
