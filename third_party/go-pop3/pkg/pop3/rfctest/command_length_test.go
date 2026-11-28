package rfctest

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	pop3 "github.com/rest-mail/go-pop3/pkg/pop3"
)

// Capture the actual arguments; accepting only the full expected bytes makes
// success sensitive to truncation, including the distinct final byte.
type lengthBackend struct {
	want  [2]string
	calls chan [2]string
}

func (b *lengthBackend) Authenticate(user, pass string) (pop3.Mailbox, error) {
	b.calls <- [2]string{user, pass}
	if [2]string{user, pass} != b.want {
		return nil, errors.New("credential bytes changed")
	}
	return mailbox{}, nil
}

func lengthLogin(t *testing.T, user, pass string, ending string, fragmented bool) {
	t.Helper()
	b := &lengthBackend{want: [2]string{user, pass}, calls: make(chan [2]string, 2)}
	w := connect(t, b)
	for i, command := range []string{"USER " + user + ending, "PASS " + pass + ending} {
		parts := []string{command}
		if fragmented {
			// Split the keyword, argument and CRLF across separate writes.
			parts = []string{command[:2], command[2:17], command[17 : len(command)-2], "\r", "\n"}
		}
		for _, part := range parts {
			_ = w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if n, err := io.WriteString(w.conn, part); err != nil || n != len(part) {
				t.Fatalf("command %d: wrote %d/%d bytes: %v", i, n, len(part), err)
			}
		}
		want := "+OK"
		if i == 1 {
			want = "+OK Authentication successful"
		}
		if got := w.line(); got != want {
			t.Fatalf("command %d response = %q, want %q", i, got, want)
		}
	}
	select {
	case got := <-b.calls:
		if got != b.want {
			t.Fatal("Authenticate received different credential bytes")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Authenticate was not called")
	}
	if got := w.cmd("STAT"); got != "+OK 1 42" {
		t.Fatalf("authenticated framing probe = %q", got)
	}
	if got := w.cmd("QUIT"); got != "+OK POP3 server signing off" {
		t.Fatalf("QUIT = %q", got)
	}
	lengthClosed(t, w)
	select {
	case <-b.calls:
		t.Fatal("Authenticate called more than once")
	default:
	}
}

func lengthClosed(t *testing.T, w *wire) {
	t.Helper()
	_ = w.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if tail, err := w.reader.ReadString('\n'); tail != "" || !errors.Is(err, io.EOF) {
		t.Fatalf("after final response: %q, %v; want empty EOF", tail, err)
	}
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
		t.Fatal("Session.Handle did not terminate")
	}
}

// RFC 2449 §4 increases RFC 1939's argument limits. Both complete commands
// are exactly 255 octets: four keyword bytes, SP, 248 argument bytes, CRLF.
// checklist: POP3-LENGTH-01 POP3-LENGTH-03
func TestCommandLength255Credentials(t *testing.T) {
	user := strings.Repeat("u", 247) + "U"
	pass := strings.Repeat("p", 247) + "P"
	if len("USER "+user+"\r\n") != 255 || len("PASS "+pass+"\r\n") != 255 {
		t.Fatal("credential command fixture is not 255 octets including CRLF")
	}
	for _, fragmented := range []bool{false, true} {
		t.Run(map[bool]string{false: "whole", true: "fragmented-crlf"}[fragmented], func(t *testing.T) {
			lengthLogin(t, user, pass, "\r\n", fragmented)
		})
	}
}

// Beyond 255 octets, UTF-8 and bare LF are implementation-policy controls,
// not claims of RFC-conforming client syntax. The cap counts bytes through LF.
// checklist: POP3-LENGTH-POLICY-01
func TestCommandLengthPolicy1024Accepted(t *testing.T) {
	for _, tc := range []struct{ name, arg, ending string }{
		{"ascii-crlf", strings.Repeat("a", 1016) + "Z", "\r\n"},
		{"utf8-crlf", strings.Repeat("\u00e9", 508) + "Z", "\r\n"},
		{"ascii-lf", strings.Repeat("a", 1017) + "Z", "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len("USER "+tc.arg+tc.ending) != 1024 || len("PASS "+tc.arg+tc.ending) != 1024 {
				t.Fatal("credential command fixture is not 1024 bytes including LF")
			}
			lengthLogin(t, tc.arg, tc.arg, tc.ending, false)
		})
	}
}

// A full 1024-byte buffer without LF is refused immediately. Complete
// 1025-byte lines cannot dispatch, and a queued QUIT cannot resynchronize.
// checklist: POP3-LENGTH-POLICY-02
func TestCommandLengthPolicyOverflowCloses(t *testing.T) {
	for _, tc := range []struct{ name, line string }{
		{"next-byte-crlf", "PASS " + strings.Repeat("p", 1018) + "\r\n"},
		{"next-byte-lf", "PASS " + strings.Repeat("p", 1019) + "\n"},
		{"utf8-next-byte", "PASS " + strings.Repeat("\u00e9", 509) + "\r\n"},
		{"unterminated", "PASS " + strings.Repeat("p", 1019)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			complete := strings.HasSuffix(tc.line, "\n")
			wantLen := 1024
			if complete {
				wantLen++
			}
			if len(tc.line) != wantLen {
				t.Fatalf("overflow fixture = %d bytes, want %d", len(tc.line), wantLen)
			}
			b := &lengthBackend{calls: make(chan [2]string, 2)}
			w := connect(t, b)
			if got := w.cmd("USER pending"); got != "+OK" {
				t.Fatalf("USER = %q", got)
			}
			input := tc.line
			if complete {
				input += "QUIT\r\n"
			}
			type result struct {
				n   int
				err error
			}
			written := make(chan result, 1)
			writerDone := make(chan struct{})
			t.Cleanup(func() {
				_ = w.conn.Close()
				select {
				case <-writerDone:
				case <-time.After(5 * time.Second):
					t.Error("overflow writer did not stop during cleanup")
				}
			})
			_ = w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			go func() {
				defer close(writerDone)
				n, err := io.WriteString(w.conn, input)
				written <- result{n, err}
			}()
			// Read concurrently with the bounded write: net.Pipe is synchronous,
			// and the server deliberately stops reading before the input ends.
			if got := w.line(); got != "-ERR Command line too long" {
				t.Fatalf("overflow response = %q", got)
			}
			lengthClosed(t, w)
			select {
			case got := <-written:
				if got.n != 1024 || (complete && !errors.Is(got.err, io.ErrClosedPipe)) || (!complete && got.err != nil) {
					t.Fatalf("overflow write = %d bytes, %v", got.n, got.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("overflow writer did not terminate")
			}
			select {
			case <-b.calls:
				t.Fatal("overflow dispatched to Authenticate")
			default:
			}
		})
	}
}

// fill pads a command to size octets including CRLF with a repeated digit,
// so numeric arguments stay syntactically numeric but out of range.
func fill(t *testing.T, prefix string, size int) string {
	t.Helper()
	line := prefix + strings.Repeat("9", size-2-len(prefix))
	if len(line+"\r\n") != size {
		t.Fatalf("fixture %q... is %d octets, want %d", prefix, len(line+"\r\n"), size)
	}
	return line
}

// RFC 2449 §4 raises the command limit to 255 octets but keeps the first line
// of every response, including the greeting, at 512 octets including CRLF.
// Each command here is a maximum-length line in its state; the 1023-octet
// unknown command is the bounded reader's policy limit. wire.line rejects any
// line longer than 512 octets or lacking CRLF.
// checklist: POP3-LENGTH-02
func TestResponseFirstLineLimit(t *testing.T) {
	w := connect(t, backend{password: "secret"})
	authorization := []struct{ command, status string }{
		{fill(t, "XYZW ", 255), "-ERR"},
		{fill(t, "CAPA ", 255), "-ERR"},
		{fill(t, "STAT ", 255), "-ERR"},
		{fill(t, "LIST ", 255), "-ERR"},
		{fill(t, "RETR ", 255), "-ERR"},
		{fill(t, "TOP ", 255), "-ERR"},
		{fill(t, "UIDL ", 255), "-ERR"},
		{fill(t, "DELE ", 255), "-ERR"},
		{fill(t, "STLS ", 255), "-ERR"},
		{fill(t, "APOP alice ", 255), "-ERR"},
		{fill(t, "PASS ", 255), "-ERR"},
		{fill(t, "USER ", 255), "+OK"},
		{fill(t, "PASS ", 255), "-ERR"},
		{fill(t, "XYZW ", 1023), "-ERR"},
	}
	for _, tc := range authorization {
		w.status(w.cmd(tc.command), tc.status)
	}
	w.login("secret")
	transaction := []struct{ command, status string }{
		{fill(t, "XYZW ", 255), "-ERR"},
		{fill(t, "STAT ", 255), "-ERR"},
		{fill(t, "LIST ", 255), "-ERR"},
		{fill(t, "RETR ", 255), "-ERR"},
		{fill(t, "TOP ", 255), "-ERR"},
		{fill(t, "TOP 1 ", 255), "-ERR"},
		{fill(t, "UIDL ", 255), "-ERR"},
		{fill(t, "DELE ", 255), "-ERR"},
		{fill(t, "NOOP ", 255), "-ERR"},
		{fill(t, "RSET ", 255), "-ERR"},
		{fill(t, "CAPA ", 255), "-ERR"},
		{fill(t, "STLS ", 255), "-ERR"},
		{fill(t, "USER ", 255), "-ERR"},
		{fill(t, "PASS ", 255), "-ERR"},
		{fill(t, "XYZW ", 1023), "-ERR"},
	}
	for _, tc := range transaction {
		w.status(w.cmd(tc.command), tc.status)
	}
	if got := w.cmd("STAT"); got != "+OK 1 42" {
		t.Fatalf("framing probe after long commands = %q", got)
	}
	if got := w.cmd("QUIT"); got != "+OK POP3 server signing off" {
		t.Fatalf("QUIT = %q", got)
	}
	lengthClosed(t, w)
}
