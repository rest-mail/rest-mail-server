package mailimport

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	apitypes "github.com/restmail/restmail/internal/api/types"
)

type imapSourceFixture struct {
	raw, flags, folders string
	size                *int
	count               int
	stall, refuse       bool
	mu                  sync.Mutex
	commands            []string
}

func newIMAPFixture(t *testing.T, f *imapSourceFixture) IMAPOptions {
	t.Helper()
	certificate := httptest.NewTLSServer(http.NotFoundHandler())
	pool := x509.NewCertPool()
	pool.AddCert(certificate.Certificate())
	config := certificate.TLS.Clone()
	certificate.Close()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", config)
	if err != nil {
		t.Fatal(err)
	}
	var connections []net.Conn
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		mu.Lock()
		connections = append(connections, conn)
		mu.Unlock()
		defer conn.Close()
		_, _ = io.WriteString(conn, "* OK [CAPABILITY IMAP4rev1] source\r\n")
		r := bufio.NewReader(conn)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			parts := strings.SplitN(strings.TrimSpace(line), " ", 3)
			if len(parts) < 2 {
				return
			}
			tag, command := parts[0], strings.ToUpper(parts[1])
			f.mu.Lock()
			f.commands = append(f.commands, command)
			f.mu.Unlock()
			switch command {
			case "CAPABILITY":
				_, _ = fmt.Fprintf(conn, "* CAPABILITY IMAP4rev1\r\n%s OK capability\r\n", tag)
			case "LOGIN":
				if f.refuse {
					_, _ = fmt.Fprintf(conn, "%s NO source-password-MUST-NOT-APPEAR\r\n", tag)
				} else {
					_, _ = fmt.Fprintf(conn, "%s OK [CAPABILITY IMAP4rev1] signed in\r\n", tag)
				}
			case "LIST":
				folders := f.folders
				if folders == "" {
					folders = "* LIST () \".\" \"INBOX\"\r\n* LIST () \".\" \"Archive.2020\"\r\n* LIST (\\Noselect) \".\" \"Parent\"\r\n"
				}
				_, _ = fmt.Fprintf(conn, "%s%s OK list\r\n", folders, tag)
			case "EXAMINE":
				_, _ = fmt.Fprintf(conn, "* %d EXISTS\r\n* OK [UIDVALIDITY 9] stable\r\n* OK [UIDNEXT 11] next\r\n%s OK [READ-ONLY] examined\r\n", f.count, tag)
			case "UID":
				if f.stall {
					continue
				}
				if !strings.Contains(line, "BODY.PEEK[]<0.") || !strings.Contains(line, "UID FETCH 1:10") {
					_, _ = fmt.Fprintf(conn, "%s BAD expected bounded UID BODY.PEEK\r\n", tag)
					continue
				}
				size := len(f.raw)
				if f.size != nil {
					size = *f.size
				}
				_, _ = fmt.Fprintf(conn, "* 1 FETCH (UID 10 FLAGS (%s) INTERNALDATE \"25-Oct-2026 01:02:03 +0200\" RFC822.SIZE %d BODY[]<0> {%d}\r\n%s)\r\n%s OK fetch\r\n", f.flags, size, len(f.raw), f.raw, tag)
			case "LOGOUT":
				_, _ = fmt.Fprintf(conn, "* BYE done\r\n%s OK logout\r\n", tag)
				return
			default:
				_, _ = fmt.Fprintf(conn, "%s BAD unexpected source mutation\r\n", tag)
			}
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		mu.Lock()
		for _, conn := range connections {
			_ = conn.Close()
		}
		mu.Unlock()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("IMAP source fixture did not stop")
		}
	})
	return IMAPOptions{Address: listener.Addr().String(), Username: "source-user", Password: "source-password-MUST-NOT-APPEAR", TLSConfig: &tls.Config{RootCAs: pool}}
}

func TestIMAPPreservesReadOnlySource(t *testing.T) {
	raw := "Message-ID: <source@example.com>\r\nSubject: original\r\n\r\n\x00\xffbody\r\n"
	f := &imapSourceFixture{raw: raw, flags: `\Seen \Answered \Flagged \Draft \Recent`, count: 1}
	options := newIMAPFixture(t, f)
	var messages []apitypes.ImportMessageBody
	err := IMAP(context.Background(), options, 1024, func(source string, body apitypes.ImportMessageBody) error {
		if source != "IMAP UID 10" {
			t.Fatalf("source identifier: %q", source)
		}
		messages = append(messages, body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Folder != "Archive/2020" || messages[1].Folder != "INBOX" {
		t.Fatalf("folder mapping: %+v", messages)
	}
	for _, message := range messages {
		_, offset := message.InternalDate.Zone()
		if !bytes.Equal(message.RawMessage, []byte(raw)) || offset != 2*60*60 || message.InternalDate.Day() != 25 || !reflect.DeepEqual(message.Flags, []string{`\Seen`, `\Answered`, `\Flagged`, `\Draft`}) {
			t.Fatal("source octets, flags or internal date changed")
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, command := range f.commands {
		if command == "SELECT" || command == "STORE" || command == "CLOSE" || command == "EXPUNGE" || command == "APPEND" {
			t.Fatalf("source mutation: %s", command)
		}
	}
}

func TestIMAPRefusesLossAndBoundsReads(t *testing.T) {
	wrong := 1
	for _, tc := range []struct {
		name string
		f    *imapSourceFixture
		max  int64
	}{
		{"deleted", &imapSourceFixture{flags: `\Deleted`, count: 1}, 1024},
		{"keyword", &imapSourceFixture{flags: "$Forwarded", count: 1}, 1024},
		{"size metadata", &imapSourceFixture{flags: `\Seen`, size: &wrong, count: 1}, 1024},
		{"bounded literal", &imapSourceFixture{flags: `\Seen`, size: &wrong, count: 1}, 10},
		{"declared too large", &imapSourceFixture{flags: `\Seen`, count: 1}, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.f.raw = "Subject: private-source-content\r\n\r\nbody\r\n"
			options := newIMAPFixture(t, tc.f)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			called := false
			start := time.Now()
			err := IMAP(ctx, options, tc.max, func(string, apitypes.ImportMessageBody) error { called = true; return nil })
			if err == nil || called || time.Since(start) > time.Second {
				t.Fatalf("invalid source not refused promptly: called=%t err=%v", called, err)
			}
			if strings.Contains(err.Error(), "private-source-content") || strings.Contains(err.Error(), options.Password) {
				t.Fatal("source content or credential leaked")
			}
		})
	}
}

func TestIMAPCancellationAndVisitorFailure(t *testing.T) {
	for _, stall := range []bool{false, true} {
		t.Run(fmt.Sprint(stall), func(t *testing.T) {
			f := &imapSourceFixture{raw: "Subject: original\r\n\r\nbody", count: 1, stall: stall}
			options := newIMAPFixture(t, f)
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			failure := errors.New("destination refused")
			start := time.Now()
			err := IMAP(ctx, options, 1024, func(string, apitypes.ImportMessageBody) error { return failure })
			if err == nil || time.Since(start) > time.Second || (!stall && !errors.Is(err, failure)) {
				t.Fatalf("transfer failed to stop: %v", err)
			}
		})
	}
}

func TestIMAPTLSAndCredentialErrorsArePrivate(t *testing.T) {
	for _, mode := range []string{"untrusted", "hostname", "insecure", "sign-in"} {
		t.Run(mode, func(t *testing.T) {
			f := &imapSourceFixture{refuse: true}
			options := newIMAPFixture(t, f)
			if mode == "untrusted" {
				options.TLSConfig = nil
			} else if mode == "hostname" {
				_, port, _ := net.SplitHostPort(options.Address)
				options.Address = net.JoinHostPort("localhost", port)
			} else if mode == "insecure" {
				options.TLSConfig.InsecureSkipVerify = true
			}
			err := IMAP(context.Background(), options, 1024, func(string, apitypes.ImportMessageBody) error { t.Fatal("visited invalid source"); return nil })
			if err == nil || strings.Contains(err.Error(), options.Password) || strings.Contains(err.Error(), options.Username) {
				t.Fatalf("invalid or non-private failure: %v", err)
			}
		})
	}
}

func TestIMAPFolderMappingIsValidatedBeforeImport(t *testing.T) {
	for _, tc := range []struct{ name, list string }{
		{"collision", "* LIST () \".\" \"a.b\"\r\n* LIST () \".\" \"a/b\"\r\n"},
		{"invalid", "* LIST () \"/\" \"../escape\"\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &imapSourceFixture{folders: tc.list}
			err := IMAP(context.Background(), newIMAPFixture(t, f), 1024, func(string, apitypes.ImportMessageBody) error { t.Fatal("imported invalid folder map"); return nil })
			if err == nil {
				t.Fatal("invalid folder map accepted")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, command := range f.commands {
				if command == "EXAMINE" {
					t.Fatal("folder mappings were not prevalidated")
				}
			}
		})
	}
}

func TestIMAPEmptySourceAndChangedSnapshot(t *testing.T) {
	for _, count := range []int{0, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			f := &imapSourceFixture{raw: "Subject: source\r\n\r\nbody", count: count}
			calls := 0
			err := IMAP(context.Background(), newIMAPFixture(t, f), 1024, func(string, apitypes.ImportMessageBody) error { calls++; return nil })
			if count == 0 {
				if err != nil || calls != 0 {
					t.Fatalf("empty source: calls=%d err=%v", calls, err)
				}
			} else if err == nil || calls != 1 || !strings.Contains(err.Error(), "omitted messages") {
				t.Fatalf("incomplete snapshot acknowledged: calls=%d err=%v", calls, err)
			}
		})
	}
}
