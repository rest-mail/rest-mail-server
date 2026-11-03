package server_test

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/go-imap/pkg/imap/server"
)

// demoBackend is a minimal in-memory Backend: it authenticates one user and
// returns a Mailbox holding a single INBOX message. A real backend would verify
// credentials against a store and return a per-user view.
type demoBackend struct{}

func (demoBackend) Authenticate(user, pass string) (imap.Mailbox, error) {
	if user != "alice@example.com" || pass != "s3cret" {
		return nil, fmt.Errorf("invalid credentials")
	}
	return demoMailbox{}, nil
}

// demoMailbox is the smallest useful Mailbox — one folder, one message. It
// implements only the base interface, so the server advertises the baseline
// capabilities and not UIDPLUS.
type demoMailbox struct{}

func (demoMailbox) Folders() ([]imap.Folder, error) {
	return []imap.Folder{{Name: "INBOX"}}, nil
}

func (demoMailbox) Messages(folder string) ([]imap.Message, error) {
	if folder != "INBOX" {
		return nil, nil
	}
	return []imap.Message{{
		UID:     101,
		Size:    23,
		Subject: "hello",
		From:    imap.Address{Name: "Bob", Email: "bob@example.net"},
	}}, nil
}

func (demoMailbox) Fetch(uid uint32) ([]byte, error) {
	return []byte("Subject: hello\r\n\r\nhi\r\n"), nil
}

func (demoMailbox) Store(uint32, imap.FlagUpdate) error          { return nil }
func (demoMailbox) Move(uint32, string) error                    { return nil }
func (demoMailbox) Delete(uint32) error                          { return nil }
func (demoMailbox) Copy(uint32, string) error                    { return nil }
func (demoMailbox) Append(string, imap.FlagUpdate, []byte) error { return nil }
func (demoMailbox) Quota() (used, limit int64, err error)        { return 0, 0, nil }

// Example wires a Backend into a Session and drives one IMAP conversation over an
// in-memory pipe, so the whole round trip is self-contained and needs no network
// socket. In production you would instead pass the Backend to server.New and
// call ListenAndServe; a Session is the single-connection primitive that a Server
// spawns per client.
func Example() {
	// net.Pipe gives us both ends of a connection in-process. The server end is
	// handed to a Session; the client end is what we type IMAP commands into.
	clientConn, serverConn := net.Pipe()
	sess := server.NewSession(serverConn, demoBackend{}, "imap.example.com", nil, nil)
	go sess.Handle()
	defer func() { _ = clientConn.Close() }()

	_ = clientConn.SetDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(clientConn)

	// Read and discard the server greeting.
	if _, err := r.ReadString('\n'); err != nil {
		panic(err)
	}

	// LOGIN. The tagged OK carries a [CAPABILITY ...] response code listing what
	// this backend supports post-authentication.
	_, _ = fmt.Fprint(clientConn, "a1 LOGIN alice@example.com s3cret\r\n")
	_, login := readReply(r, "a1")
	fmt.Println("capabilities:", capsFrom(login))

	// SELECT INBOX. The untagged "* N EXISTS" line reports the message count the
	// Mailbox returned for that folder.
	_, _ = fmt.Fprint(clientConn, "a2 SELECT INBOX\r\n")
	untagged, _ := readReply(r, "a2")
	fmt.Println("INBOX EXISTS:", existsFrom(untagged))

	_, _ = fmt.Fprint(clientConn, "a3 LOGOUT\r\n")
	_, _ = readReply(r, "a3")

	// Output:
	// capabilities: IMAP4rev1 LITERAL- IDLE MOVE QUOTA UNSELECT ENABLE SPECIAL-USE NAMESPACE ESEARCH LIST-STATUS STATUS=SIZE BINARY SEARCHRES LIST-EXTENDED SASL-IR AUTH=PLAIN
	// INBOX EXISTS: 1
}

// readReply reads response lines until the one tagged with tag, returning the
// untagged lines and that final tagged line. The demo commands carry no IMAP
// literals, so a plain line reader suffices.
func readReply(r *bufio.Reader, tag string) (untagged []string, tagged string) {
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			panic(err)
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, tag+" ") {
			return untagged, line
		}
		untagged = append(untagged, line)
	}
}

// capsFrom extracts the space-separated tokens of a "[CAPABILITY ...]" response
// code from a tagged status line.
func capsFrom(line string) string {
	const open = "[CAPABILITY "
	i := strings.Index(line, open)
	if i < 0 {
		return ""
	}
	rest := line[i+len(open):]
	if j := strings.IndexByte(rest, ']'); j >= 0 {
		return rest[:j]
	}
	return ""
}

// existsFrom returns the count from an untagged "* N EXISTS" line, or "?".
func existsFrom(lines []string) string {
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) == 3 && f[0] == "*" && f[2] == "EXISTS" {
			return f[1]
		}
	}
	return "?"
}
