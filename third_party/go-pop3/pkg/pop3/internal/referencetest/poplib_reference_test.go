// Package referencetest compares the public server with an independent client.
// It is separate from the checked wire proofs, which run without Python.
package referencetest

import (
	"bytes"
	"context"

	pop3server "github.com/rest-mail/go-pop3/pkg/pop3/server"

	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"

	pop3 "github.com/rest-mail/go-pop3/pkg/pop3"
)

//go:embed testdata/poplib_reference.py
var poplibReference string

type referenceMessage struct {
	Raw  string `json:"-"`
	Want string `json:"want"`
	UID  string `json:"uid"`
}

type referenceMailbox struct {
	messages []referenceMessage
	deleted  []string
}

func (m *referenceMailbox) Authenticate(user, password string) (pop3.Mailbox, error) {
	if user != "alice" || password != "secret" {
		return nil, fmt.Errorf("invalid fixture credentials")
	}
	return m, nil
}

func (m *referenceMailbox) Messages() ([]pop3.Message, error) {
	messages := make([]pop3.Message, len(m.messages))
	for i, message := range m.messages {
		// Expected canonical bytes are fixture data, independent of OctetCount.
		messages[i] = pop3.Message{UID: message.UID, Size: len(message.Want)}
	}
	return messages, nil
}

func (m *referenceMailbox) Retrieve(uid string) ([]byte, error) {
	for _, message := range m.messages {
		if message.UID == uid {
			return []byte(message.Raw), nil
		}
	}
	return nil, fmt.Errorf("unknown fixture UID %q", uid)
}

func (*referenceMailbox) MarkSeen(string) error { return nil }
func (m *referenceMailbox) Delete(uid string) error {
	m.deleted = append(m.deleted, uid)
	return nil
}

// TestPythonPoplibReference compares the public server's replies with CPython's
// independent POP3 response parser and fixed expected message bytes. This is a
// client interoperability/differential decoding proof, not a second server or
// a complete RFC oracle. Existing wire proofs check stricter CRLF/status syntax.
// The reference is optional for Go-only users but explicitly required in CI.
func TestPythonPoplibReference(t *testing.T) {
	python := os.Getenv("POP3_REFERENCE_PYTHON")
	if python == "" {
		t.Skip("set POP3_REFERENCE_PYTHON=python3 to run the independent client (required in CI)")
	}
	for _, mode := range []string{"quit", "disconnect", "empty"} {
		t.Run(mode, func(t *testing.T) {
			m := &referenceMailbox{messages: []referenceMessage{
				{Raw: "Subject: dots\r\n\r\n.\r\n..\r\n.x\r\nlast\r\n", Want: "Subject: dots\r\n\r\n.\r\n..\r\n.x\r\nlast\r\n", UID: "opaque-alpha"},
				{Raw: "Subject: lf\n\n.\n..\nlast", Want: "Subject: lf\r\n\r\n.\r\n..\r\nlast\r\n", UID: "opaque-bravo"},
				{Raw: "", Want: "", UID: "opaque-empty"},
				{Raw: "Subject: bytes\r\n\r\n\x00\x80\xff\r\n\r\n", Want: "Subject: bytes\r\n\r\n\x00\x80\xff\r\n\r\n", UID: "opaque-bytes"},
			}}
			if mode == "empty" {
				m.messages = []referenceMessage{}
			}
			for _, message := range m.messages {
				if got := pop3.OctetCount([]byte(message.Raw)); got != len(message.Want) {
					t.Fatalf("OctetCount(%q) = %d, want %d", message.UID, got, len(message.Want))
				}
			}
			// JSON strings cannot preserve invalid UTF-8. Keep the byte fixture
			// representable via Latin-1 code points for the Python reference.
			input := struct {
				Messages []referenceMessage `json:"messages"`
				Mode     string             `json:"mode"`
			}{Messages: append([]referenceMessage{}, m.messages...), Mode: mode}
			for i := range input.Messages {
				var runes []rune
				for _, b := range []byte(input.Messages[i].Want) {
					runes = append(runes, rune(b))
				}
				input.Messages[i].Want = string(runes)
			}
			data, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			stopListener := context.AfterFunc(ctx, func() { _ = listener.Close() })
			done := make(chan struct{})
			var serveErr error
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					serveErr = err
					return
				}
				stopConn := context.AfterFunc(ctx, func() { _ = conn.Close() })
				defer stopConn()
				pop3server.NewSession(conn, m, nil, nil).Handle()
			}()
			t.Cleanup(func() {
				cancel()
				stopListener()
				_ = listener.Close()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("reference session did not stop")
				}
			})
			command := exec.CommandContext(ctx, python, "-I", "-c", poplibReference, listener.Addr().String())
			command.Stdin = bytes.NewReader(data)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("Python poplib reference: %v\n%s", err, output)
			}
			select {
			case <-done:
				if serveErr != nil {
					t.Fatal(serveErr)
				}
			case <-ctx.Done():
				t.Fatal("reference client exited without stopping the session")
			}
			var wantDeleted []string
			if mode == "quit" {
				wantDeleted = []string{"opaque-bravo"}
			}
			if !reflect.DeepEqual(m.deleted, wantDeleted) {
				t.Fatalf("backend deletes = %v, want %v", m.deleted, wantDeleted)
			}
		})
	}
}
