package server_test

import (
	"bufio"
	"fmt"
	"net"
	"strings"

	pop3server "github.com/rest-mail/go-pop3/pkg/pop3/server"

	pop3 "github.com/rest-mail/go-pop3/pkg/pop3"
)

// memBackend is a trivial in-memory [pop3.Backend]: it accepts one hard-coded
// user and hands out a maildrop holding a single message. A real backend would
// look credentials up in a store and scope the returned [pop3.Mailbox] to that
// user.
type memBackend struct{}

func (memBackend) Authenticate(user, pass string) (pop3.Mailbox, error) {
	if user != "me@example.com" || pass != "s3cret" {
		return nil, fmt.Errorf("bad credentials")
	}
	return &memMailbox{}, nil
}

// memMailbox serves one fixed message from memory.
type memMailbox struct{}

var exampleRaw = []byte("From: me@example.com\r\n" +
	"Subject: hello\r\n" +
	"\r\n" +
	"Hi there!\r\n")

func (m *memMailbox) Messages() ([]pop3.Message, error) {
	return []pop3.Message{{UID: "1001", Size: len(exampleRaw)}}, nil
}
func (m *memMailbox) Retrieve(uid string) ([]byte, error) { return exampleRaw, nil }
func (m *memMailbox) MarkSeen(uid string) error           { return nil }
func (m *memMailbox) Delete(uid string) error             { return nil }

// Example drives a full POP3 conversation against an in-memory backend. It runs
// a [pop3server.Session] over an in-process net.Pipe instead of a TCP listener, so the
// round trip is self-contained; in production you would call [pop3server.New]
// and [pop3server.Server.ListenAndServe] with real ports (see the package README).
func Example() {
	client, server := net.Pipe()

	// Serve the connection in the background. A nil *tls.Config disables STLS,
	// and a nil Limiter defaults to pop3.NopLimiter (no per-IP limits).
	go pop3server.NewSession(server, memBackend{}, nil, nil).Handle()

	r := bufio.NewReader(client)
	w := bufio.NewWriter(client)

	// send writes one command line and prints the server's status reply.
	send := func(cmd string) {
		if cmd != "" {
			_, _ = fmt.Fprintf(w, "%s\r\n", cmd)
			_ = w.Flush()
		}
		line, _ := r.ReadString('\n')
		fmt.Println(strings.TrimRight(line, "\r\n"))
	}

	send("")                    // read the greeting
	send("USER me@example.com") // identify the user
	send("PASS s3cret")         // authenticate
	send("STAT")                // count and total size
	send("QUIT")                // commit deletes and disconnect

	// Output:
	// +OK POP3 server ready
	// +OK
	// +OK Authentication successful
	// +OK 1 51
	// +OK POP3 server signing off
}
