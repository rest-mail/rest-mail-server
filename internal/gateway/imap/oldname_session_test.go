package imap

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

// The same name in both forms, in modified UTF-7 as a session writes it before UTF-8 is enabled.
const (
	cafeNFC     = "Caf\u00e9"
	cafeNFCWire = "Caf&AOk-"
	cafeNFDWire = "Cafe&AwE-"
)

// TestOldNameThroughTheGateway: the engine and the gateway agree on NFC. A client's decomposed
// spelling reaches the folder, the API only ever sees the NFC name, each command that converts a
// name says so with OLDNAME, and a name that matches an existing folder once converted is refused
// (RFC 9051 §§5.1, 6.3.9.7, #1669).
func TestOldNameThroughTheGateway(t *testing.T) {
	api := &folderAPI{folders: map[string][]uint{"INBOX": {1}, "Trash": {}, "Plain": {}}, nextID: 10}
	srv := api.serve(t)
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
	if err := client.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	c := &imapClient{t: t, conn: client, r: bufio.NewReader(client)}
	c.read() // greeting
	c.do("a1", "LOGIN dana@example.test pw")

	oldName := fmt.Sprintf(`* LIST () "/" %q ("OLDNAME" (%q))`, cafeNFCWire, cafeNFDWire)
	wantOldName := func(tag, command, want string) {
		t.Helper()
		out := c.do(tag, command)
		if !strings.Contains(out, want) || !strings.Contains(out, tag+" OK") {
			t.Fatalf("%s:\n%s\nwant %s", command, out, want)
		}
	}

	wantOldName("b1", fmt.Sprintf("CREATE %q", cafeNFDWire), oldName)
	if got := api.names(); got != cafeNFC+" INBOX Plain Trash" {
		t.Fatalf("folders after CREATE: %q", got)
	}
	for _, spelling := range []string{cafeNFDWire, cafeNFCWire} {
		if got := c.tagged("b2", fmt.Sprintf("b2 CREATE %q", spelling)); got != "b2 NO [ALREADYEXISTS] Mailbox already exists" {
			t.Fatalf("CREATE %s of an existing folder: %q", spelling, got)
		}
	}

	wantOldName("c1", fmt.Sprintf("SELECT %q", cafeNFDWire), oldName)
	wantOldName("c2", fmt.Sprintf("EXAMINE %q", cafeNFDWire), oldName)
	if out := c.do("c3", fmt.Sprintf("SELECT %q", cafeNFCWire)); strings.Contains(out, "OLDNAME") {
		t.Fatalf("SELECT of the name in NFC reported OLDNAME:\n%s", out)
	}
	c.do("c4", "UNSELECT")

	if out := c.do("d1", fmt.Sprintf(`LIST "" %q`, cafeNFDWire)); !strings.Contains(out, fmt.Sprintf(`"/" %q`, cafeNFCWire)) {
		t.Fatalf("LIST of the other spelling:\n%s", out)
	}
	if out := c.do("d2", fmt.Sprintf("STATUS %q (MESSAGES)", cafeNFDWire)); !strings.Contains(out, fmt.Sprintf("* STATUS %q (MESSAGES 0)", cafeNFCWire)) {
		t.Fatalf("STATUS of the other spelling:\n%s", out)
	}

	wantOldName("e1", fmt.Sprintf(`RENAME %q "Tea"`, cafeNFDWire), fmt.Sprintf(`* LIST () "/" "Tea" ("OLDNAME" (%q))`, cafeNFDWire))
	wantOldName("e2", fmt.Sprintf(`RENAME "Tea" %q`, cafeNFDWire), fmt.Sprintf(`* LIST () "/" %q ("OLDNAME" ("Tea"))`, cafeNFCWire))
	if got := c.tagged("e3", fmt.Sprintf(`e3 RENAME "Plain" %q`, cafeNFDWire)); got != "e3 NO [ALREADYEXISTS] Mailbox already exists" {
		t.Fatalf("RENAME onto an existing folder: %q", got)
	}

	wantOldName("f1", fmt.Sprintf("DELETE %q", cafeNFDWire), fmt.Sprintf(`* LIST (\NonExistent) "/" %q ("OLDNAME" (%q))`, cafeNFCWire, cafeNFDWire))
	if got := api.names(); got != "INBOX Plain Trash" {
		t.Fatalf("folders after DELETE: %q", got)
	}
}
