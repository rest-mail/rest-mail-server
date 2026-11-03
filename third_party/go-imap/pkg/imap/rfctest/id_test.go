package rfctest

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/go-imap/pkg/imap/server"
)

// idBox is an account fixture that keeps what the session hands it through
// imap.IDMailbox.
type idBox struct {
	mailbox
	mu    sync.Mutex
	calls []imap.ClientID
}

func (b *idBox) SetClientID(id imap.ClientID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, id)
}

func (b *idBox) received() []imap.ClientID {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.calls)
}

// imapsyncID is the ID imapsync sends whenever the server advertises ID
// (imapsync's sub imap_id).
const imapsyncID = `ID ("name" "imapsync" "version" "2.290" "os" "linux" "vendor" "Gilles LAMIRAL" "support-url" "https://imapsync.lamiral.info/" "date" "22-Sep-2025 00:00:00 +0000")`

var imapsyncFields = imap.ClientID{
	{Name: "name", Value: "imapsync"},
	{Name: "version", Value: "2.290"},
	{Name: "os", Value: "linux"},
	{Name: "vendor", Value: "Gilles LAMIRAL"},
	{Name: "support-url", Value: "https://imapsync.lamiral.info/"},
	{Name: "date", Value: "22-Sep-2025 00:00:00 +0000"},
}

// TestIDExamples: RFC 2971's own examples. ID is advertised before and after
// authentication; the §3.1 example list is accepted and, with no identity
// configured, answered "* ID NIL" exactly as that example shows; the §3.2
// example "ID NIL" is answered with the server's list, holding only its name
// and version. ID is valid in the not-authenticated, authenticated and
// selected states.
//
// checklist: IMAP-ID-01 IMAP-ID-02 IMAP-ID-03
func TestIDExamples(t *testing.T) {
	c := dial(t)
	c.line()
	requireCaps(t, c.capabilities("C1"), []string{"ID"}, nil)
	lines, tagged := c.command("a023", `ID ("name" "sodr" "version" "19.34" "vendor"`+
		` "Pink Floyd Music Limited")`)
	requireOK(t, tagged, "a023")
	requireExactly(t, lines, "* ID NIL")
	if tagged != "a023 OK ID completed" {
		t.Fatalf("§3.1 example: %q", tagged)
	}

	named := dialConfigured(t, nil, nil, nil, func(s *server.Session) { s.SetServerID("REST Mail", "1.2.3") })
	named.line()
	want := `* ID ("name" "REST Mail" "version" "1.2.3")`
	lines, tagged = named.command("a042", "ID NIL")
	requireOK(t, tagged, "a042")
	requireExactly(t, lines, want)
	named.login()
	requireCaps(t, named.capabilities("C2"), []string{"ID"}, []string{"IMAP4rev2"})
	for _, state := range []string{"authenticated", "selected"} {
		if state == "selected" {
			if _, tagged := named.command("S1", "SELECT INBOX"); !strings.HasPrefix(tagged, "S1 OK") {
				t.Fatalf("SELECT: %q", tagged)
			}
		}
		lines, tagged = named.command("i1", "id nil")
		requireOK(t, tagged, "i1")
		requireExactly(t, lines, want)
		lines, tagged = named.command("i2", `ID ("name" "sodr")`)
		requireOK(t, tagged, "i2")
		requireExactly(t, lines, want)
	}
}

// TestIDStoredForTheMailbox: the client's fields reach an imap.IDMailbox in
// the order sent, whether ID came before or after LOGIN, with NIL values kept
// as NIL and a literal taken as a string. ID NIL hands over nothing, and a
// client that never sends ID is served as before.
//
// checklist: IMAP-ID-04
func TestIDStoredForTheMailbox(t *testing.T) {
	before := &idBox{}
	c := dialMailbox(t, before)
	c.line()
	if _, tagged := c.command("a1", "%s", imapsyncID); tagged != "a1 OK ID completed" {
		t.Fatalf("imapsync ID before login: %q", tagged)
	}
	if got := before.received(); len(got) != 0 {
		t.Fatalf("fields reached the mailbox before login: %v", got)
	}
	c.login()
	got := before.received()
	if len(got) != 1 || !slices.Equal(got[0], imapsyncFields) {
		t.Fatalf("after login the mailbox holds %v, want %v", got, imapsyncFields)
	}
	if name, ok := got[0].Get("NAME"); !ok || name != "imapsync" {
		t.Fatalf("Get(NAME) = %q %v", name, ok)
	}

	after := &idBox{}
	c = dialMailbox(t, after)
	c.line()
	c.login()
	if _, tagged := c.command("a2", "ID NIL"); tagged != "a2 OK ID completed" {
		t.Fatalf("ID NIL: %q", tagged)
	}
	if got := after.received(); len(got) != 0 {
		t.Fatalf("ID NIL handed over %v", got)
	}
	if _, tagged := c.command("a3", "ID (\"name\" {8+}\r\nimapsync \"os\" NIL)"); tagged != "a3 OK ID completed" {
		t.Fatalf("ID with a literal and NIL: %q", tagged)
	}
	want := imap.ClientID{{Name: "name", Value: "imapsync"}, {Name: "os", Nil: true}}
	if got := after.received(); len(got) != 1 || !slices.Equal(got[0], want) {
		t.Fatalf("after ID the mailbox holds %v, want %v", got, want)
	}
	if _, ok := want.Get("os"); ok {
		t.Fatal("Get reports a NIL value as sent")
	}

	silent := &idBox{}
	c = dialMailbox(t, silent)
	c.line()
	c.login()
	if _, tagged := c.command("s1", "SELECT INBOX"); !strings.HasPrefix(tagged, "s1 OK") {
		t.Fatalf("SELECT without ID: %q", tagged)
	}
	if _, tagged := c.command("s2", "FETCH 1 FLAGS"); !strings.HasPrefix(tagged, "s2 OK") {
		t.Fatalf("FETCH without ID: %q", tagged)
	}
	if got := silent.received(); len(got) != 0 {
		t.Fatalf("a client that sent no ID handed over %v", got)
	}
}

// TestIDLimits: RFC 2971 §3.3's bounds on the client's data, at most 30
// field-value pairs, field names at most 30 octets, values at most 1024 octets
// and no field name twice (in any case), and §4's grammar. Data at each bound is
// taken; past it, or malformed, the command is BAD (§3.1 "arguments invalid"),
// no ID response is sent, and the fields already held are kept.
//
// checklist: IMAP-ID-05
func TestIDLimits(t *testing.T) {
	pairs := func(n int) string {
		var b strings.Builder
		for i := range n {
			if i > 0 {
				b.WriteByte(' ')
			}
			fmt.Fprintf(&b, `"f%d" "v"`, i)
		}
		return "ID (" + b.String() + ")"
	}
	field30, value1024 := strings.Repeat("f", 30), strings.Repeat("v", 1024)
	accepted := []string{
		pairs(30),
		fmt.Sprintf(`ID (%q "v")`, field30),
		fmt.Sprintf(`ID ("name" %q)`, value1024),
		"ID ()",
	}
	refused := []string{
		pairs(31),
		fmt.Sprintf(`ID (%q "v")`, field30+"f"),
		fmt.Sprintf(`ID ("name" %q)`, value1024+"v"),
		`ID ("name" "a" "NAME" "b")`,
		"ID",
		"ID  NIL",
		`ID ("name")`,
		`ID (name "a")`,
		`ID ("name" "a" )`,
		`ID ("name" "a"`,
		`ID ("name"  "a")`,
		`ID "name" "a"`,
		"ID NIL NIL",
	}

	box := &idBox{}
	c := dialMailbox(t, box)
	c.line()
	c.login()
	for i, cmd := range accepted {
		tag := fmt.Sprintf("ok%d", i)
		lines, tagged := c.command(tag, "%s", cmd)
		if tagged != tag+" OK ID completed" || len(lines) != 1 {
			t.Fatalf("%.60s: %q %q", cmd, lines, tagged)
		}
	}
	held := box.received()
	for i, cmd := range refused {
		tag := fmt.Sprintf("bad%d", i)
		lines, tagged := c.command(tag, "%s", cmd)
		if !strings.HasPrefix(tagged, tag+" BAD ") || len(lines) != 0 {
			t.Fatalf("%.60s: %q %q, want BAD and no ID response", cmd, lines, tagged)
		}
	}
	if got := box.received(); len(got) != len(held) {
		t.Fatalf("a refused ID reached the mailbox: %d calls, want %d", len(got), len(held))
	}
	if len(held) != 3 || len(held[0]) != 30 || held[2][0].Value != value1024 {
		t.Fatalf("accepted IDs held %d calls", len(held))
	}
}
