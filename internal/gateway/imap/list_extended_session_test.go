package imap

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

// dialListExtended logs in to a session over an API whose account holds RFC
// 5258 §5's hierarchy, with Sent and, as in example 11, "music/rock" under a
// "music" that is not a folder.
func dialListExtended(t *testing.T) *imapClient {
	t.Helper()
	c, _ := dialListExtendedWith(t, []string{"INBOX", "Fruit", "Fruit/Apple", "Fruit/Banana", "Tofu", "Vegetable",
		"Vegetable/Broccoli", "Vegetable/Corn", "Sent", "music/rock"})
	return c
}

// fakeSubscriptions is the API's subscription list for one account, as the
// session test's fake API keeps it.
type fakeSubscriptions struct {
	mu    sync.Mutex
	names map[string]bool
}

func (f *fakeSubscriptions) list() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.names))
	for name := range f.names {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// dialListExtendedWith is dialListExtended with the account subscribed to
// subscribed, on a fake API that keeps the list as the real one does: PUT and
// DELETE on /subscriptions/{folder}, the folder path-escaped.
func dialListExtendedWith(t *testing.T, subscribed []string) (*imapClient, *fakeSubscriptions) {
	t.Helper()
	subs := &fakeSubscriptions{names: map[string]bool{}}
	for _, name := range subscribed {
		subs.names[name] = true
	}
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
	mux.HandleFunc("/api/v1/gateway-sessions", func(w http.ResponseWriter, r *http.Request) {
		reply(w, `{"data":{"token":"rmgs_tok","user":{"id":1,"email":"dana@example.test"}}}`)
	})
	mux.HandleFunc("/api/v1/accounts/1/folders", func(w http.ResponseWriter, r *http.Request) {
		reply(w, `{"data":[{"name":"INBOX"},{"name":"Fruit"},{"name":"Fruit/Apple"},{"name":"Fruit/Banana"},`+
			`{"name":"Tofu"},{"name":"Vegetable"},{"name":"Vegetable/Broccoli"},{"name":"Vegetable/Corn"},`+
			`{"name":"Sent"},{"name":"music/rock"}]}`)
	})
	mux.HandleFunc("GET /api/v1/accounts/1/subscriptions", func(w http.ResponseWriter, r *http.Request) {
		body := `{"data":[`
		for i, name := range subs.list() {
			if i > 0 {
				body += ","
			}
			body += fmt.Sprintf(`{"name":%q}`, name)
		}
		reply(w, body+`]}`)
	})
	mux.HandleFunc("/api/v1/accounts/1/subscriptions/{folder}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		name := r.PathValue("folder")
		subs.mu.Lock()
		defer subs.mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			subs.names[name] = true
			reply(w, fmt.Sprintf(`{"data":{"name":%q}}`, name))
		case http.MethodDelete:
			delete(subs.names, name)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, server := net.Pipe()
	sess := imapserver.NewSession(server, NewBackend(apiclient.New(srv.URL), nil), "imap.test", nil, goimap.NopLimiter{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		sess.Handle()
	}()
	t.Cleanup(func() {
		_ = client.Close()
		<-done
	})
	if err := client.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	c := &imapClient{t: t, conn: client, r: bufio.NewReader(client)}
	c.read() // greeting
	c.do("a1", "LOGIN dana@example.test pw")
	return c, subs
}

// listExactly sends command and requires exactly want as its untagged replies.
func (c *imapClient) listExactly(tag, command string, want ...string) {
	c.t.Helper()
	out := strings.Split(strings.TrimSuffix(c.do(tag, command), "\r\n"), "\r\n")
	if got := out[:len(out)-1]; !slices.Equal(got, want) {
		c.t.Errorf("%s:\n got %q\nwant %q", command, got, want)
	}
}

// A mail app gets LIST-EXTENDED (RFC 5258, part of IMAP4rev2 as RFC 9051
// §6.3.9) from the gateway: the CHILDREN return option (example 3), several
// patterns (examples 7 and 11), "music" that is not a folder answered
// \NonExistent \HasChildren (example 11), the SUBSCRIBED options and LSUB, and
// CHILDINFO under RECURSIVEMATCH (example 8). The account here is subscribed to
// every folder it has.
func TestListExtendedThroughTheSession(t *testing.T) {
	c := dialListExtended(t)
	if caps := c.do("a2", "CAPABILITY"); !strings.Contains(caps, " LIST-EXTENDED") {
		t.Fatalf("CAPABILITY lacks LIST-EXTENDED: %q", caps)
	}
	c.listExactly("a3", `LIST () "" "%" RETURN (CHILDREN)`,
		`* LIST (\HasNoChildren) "/" "INBOX"`, `* LIST (\HasChildren) "/" "Fruit"`, `* LIST (\HasNoChildren) "/" "Tofu"`,
		`* LIST (\HasChildren) "/" "Vegetable"`, `* LIST (\Sent \HasNoChildren) "/" "Sent"`,
		`* LIST (\NonExistent \HasChildren) "/" "music"`)
	c.listExactly("a4", `LIST "" ("INBOX" "Fruit/%" "Fruit/Apple")`,
		`* LIST () "/" "INBOX"`, `* LIST () "/" "Fruit/Apple"`, `* LIST () "/" "Fruit/Banana"`)
	c.listExactly("a5", `LIST "" (% music/rock)`,
		`* LIST () "/" "INBOX"`, `* LIST () "/" "Fruit"`, `* LIST () "/" "Tofu"`, `* LIST () "/" "Vegetable"`,
		`* LIST (\Sent) "/" "Sent"`, `* LIST () "/" "music/rock"`)
	c.listExactly("a6", `LIST (SUBSCRIBED) "" "Vegetable*" RETURN (CHILDREN)`,
		`* LIST (\Subscribed \HasChildren) "/" "Vegetable"`, `* LIST (\Subscribed \HasNoChildren) "/" "Vegetable/Broccoli"`,
		`* LIST (\Subscribed \HasNoChildren) "/" "Vegetable/Corn"`)
	c.listExactly("a7", `LSUB "" "%"`,
		`* LSUB () "/" "INBOX"`, `* LSUB () "/" "Fruit"`, `* LSUB () "/" "Tofu"`, `* LSUB () "/" "Vegetable"`,
		`* LSUB (\Sent) "/" "Sent"`, `* LSUB (\Noselect) "/" "music"`)
	c.listExactly("a8", `LIST (SUBSCRIBED RECURSIVEMATCH) "" "%"`,
		`* LIST (\Subscribed) "/" "INBOX"`, `* LIST (\Subscribed) "/" "Fruit" ("CHILDINFO" ("SUBSCRIBED"))`,
		`* LIST (\Subscribed) "/" "Tofu"`, `* LIST (\Subscribed) "/" "Vegetable" ("CHILDINFO" ("SUBSCRIBED"))`,
		`* LIST (\Sent \Subscribed) "/" "Sent"`, `* LIST (\NonExistent) "/" "music" ("CHILDINFO" ("SUBSCRIBED"))`)
	c.listExactly("a9", `LIST (SPECIAL-USE) "" "*" RETURN (SUBSCRIBED)`, `* LIST (\Sent \Subscribed) "/" "Sent"`)
}

// RFC 5258 §3: "A server MUST respond to options it does not recognize with a
// BAD response", and RECURSIVEMATCH alone, or with only REMOTE, is BAD (§3.1).
func TestListExtendedRefusalsThroughTheSession(t *testing.T) {
	c := dialListExtended(t)
	for i, command := range []string{
		`LIST (RECURSIVEMATCH) "" "%"`, `LIST (REMOTE RECURSIVEMATCH) "" "%"`, `LIST (VIRTUAL) "" "*"`,
		`LIST "" "*" RETURN (MYRIGHTS)`, `LIST "" ("INBOX" "Tofu"`,
	} {
		tag := fmt.Sprintf("b%d", i)
		if _, err := fmt.Fprintf(c.conn, "%s %s\r\n", tag, command); err != nil {
			t.Fatal(err)
		}
		if line := c.read(); !strings.HasPrefix(line, tag+" BAD ") {
			t.Errorf("%s: %q, want a tagged BAD and nothing before it", command, line)
		}
	}
}

// SUBSCRIBE and UNSUBSCRIBE change the account's list in the API (#1675), and
// LSUB and LIST (SUBSCRIBED) read it back: RFC 3501 §6.3.6 has SUBSCRIBE add
// "the specified mailbox name to the server's set of "active" or "subscribed"
// mailboxes as returned by the LSUB command", §6.3.7 has UNSUBSCRIBE remove it.
func TestSubscribePersistsThroughTheSession(t *testing.T) {
	c, subs := dialListExtendedWith(t, []string{"INBOX"})
	c.listExactly("a2", `LSUB "" "*"`, `* LSUB () "/" "INBOX"`)
	c.do("a3", `SUBSCRIBE "Fruit/Apple"`)
	c.do("a4", `SUBSCRIBE Tofu`)
	c.do("a5", `SUBSCRIBE Tofu`) // already subscribed: still OK, still one name
	if got := subs.list(); !slices.Equal(got, []string{"Fruit/Apple", "INBOX", "Tofu"}) {
		t.Fatalf("API list after SUBSCRIBE: %q", got)
	}
	c.listExactly("a6", `LSUB "" "*"`,
		`* LSUB () "/" "INBOX"`, `* LSUB () "/" "Fruit/Apple"`, `* LSUB () "/" "Tofu"`)
	c.listExactly("a7", `LIST (SUBSCRIBED) "" "*"`,
		`* LIST (\Subscribed) "/" "INBOX"`, `* LIST (\Subscribed) "/" "Fruit/Apple"`, `* LIST (\Subscribed) "/" "Tofu"`)
	c.do("a8", `UNSUBSCRIBE Tofu`)
	c.do("a9", `UNSUBSCRIBE Tofu`) // not subscribed: still OK
	if got := subs.list(); !slices.Equal(got, []string{"Fruit/Apple", "INBOX"}) {
		t.Fatalf("API list after UNSUBSCRIBE: %q", got)
	}
	c.listExactly("a10", `LSUB "" "%"`, `* LSUB () "/" "INBOX"`, `* LSUB (\Noselect) "/" "Fruit"`)
	c.listExactly("a11", `LIST "" "Tofu" RETURN (SUBSCRIBED)`, `* LIST () "/" "Tofu"`)
}

// A subscribed name with no folder behind it stays on the list (RFC 9051
// §6.3.7: the server "SHOULD NOT unilaterally remove an existing mailbox name
// from the subscription list even if a mailbox by that name no longer
// exists"), and LIST (SUBSCRIBED) answers it \NonExistent (RFC 5258 §3.1,
// example 2's Fruit/Peach).
func TestDeletedFolderStaysSubscribedThroughTheSession(t *testing.T) {
	c, _ := dialListExtendedWith(t, []string{"INBOX", "Fruit/Peach"})
	c.listExactly("a2", `LIST (SUBSCRIBED) "" "*"`,
		`* LIST (\Subscribed) "/" "INBOX"`, `* LIST (\Subscribed \NonExistent) "/" "Fruit/Peach"`)
	c.listExactly("a3", `LSUB "" "Fruit/*"`, `* LSUB () "/" "Fruit/Peach"`)
}
