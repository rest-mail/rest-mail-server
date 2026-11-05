package filters

import (
	"math/rand/v2"
	"mime"
	"reflect"
	"runtime"
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

// Other filters ask of From only whether it has addresses and what the first is, which for a list
// of comments or encoded names still reads each of them. That stays under 2x for each filter asking.
func TestFiltersAddressReadsStayCheap(t *testing.T) {
	const size = 1 << 20
	for name, one := range map[string]string{
		"plain":    "Some Name <someone@example.test>,",
		"comments": "someone@example.test (a comment),",
		"encoded":  "=?UTF-8?Q?Some_Name?= <someone@example.test>,",
		"8-bit":    "Gr\xc3\xbc\xc3\x9fe <someone@example.test>,",
		"short":    "a@b,",
	} {
		t.Run(name, func(t *testing.T) {
			field := strings.Repeat(one, size/len(one)) + "last@example.test"
			list := pipeline.ParsedAddresses(field, &mime.WordDecoder{})
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			count, first := list.Len(), list.FirstAddress()
			runtime.ReadMemStats(&after)
			allocated := after.TotalAlloc - before.TotalAlloc
			t.Logf("%d addresses, first %q: %d bytes, %.2fx", count, first, allocated, float64(allocated)/float64(len(field)))
			if count == 0 || first == "" {
				t.Fatalf("read %d, %q", count, first)
			}
			if allocated > 2*uint64(len(field)) {
				t.Errorf("reading %d bytes allocated %d", len(field), allocated)
			}
		})
	}
}
