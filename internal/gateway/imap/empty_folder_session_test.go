package imap

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

// dialFolders opens a signed-in IMAP session against the gateway backend,
// talking to the fake folder API at url.
func dialFolders(t *testing.T, url string) *imapClient {
	t.Helper()
	client, server := net.Pipe()
	sess := imapserver.NewSession(server, NewBackend(apiclient.New(url), nil), "imap.test", nil, goimap.NopLimiter{})
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
	return c
}

// listed reports whether a LIST response holds an entry for name.
func listed(out, name string) bool {
	return strings.Contains(out, `"/" "`+name+`"`) || strings.Contains(out, `"/" `+name+"\r\n")
}

// #1834: CREATE of an empty mailbox answered OK but stored nothing, so LIST
// did not show it, in that session or after a reconnect, and imapsync lost
// every empty folder while reporting success. CREATE now makes the folder in
// the store: it is listed in the same session and in a new one, it can be
// selected while it holds nothing, and DELETE removes it from both.
func TestCreateEmptyMailboxPersists(t *testing.T) {
	api := &folderAPI{folders: map[string][]uint{"INBOX": {1}, "Trash": {}}, nextID: 10}
	srv := api.serve(t)

	first := dialFolders(t, srv.URL)
	if got := first.tagged("b1", "b1 CREATE EmptyProbe734"); got != "b1 OK CREATE completed" {
		t.Fatalf("CREATE EmptyProbe734: %q", got)
	}
	if !strings.Contains(api.names(), "EmptyProbe734") {
		t.Fatalf("CREATE answered OK but the store has no such folder: %q", api.names())
	}
	if out := first.do("b2", `LIST "" EmptyProbe734`); !listed(out, "EmptyProbe734") {
		t.Fatalf("LIST in the same session does not show the empty folder:\n%s", out)
	}

	second := dialFolders(t, srv.URL)
	if out := second.do("c1", `LIST "" *`); !listed(out, "EmptyProbe734") {
		t.Fatalf("LIST after reconnecting does not show the empty folder:\n%s", out)
	}
	if out := second.do("c2", "SELECT EmptyProbe734"); !strings.Contains(out, "* 0 EXISTS") {
		t.Fatalf("SELECT of the empty folder:\n%s", out)
	}
	second.do("c3", "CLOSE")

	if got := first.tagged("d1", "d1 DELETE EmptyProbe734"); got != "d1 OK DELETE completed" {
		t.Fatalf("DELETE EmptyProbe734: %q", got)
	}
	if out := first.do("d2", `LIST "" *`); listed(out, "EmptyProbe734") {
		t.Fatalf("LIST in the same session still shows the deleted folder:\n%s", out)
	}
	if out := second.do("d3", `LIST "" *`); listed(out, "EmptyProbe734") {
		t.Fatalf("LIST in the other session still shows the deleted folder:\n%s", out)
	}
	if strings.Contains(api.names(), "EmptyProbe734") {
		t.Fatalf("DELETE answered OK but the store still holds the folder: %q", api.names())
	}
}
