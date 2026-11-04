package smtp

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

const (
	memorySmallMessage = 1 << 20
	memoryLargeMessage = 48 << 20
	// memoryGrowthAllowed is how much more a large message may hold at its peak than a small
	// one: far less than the difference between them, and room for buffers and the collector.
	memoryGrowthAllowed = 12 << 20
)

// memoryMessageShapes are the two ways a message can be large: a part the gateway passes over,
// and a text part it has to send on as body_text.
var memoryMessageShapes = []struct {
	name, head, line string
}{
	{
		name: "attachment",
		head: "From: s@remote.test\r\nTo: bob@example.com\r\nSubject: big\r\nMIME-Version: 1.0\r\n" +
			"Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\nhi\r\n" +
			"--b\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=x.bin\r\n" +
			"Content-Transfer-Encoding: base64\r\n\r\n",
		line: strings.Repeat("QUJD", 19),
	},
	{
		name: "text",
		head: "From: s@remote.test\r\nTo: bob@example.com\r\nSubject: big\r\n\r\n",
		line: strings.Repeat("words ", 12),
	},
}

// TestData_MemoryDoesNotGrowWithMessageSize passes a large message through the whole gateway,
// from the SMTP connection to the HTTP request the real API client makes, and holds what it
// keeps in memory at its peak to about what a small message costs (#1437).
func TestData_MemoryDoesNotGrowWithMessageSize(t *testing.T) {
	defer debug.SetGCPercent(debug.SetGCPercent(10))
	for _, shape := range memoryMessageShapes {
		t.Run(shape.name, func(t *testing.T) {
			small, _ := peakHeapForMessage(t, shape.head, shape.line, memorySmallMessage)
			large, uploaded := peakHeapForMessage(t, shape.head, shape.line, memoryLargeMessage)
			if uploaded < memoryLargeMessage {
				t.Fatalf("the API received %d bytes for a %d byte message", uploaded, memoryLargeMessage)
			}
			if growth := int64(large) - int64(small); growth > memoryGrowthAllowed {
				t.Fatalf("peak heap grew %d MiB between a %d MiB and a %d MiB message (%d MiB and %d MiB), want at most %d MiB",
					growth>>20, memorySmallMessage>>20, memoryLargeMessage>>20, small>>20, large>>20, memoryGrowthAllowed>>20)
			}
		})
	}
}

// peakHeapForMessage delivers one message of about size bytes and answers the most the heap held
// above where it started, and how many bytes reached the API.
func peakHeapForMessage(t *testing.T, head, line string, size int) (peak uint64, uploaded int64) {
	t.Helper()
	var received atomic.Int64
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/messages/deliver":
			n, _ := io.Copy(io.Discard, r.Body)
			received.Store(n)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"data":{"id":1,"mailbox_id":1}}`)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":{"exists":true,"address":"bob@example.com"}}`)
		}
	}))
	defer api.Close()

	h := newSMTPHarness(t, newMockBackend(), newMockStore(), false, func(s *Server) {
		s.api = apiclient.New(api.URL, apiclient.WithMessageDeadline(2*time.Minute))
		s.SetMaxMessageSize(2 * memoryLargeMessage)
	})
	h.ehlo()
	if r := h.cmd("MAIL FROM:<s@remote.test>"); !strings.HasPrefix(r, "250") {
		t.Fatalf("MAIL = %q", r)
	}
	if r := h.cmd("RCPT TO:<bob@example.com>"); !strings.HasPrefix(r, "250") {
		t.Fatalf("RCPT = %q", r)
	}
	if r := h.cmd("DATA"); !strings.HasPrefix(r, "354") {
		t.Fatalf("DATA = %q", r)
	}

	runtime.GC()
	var start runtime.MemStats
	runtime.ReadMemStats(&start)
	stop := make(chan struct{})
	sampled := make(chan uint64)
	go func() {
		var most uint64
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			var now runtime.MemStats
			runtime.ReadMemStats(&now)
			if now.HeapAlloc > most {
				most = now.HeapAlloc
			}
			select {
			case <-stop:
				sampled <- most
				return
			case <-ticker.C:
			}
		}
	}()

	_ = h.conn.SetWriteDeadline(time.Now().Add(3 * time.Minute))
	_ = h.conn.SetReadDeadline(time.Now().Add(3 * time.Minute))
	writeOrFail := func(text string) {
		if _, err := io.WriteString(h.cw, text); err != nil {
			t.Fatalf("write message: %v", err)
		}
	}
	writeOrFail(head)
	for written := len(head); written < size; written += len(line) + 2 {
		writeOrFail(line)
		writeOrFail("\r\n")
	}
	if strings.Contains(head, "boundary=b") {
		writeOrFail("--b--\r\n")
	}
	writeOrFail(".\r\n")
	if err := h.cw.Flush(); err != nil {
		t.Fatalf("flush message: %v", err)
	}
	reply, err := h.cr.ReadString('\n')
	close(stop)
	most := <-sampled
	if err != nil {
		t.Fatalf("reading the reply to DATA: %v", err)
	}
	if !strings.HasPrefix(reply, "250") {
		t.Fatalf("reply to DATA = %q, want 250", reply)
	}
	if most < start.HeapAlloc {
		return 0, received.Load()
	}
	if testing.Verbose() {
		fmt.Printf("%d MiB message: peak %d MiB above %d MiB\n", size>>20, (most-start.HeapAlloc)>>20, start.HeapAlloc>>20)
	}
	return most - start.HeapAlloc, received.Load()
}
