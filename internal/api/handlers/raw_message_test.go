package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"hash"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// An id that is not one is binding's 422, before any message is looked up.
func TestRawMessageBindingBeforeDatabase(t *testing.T) {
	routes := messageRoutes(NewMessageHandler(nil, nil, nil, "", nil))
	for _, path := range []string{"/api/v1/messages/0/raw", "/api/v1/messages/abc/raw"} {
		rr := httptest.NewRecorder()
		routes.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), `"id":`) {
			t.Fatalf("%s: want 422 on id, got %d %s", path, rr.Code, rr.Body)
		}
	}
}

// The source is written byte for byte as message/rfc822; a message with none kept, one that
// is not there and one in another account are refused as before.
func TestRawMessage(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fx := seedMsgDeleteFixture(t, tx, 0)
	source := "Subject: café\r\n\r\nbody\r\n"
	kept := models.Message{MailboxID: fx.mailbox.ID, Folder: "INBOX", Sender: "a@b.test", RawMessage: source}
	none := models.Message{MailboxID: fx.mailbox.ID, Folder: "INBOX", Sender: "a@b.test"}
	for _, m := range []*models.Message{&kept, &none} {
		if err := tx.Create(m).Error; err != nil {
			t.Fatal(err)
		}
	}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), middleware.ClaimsKey, &auth.Claims{AccountID: fx.mailbox.ID})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	NewMessageHandler(tx, nil, nil, "", nil).RegisterMessages(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()})
	get := func(id uint) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d/raw", id), nil))
		return rr
	}

	rr := get(kept.ID)
	if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "message/rfc822" || rr.Body.String() != source {
		t.Fatalf("kept source: %d %q %q", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String())
	}
	if rr := get(none.ID); rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), `"not_found"`) {
		t.Fatalf("no source: %d %s", rr.Code, rr.Body)
	}
	if rr := get(none.ID + 1000); rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), `"message_not_found"`) {
		t.Fatalf("missing message: %d %s", rr.Code, rr.Body)
	}
}

// heapWatcher is a ResponseWriter that keeps nothing of the body but its digest and length,
// and records the most live heap there was while any of it was being written.
type heapWatcher struct {
	header http.Header
	status int
	digest hash.Hash
	n      int
	peak   uint64
}

func (w *heapWatcher) Header() http.Header { return w.header }
func (w *heapWatcher) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *heapWatcher) Flush() {}
func (w *heapWatcher) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	w.peak = max(w.peak, stats.HeapAlloc)
	w.digest.Write(p)
	w.n += len(p)
	return len(p), nil
}

// The raw route sends a source of any size holding at most a few chunks of it: the live heap
// while the body is on the wire stays far below the message, for a source kept as text and
// one kept as octets, and the octets and Content-Length are the stored source's (#2107).
func TestRawMessageMemoryDoesNotGrowWithMessageSize(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fx := seedMsgDeleteFixture(t, tx, 0)
	const size = 32 << 20
	const bound = 8 << 20
	type stored struct {
		name   string
		id     uint
		digest [sha256.Size]byte
	}
	var sources []stored
	for _, octets := range []bool{false, true} {
		id, digest := func() (uint, [sha256.Size]byte) {
			body := bytes.Repeat([]byte("0123456789 the quick brown fox café\r\n"), size/38)
			if octets {
				body[len(body)/2] = 0xff // not UTF-8, so the row keeps raw_octets
			}
			source := append([]byte("Subject: large\r\n\r\n"), body...)
			m := models.Message{MailboxID: fx.mailbox.ID, Folder: "INBOX", Sender: "a@b.test", RawMessage: string(source)}
			// Silent: the slow-statement log would print the whole source.
			if err := tx.Session(&gorm.Session{Logger: logger.Discard}).Create(&m).Error; err != nil {
				t.Fatal(err)
			}
			if got := m.RawOctets != nil; got != octets {
				t.Fatalf("raw_octets kept %v, want %v", got, octets)
			}
			return m.ID, sha256.Sum256(source)
		}()
		sources = append(sources, stored{name: fmt.Sprintf("octets=%v", octets), id: id, digest: digest})
	}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), middleware.ClaimsKey, &auth.Claims{AccountID: fx.mailbox.ID})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	NewMessageHandler(tx, nil, nil, "", nil).RegisterMessages(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()})
	for _, s := range sources {
		t.Run(s.name, func(t *testing.T) {
			runtime.GC()
			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			w := &heapWatcher{header: http.Header{}, digest: sha256.New()}
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d/raw", s.id), nil))
			if w.status != http.StatusOK || w.header.Get("Content-Type") != "message/rfc822" {
				t.Fatalf("got %d %q", w.status, w.header.Get("Content-Type"))
			}
			if [sha256.Size]byte(w.digest.Sum(nil)) != s.digest {
				t.Fatalf("sent %d octets that are not the stored source", w.n)
			}
			grew := int64(w.peak) - int64(before.HeapAlloc)
			t.Logf("%d-octet source: live heap while sending grew by %d octets", w.n, grew)
			if grew > bound {
				t.Fatalf("live heap grew by %d octets while sending a %d-octet source, want at most %d", grew, w.n, bound)
			}
			if got := w.header.Get("Content-Length"); got != strconv.Itoa(w.n) {
				t.Fatalf("Content-Length %q, sent %d octets", got, w.n)
			}
		})
	}
}

// A message that goes away while its source is being sent ends the answer short rather than
// sending anything else: streamRawSource fails at the first chunk it cannot read in full.
func TestStreamRawSourceStopsWhenTheMessageGoesAway(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fx := seedMsgDeleteFixture(t, tx, 0)
	source := "Subject: x\r\n\r\n" + strings.Repeat("y", 2*rawSourceChunk+5)
	m := models.Message{MailboxID: fx.mailbox.ID, Folder: "INBOX", Sender: "a@b.test", RawMessage: source}
	if err := tx.Create(&m).Error; err != nil {
		t.Fatal(err)
	}
	size, err := rawSourceLength(tx, m.ID)
	if err != nil || size != int64(len(source)) {
		t.Fatalf("length %d %v, want %d", size, err, len(source))
	}
	var sent bytes.Buffer
	gone := writerFunc(func(p []byte) (int, error) {
		if sent.Len() == 0 {
			if err := tx.Exec("DELETE FROM messages WHERE id = ?", m.ID).Error; err != nil {
				t.Fatal(err)
			}
		}
		return sent.Write(p)
	})
	if err := streamRawSource(context.Background(), tx, m.ID, size, gone); err == nil {
		t.Fatal("a source whose message went away was sent as complete")
	}
	if sent.String() != source[:rawSourceChunk] {
		t.Fatalf("sent %d octets, want only the first chunk", sent.Len())
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
