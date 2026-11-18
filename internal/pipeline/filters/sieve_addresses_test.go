package filters

import (
	"math/rand/v2"
	"mime"
	"net/mail"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/rest-mail/go-sieve/pkg/sieve"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// sieveAddresses is what the library was given for a list before it could ask: every address, built
// from All. These tests hold what it reads now to that.
func sieveAddresses(addrs []pipeline.Address) []sieve.Address {
	if len(addrs) == 0 {
		return nil
	}
	out := make([]sieve.Address, len(addrs))
	for i, a := range addrs {
		out[i] = sieve.Address{Name: a.Name, Address: a.Address}
	}
	return out
}

// addressFieldSeeds are the lists the tests below start from, with what they mutate them with.
var addressFieldSeeds = []string{
	"", ",", "a@b", "a@b,", "a@b, c@d", "A <a@b>, B <c@d>", "a@b (c), (d) e@f",
	"=?UTF-8?Q?caf=C3=A9?= <a@b>, =?x-unknown?Q?z?= <c@d>", "=?windows-1252?q?caf=E9?= <a@b>", "caf\xc3\xa9 <a@b>",
	"group: a@b, c@d;", "empty:;", `"q, r" <a@b>`, "<@r:a@b>", "a@[1.2.3.4], b@c", "a\r\n b@c, d@e",
	"=?UTF-8?Q?a=0D=0Ab?= <a@b>", "broken <a@b", "a@b, broken",
}

var addressFieldAlphabet = []string{",", ";", ":", "<", ">", "(", ")", "@", `"`, "\\", " ", "\r\n ", "=?", "?=", "a", "\xc3\xa9", "[", "]"}

// eachMutatedField calls check with every seed and with count mutations of it, the same ones each run.
func eachMutatedField(seed1 uint64, count int, check func(field string)) {
	random := rand.New(rand.NewPCG(seed1, 2107))
	for _, seed := range addressFieldSeeds {
		check(seed)
		for range count {
			var b strings.Builder
			at := random.IntN(len(seed) + 1)
			b.WriteString(seed[:at])
			b.WriteString(addressFieldAlphabet[random.IntN(len(addressFieldAlphabet))])
			b.WriteString(seed[min(len(seed), at+random.IntN(3)):])
			check(b.String())
		}
	}
}

// sieveActionLog keeps what a script chose, in order.
type sieveActionLog struct {
	sieve.Executor
	actions []string
}

func (r *sieveActionLog) Keep() { r.actions = append(r.actions, "keep") }
func (r *sieveActionLog) FileInto(folder string, _ bool) {
	r.actions = append(r.actions, "fileinto:"+folder)
}

// sieveTestScripts are scripts of each test that reads an address field, and of one that reads none.
func sieveTestScripts(t *testing.T) (map[string]*sieve.Script, []string) {
	t.Helper()
	tests := []string{
		`address :is "to" "a@b"`, `address :is :localpart "to" "a"`, `address :contains :domain ["to","cc"] "b"`,
		`address :matches :all "from" "*@*"`, `address :is "reply-to" "a@b"`, `address :is "sender" "c@d"`,
		`address :is ["bcc","resent-to"] "a@b"`, `address :is "TO" "A@B"`, `address :is :comparator "i;octet" "to" "a@b"`,
		`address :is "subject" "a@b"`, `address :contains :localpart "cc" "q"`, `not address :is "to" "a@b"`,
		`address :is "to" "f@g"`, `address :is "to" "q@r"`, `address :is "from" "f@g"`, `address :is "cc" "q@r"`, `address :is "cc" "f@g"`,
		`header :contains "to" "a"`, `header :is "from" "A <a@b>"`, `header :contains ["cc","bcc"] "@"`,
		`header :contains "to" "f@g"`, `header :contains "from" "f@g"`, `header :contains "cc" "q@r"`,
		`exists "to"`, `exists ["cc","bcc"]`, `size :over 60`, `size :under 90`,
	}
	scripts := make(map[string]*sieve.Script, len(tests))
	for _, test := range tests {
		script, err := sieve.Parse(`require ["fileinto"]; if ` + test + ` { fileinto "hit"; }`)
		if err != nil {
			t.Fatalf("%s: %v", test, err)
		}
		scripts[test] = script
	}
	return scripts, tests
}

// builtSieveMessage is msg as the library was given it before it could ask for a list.
func builtSieveMessage(email *pipeline.EmailJSON) *sieve.Message {
	msg := sieveMessage(email)
	msg.Headers.Lists = nil
	msg.Headers.From = sieveAddressList(email.Headers.From)
	msg.Headers.To = sieveAddressList(email.Headers.To)
	msg.Headers.Cc = sieveAddressList(email.Headers.Cc)
	msg.Headers.Bcc = sieveAddresses(email.Headers.Bcc)
	return msg
}

// A script answers the same when its lists are read as it asks as it did with all of them built, on
// lists that read and lists that don't, from a real parse and from the fields alone.
func TestSieveListsAnswerAsBuiltLists(t *testing.T) {
	scripts, tests := sieveTestScripts(t)
	dec := &mime.WordDecoder{}
	checks, hits := 0, 0
	compare := func(field string, email *pipeline.EmailJSON) {
		t.Helper()
		lazy, built := sieveMessage(email), builtSieveMessage(email)
		for _, test := range tests {
			got, want := &sieveActionLog{}, &sieveActionLog{}
			scripts[test].Evaluate(lazy, got)
			scripts[test].Evaluate(built, want)
			checks++
			if !slices.Equal(got.actions, want.actions) {
				t.Fatalf("%q under %s: read as asked %v, built %v", field, test, got.actions, want.actions)
			}
			if slices.Contains(got.actions, "fileinto:hit") {
				hits++
			}
		}
	}
	eachMutatedField(1915, 120, func(field string) {
		// Each field differs from the others, so that reading the wrong one shows.
		sender, other := "f@g, "+field, "q@r, "+field
		email := &pipeline.EmailJSON{}
		email.Headers.Subject = "a@b"
		email.Headers.From = pipeline.ParsedAddresses(sender, dec)
		email.Headers.To = pipeline.ParsedAddresses(field, dec)
		email.Headers.Cc = pipeline.ParsedAddresses(other, dec)
		email.Headers.Bcc = []pipeline.Address{{Address: "a@b"}, {Name: "only a name"}}
		email.Headers.Raw = map[string][]string{"To": {field, "c@d"}, "From": {sender}, "Cc": {other}, "Reply-To": {field}, "sender": {field}, "Resent-To": {other}}
		compare(field, email)
		parsed, err := rmime.Parse([]byte("From: " + sender + "\r\nTo: " + field + "\r\nCc: " + other + "\r\nReply-To: " + field + "\r\nSubject: a@b\r\n\r\nb\r\n"))
		if err == nil {
			compare(field, parsed)
		}
	})
	t.Logf("%d evaluations, %d matched", checks, hits)
	if hits == 0 || hits == checks {
		t.Fatalf("%d of %d evaluations matched, so this tells the readings apart from nothing", hits, checks)
	}
}

// A raw field is read as net/mail reads it, which is what the library read it with: the same addresses
// in the same order, and none at all when any of the field doesn't read.
func TestSieveRawSpecsReadAsNetMail(t *testing.T) {
	lists, read, refused := newSieveLists(&pipeline.EmailJSON{}), 0, 0
	eachMutatedField(2107, 2000, func(field string) {
		var want []string
		if addresses, err := mail.ParseAddressList(field); err == nil {
			read++
			for _, address := range addresses {
				want = append(want, address.Address)
			}
		} else {
			refused++
		}
		var got []string
		lists.RawSpecs(field, func(spec string) bool {
			got = append(got, spec)
			return true
		})
		if !slices.Equal(got, want) {
			t.Fatalf("%q: read %q, net/mail reads %q", field, got, want)
		}
	})
	t.Logf("%d fields read, %d refused", read, refused)
	if read == 0 || refused == 0 {
		t.Fatalf("%d read and %d refused, so this compares one kind of field", read, refused)
	}
}

// A list that goes wrong partway has no addresses, however many came before the fault, and a visit
// that answers false is the last.
func TestSieveListSpecsAreAllOrNothing(t *testing.T) {
	dec := &mime.WordDecoder{}
	specs := func(lists *sieveLists, stopAfter int) []string {
		var got []string
		lists.Specs("to", func(spec string) bool {
			got = append(got, spec)
			return stopAfter == 0 || len(got) < stopAfter
		})
		return got
	}
	email := &pipeline.EmailJSON{}
	email.Headers.To = pipeline.ParsedAddresses("a@b, c@d, broken", dec)
	if got := specs(newSieveLists(email), 0); got != nil {
		t.Fatalf("a field that doesn't read gave %q", got)
	}
	email.Headers.To = pipeline.ParsedAddresses("a@b, c@d, e@f", dec)
	if got := specs(newSieveLists(email), 0); !slices.Equal(got, []string{"a@b", "c@d", "e@f"}) {
		t.Fatalf("read %q", got)
	}
	if got := specs(newSieveLists(email), 2); !slices.Equal(got, []string{"a@b", "c@d"}) {
		t.Fatalf("a visit that stops read %q", got)
	}
}

// What a script evaluates is the message as it was when evaluation began, and a list a script reads
// whole many times is built for the first of them only.
func TestSieveListsKeepTheirFirstReading(t *testing.T) {
	dec := &mime.WordDecoder{}
	email := &pipeline.EmailJSON{}
	email.Headers.To = pipeline.ParsedAddresses("a@b", dec)
	lists := newSieveLists(email)
	email.Headers.To = pipeline.ParsedAddresses("later@b", dec)
	first := lists.All("to")
	if len(first) != 1 || first[0].Address != "a@b" {
		t.Fatalf("read %+v", first)
	}
	if again := lists.All("to"); &again[0] != &first[0] {
		t.Fatal("the list was built a second time")
	}
	if got := lists.All("resent-to"); got != nil {
		t.Fatalf("a field that isn't a list read as %+v", got)
	}
}

// evaluationCost is what evaluating script allocates, the least of a few tries as a collection or
// another test can add to one.
func evaluationCost(t *testing.T, script string, msg func() *sieve.Message) uint64 {
	t.Helper()
	parsed, err := sieve.Parse(script)
	if err != nil {
		t.Fatal(err)
	}
	best := uint64(1) << 63
	for range 3 {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		parsed.Evaluate(msg(), &sieveActionLog{})
		runtime.ReadMemStats(&after)
		best = min(best, after.TotalAlloc-before.TotalAlloc)
	}
	return best
}

// A very long list costs a script that doesn't read it nothing, and costs an address test nothing for
// each address. Building the lists first, as the library had to be given them, costs many times the
// field for either, so this is what fails if the lists are built again.
func TestSieveAddressTestHoldsNoList(t *testing.T) {
	const size = 1 << 20
	field := strings.Repeat("a@b,", size/4) + "a@b"
	email := &pipeline.EmailJSON{}
	email.Headers.Subject = "s"
	email.Headers.To = pipeline.ParsedAddresses(field, &mime.WordDecoder{})
	email.Headers.Raw = map[string][]string{"To": {field}}
	for name, script := range map[string]string{
		"a test of another field": `if header :is "subject" "x" { keep; }`,
		"an address test":         `if address :is "to" "nobody@example.test" { keep; }`,
	} {
		t.Run(name, func(t *testing.T) {
			lazy := evaluationCost(t, script, func() *sieve.Message { return sieveMessage(email) })
			built := evaluationCost(t, script, func() *sieve.Message { return builtSieveMessage(email) })
			t.Logf("%d bytes of list: %d allocated read as asked, %d with the lists built", len(field), lazy, built)
			if lazy >= uint64(len(field))/8 {
				t.Errorf("%d bytes of list allocated %d", len(field), lazy)
			}
			if built < uint64(len(field)) {
				t.Errorf("building the lists allocated only %d, so this can't tell the two apart", built)
			}
		})
	}
}

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
