package auditanchor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/db/models"
)

// endpoint is a TLS anchor receiver answering every POST with status.
func endpoint(t *testing.T, status int, got *atomic.Int32, body *atomic.Value) *httptest.Server {
	t.Helper()
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Add(1)
		raw, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer anchor-token" || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/plain") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		body.Store(string(raw))
		w.WriteHeader(status)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func httpsWorker(t *testing.T, d *anchorDB, ts *httptest.Server) *HTTPSWorker {
	t.Helper()
	w, err := NewHTTPSWorker(workerDB(t, d), "mx.example.test", ts.URL+"/anchors", "anchor-token")
	if err != nil {
		t.Fatal(err)
	}
	w.target.client.Transport = ts.Client().Transport
	w.target.backoff = time.Millisecond
	return w
}

func httpsEntry(t *testing.T, d *anchorDB) (map[string]any, models.AuditTarget) {
	t.Helper()
	if len(d.entries) != 1 || d.committed != 1 {
		t.Fatalf("committed entries=%d transactions=%d; want one recorded attempt", len(d.entries), d.committed)
	}
	e := map[string]any{}
	for k, v := range d.entries[0] {
		e[k] = v
	}
	var target models.AuditTarget
	if err := json.Unmarshal([]byte(e["target"].(string)), &target); err != nil {
		t.Fatal(err)
	}
	return e, target
}

func TestHTTPSAnchorAccepted(t *testing.T) {
	var hits atomic.Int32
	var body atomic.Value
	d := workerFixture()
	w := httpsWorker(t, d, endpoint(t, http.StatusCreated, &hits, &body))
	sent, err := w.Check(context.Background(), testNow)
	if err != nil || !sent {
		t.Fatalf("Check: %v, %v", sent, err)
	}
	want := anchorBody("mx.example.test", testNow, d.head, audit.Anchor{Hash: audit.GenesisHash})
	if hits.Load() != 1 || body.Load() != want {
		t.Fatalf("posted %d times, body %q; want the email anchor text %q", hits.Load(), body.Load(), want)
	}
	e, target := httpsEntry(t, d)
	if e["action"] != httpsAction || e["outcome"] != models.AuditOutcomeSuccess || e["seq"] != int64(43) || e["prev_hash"] != d.head.Hash {
		t.Fatalf("audit entry: %v", e)
	}
	if target["delivery"] != "accepted" || target["seq"] != "42" || target["hash"] != d.head.Hash || target["previous_seq"] != "0" || target["status"] != "201" {
		t.Fatalf("audit target: %v", target)
	}
	if strings.Contains(e["target"].(string), "anchor-token") {
		t.Fatalf("bearer token recorded in the audit log: %v", target)
	}
}

func TestHTTPSAnchorFailuresAreAuditedAndVisible(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		closed   bool
		hits     int32
		attempts string
		delivery string
	}{
		{name: "refused", status: http.StatusForbidden, hits: 1, attempts: "1", delivery: "refused"},
		{name: "server error retried", status: http.StatusServiceUnavailable, hits: 3, attempts: "3", delivery: "refused"},
		{name: "unreachable", closed: true, attempts: "3", delivery: "unreachable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			var body atomic.Value
			d := workerFixture()
			ts := endpoint(t, tc.status, &hits, &body)
			w := httpsWorker(t, d, ts)
			if tc.closed {
				ts.Close()
			}
			sent, err := w.Check(context.Background(), testNow)
			if err == nil || sent {
				t.Fatalf("failed POST reported as sent: %v %v", sent, err)
			}
			if hits.Load() != tc.hits {
				t.Fatalf("endpoint hit %d times; want %d", hits.Load(), tc.hits)
			}
			e, target := httpsEntry(t, d)
			if e["action"] != httpsAction || e["outcome"] != models.AuditOutcomeFailed || target["delivery"] != tc.delivery || target["attempts"] != tc.attempts {
				t.Fatalf("failure audit: %v %v", e["outcome"], target)
			}
			// A failed attempt also waits out the hourly limit, so an endpoint
			// that stays down is recorded hourly rather than every minute.
			d.previous = models.AuditEntry{Seq: 43, OccurredAt: testNow, Action: httpsAction, Outcome: models.AuditOutcomeFailed}
			hits.Store(0)
			if sent, err := w.Check(context.Background(), testNow.Add(time.Minute)); err != nil || sent || hits.Load() != 0 || len(d.entries) != 1 {
				t.Fatalf("retried inside the hourly limit: %v %v hits=%d entries=%d", sent, err, hits.Load(), len(d.entries))
			}
		})
	}
}

func TestHTTPSAnchorScheduleAndReplicas(t *testing.T) {
	t.Run("hourly limit", func(t *testing.T) {
		var hits atomic.Int32
		var body atomic.Value
		d := workerFixture()
		d.previous = previousAnchor(testNow.Add(-time.Minute))
		sent, err := httpsWorker(t, d, endpoint(t, http.StatusOK, &hits, &body)).Check(context.Background(), testNow)
		if err != nil || sent || hits.Load() != 0 || len(d.entries) != 0 {
			t.Fatalf("published inside the hourly limit: %v %v hits=%d", sent, err, hits.Load())
		}
	})
	t.Run("replica owns publication lock", func(t *testing.T) {
		var hits atomic.Int32
		var body atomic.Value
		d := workerFixture()
		d.locked = false
		sent, err := httpsWorker(t, d, endpoint(t, http.StatusOK, &hits, &body)).Check(context.Background(), testNow)
		if err != nil || sent || hits.Load() != 0 {
			t.Fatalf("duplicate replica publication: %v %v", sent, err)
		}
	})
}

func TestNewHTTPSWorkerRefusesPlainHTTP(t *testing.T) {
	for _, endpoint := range []string{"http://anchors.example.test/", "anchors.example.test", "https:///anchors"} {
		if _, err := NewHTTPSWorker(nil, "mx.example.test", endpoint, ""); err == nil {
			t.Errorf("accepted %q", endpoint)
		}
	}
}
