package smtp

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

type panickingDataReader struct{}

func (panickingDataReader) Read([]byte) (int, error) { panic("interrupted DATA copy") }

func TestAC01DATAFileCleansUpWhenSourcePanics(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	func() {
		defer func() {
			if got := recover(); got != "interrupted DATA copy" {
				t.Fatalf("spool recovered %v, want the original panic", got)
			}
		}()
		_, _ = spool(io.MultiReader(strings.NewReader("partial DATA"), panickingDataReader{}))
	}()
	if files := spoolEntries(t); len(files) != 0 {
		t.Fatalf("panicking DATA left %d spool file(s): %v", len(files), files)
	}
}

func TestAC01InboundDATAWaitsForDeliveryVerdict(t *testing.T) {
	for _, verdict := range []struct {
		status int
		body   string
		smtp   string
	}{
		{http.StatusCreated, `{"data":{"id":1,"mailbox_id":1}}`, "250"},
		{http.StatusForbidden, `{"error":{"code":"rejected","message":"test policy"}}`, "550"},
	} {
		t.Run(verdict.smtp, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			awaiting := make(chan error, 1)
			release := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path != "/api/v1/messages/deliver" {
					_, _ = io.WriteString(w, `{"data":{"exists":true,"address":"bob@example.com"}}`)
					return
				}
				_, err := io.Copy(io.Discard, r.Body)
				awaiting <- err
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				w.WriteHeader(verdict.status)
				_, _ = io.WriteString(w, verdict.body)
			}))
			defer server.Close()
			defer close(release)
			harness := newSMTPHarnessWith(t, apiclient.New(server.URL), newMockStore(), false, true)
			harness.ehlo()
			if got := harness.cmd("MAIL FROM:<sender@remote.test>"); replyCode(got) != "250" {
				t.Fatalf("MAIL: %s", got)
			}
			if got := harness.cmd("RCPT TO:<bob@example.com>"); replyCode(got) != "250" {
				t.Fatalf("RCPT: %s", got)
			}
			if got := harness.cmd("DATA"); replyCode(got) != "354" {
				t.Fatalf("DATA: %s", got)
			}
			harness.send("From: sender@remote.test\r\nTo: bob@example.com\r\nSubject: spool\r\n\r\nbody\r\n.")
			select {
			case err := <-awaiting:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("gateway did not upload DATA")
			}
			if files := spoolEntries(t); len(files) == 0 {
				t.Fatal("no DATA spool while delivery decision is pending")
			}
			if err := harness.conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			var timeout net.Error
			if line, err := harness.cr.ReadString('\n'); line != "" || !errors.As(err, &timeout) || !timeout.Timeout() {
				t.Fatalf("read SMTP reply before delivery verdict: %q (%v)", line, err)
			}
			release <- struct{}{}
			if _, got := harness.readReply(); replyCode(got) != verdict.smtp {
				t.Fatalf("DATA answer = %q after HTTP %d, want %s", got, verdict.status, verdict.smtp)
			}
			if files := spoolEntries(t); len(files) != 0 {
				t.Fatalf("DATA reply left spool files behind: %v", files)
			}
		})
	}
}
