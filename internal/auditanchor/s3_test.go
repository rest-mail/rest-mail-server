package auditanchor

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/audit"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

const lockedBucket = `<?xml version="1.0" encoding="UTF-8"?>
<ObjectLockConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><ObjectLockEnabled>Enabled</ObjectLockEnabled><Rule><DefaultRetention><Mode>COMPLIANCE</Mode><Days>3650</Days></DefaultRetention></Rule></ObjectLockConfiguration>`

const unlockedBucket = `<?xml version="1.0" encoding="UTF-8"?>
<ObjectLockConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><ObjectLockEnabled>Enabled</ObjectLockEnabled></ObjectLockConfiguration>`

// objectStore is a TLS S3 fixture for one bucket. It answers the Object Lock
// query with lockConfig and every object write with putStatus.
type objectStore struct {
	lockConfig string
	putStatus  int
	puts       atomic.Int32
	mu         sync.Mutex
	objects    map[string]string
}

func newObjectStore(t *testing.T, lockConfig string, putStatus int) (*objectStore, *httptest.Server) {
	t.Helper()
	f := &objectStore{lockConfig: lockConfig, putStatus: putStatus, objects: map[string]string{}}
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKIDANCHOR/") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/anchors" && r.URL.Query().Has("object-lock"):
			if f.lockConfig == "" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `<Error><Code>ObjectLockConfigurationNotFoundError</Code></Error>`)
				return
			}
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, f.lockConfig)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/anchors/"):
			f.puts.Add(1)
			raw, _ := io.ReadAll(r.Body)
			sum := md5.Sum(raw)
			if r.Header.Get("Content-MD5") != base64.StdEncoding.EncodeToString(sum[:]) || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/plain") {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if f.putStatus != http.StatusOK {
				w.WriteHeader(f.putStatus)
				_, _ = io.WriteString(w, `<Error><Code>Failure</Code></Error>`)
				return
			}
			f.mu.Lock()
			f.objects[strings.TrimPrefix(r.URL.Path, "/anchors/")] = string(raw)
			f.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotImplemented)
		}
	}))
	t.Cleanup(ts.Close)
	return f, ts
}

func s3Worker(t *testing.T, d *anchorDB, ts *httptest.Server) *S3Worker {
	t.Helper()
	w, err := newS3Worker(workerDB(t, d), "mx.example.test", config.AuditAnchorS3Config{
		Endpoint: ts.URL, Region: "eu-1", Bucket: "anchors", AccessKeyID: "AKIDANCHOR", SecretAccessKey: "anchor-secret",
		ForcePathStyle: true, Prefix: "mx/",
	}, ts.Client())
	if err != nil {
		t.Fatal(err)
	}
	w.target.backoff = time.Millisecond
	return w
}

func TestS3AnchorStoredInLockedBucket(t *testing.T) {
	d := workerFixture()
	store, ts := newObjectStore(t, lockedBucket, http.StatusOK)
	sent, err := s3Worker(t, d, ts).Check(context.Background(), testNow)
	if err != nil || !sent {
		t.Fatalf("Check: %v, %v", sent, err)
	}
	want := anchorBody("mx.example.test", testNow, d.head, audit.Anchor{Hash: audit.GenesisHash})
	key := "mx/00000000000000000042-" + testNow.UTC().Format("20060102T150405.000000Z") + ".txt"
	if len(store.objects) != 1 || store.objects[key] != want {
		t.Fatalf("stored objects %v; want the email anchor text at %s", store.objects, key)
	}
	e, target := httpsEntry(t, d)
	if e["action"] != s3Action || e["outcome"] != models.AuditOutcomeSuccess || e["seq"] != int64(43) {
		t.Fatalf("audit entry: %v", e)
	}
	if target["delivery"] != "accepted" || target["seq"] != "42" || target["hash"] != d.head.Hash || target["bucket"] != "anchors" || target["attempts"] != "1" {
		t.Fatalf("audit target: %v", target)
	}
	if strings.Contains(e["target"].(string), "anchor-secret") || strings.Contains(e["target"].(string), "AKIDANCHOR") {
		t.Fatalf("credentials recorded in the audit log: %v", target)
	}
}

func TestS3AnchorFailuresAreAuditedAndVisible(t *testing.T) {
	for _, tc := range []struct {
		name       string
		lockConfig string
		putStatus  int
		closed     bool
		puts       int32
		attempts   string
		delivery   string
		status     string
	}{
		{name: "bucket without object lock", putStatus: http.StatusOK, attempts: "1", delivery: "refused", status: "404"},
		{name: "object lock without default retention", lockConfig: unlockedBucket, putStatus: http.StatusOK, attempts: "1", delivery: "refused"},
		{name: "write refused", lockConfig: lockedBucket, putStatus: http.StatusForbidden, puts: 1, attempts: "1", delivery: "refused", status: "403"},
		{name: "server error retried", lockConfig: lockedBucket, putStatus: http.StatusServiceUnavailable, puts: 3, attempts: "3", delivery: "refused", status: "503"},
		{name: "unreachable", lockConfig: lockedBucket, closed: true, attempts: "3", delivery: "unreachable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := workerFixture()
			store, ts := newObjectStore(t, tc.lockConfig, tc.putStatus)
			w := s3Worker(t, d, ts)
			if tc.closed {
				ts.Close()
			}
			sent, err := w.Check(context.Background(), testNow)
			if err == nil || sent {
				t.Fatalf("failed write reported as stored: %v %v", sent, err)
			}
			if store.puts.Load() != tc.puts || len(store.objects) != 0 {
				t.Fatalf("object writes %d, stored %d; want %d writes and nothing stored", store.puts.Load(), len(store.objects), tc.puts)
			}
			e, target := httpsEntry(t, d)
			if e["action"] != s3Action || e["outcome"] != models.AuditOutcomeFailed || target["delivery"] != tc.delivery || target["attempts"] != tc.attempts || target["status"] != tc.status {
				t.Fatalf("failure audit: %v %v", e["outcome"], target)
			}
		})
	}
}

func TestS3AnchorScheduleAndReplicas(t *testing.T) {
	t.Run("hourly limit", func(t *testing.T) {
		d := workerFixture()
		d.previous = previousAnchor(testNow.Add(-time.Minute))
		store, ts := newObjectStore(t, lockedBucket, http.StatusOK)
		sent, err := s3Worker(t, d, ts).Check(context.Background(), testNow)
		if err != nil || sent || store.puts.Load() != 0 || len(d.entries) != 0 {
			t.Fatalf("published inside the hourly limit: %v %v puts=%d", sent, err, store.puts.Load())
		}
	})
	t.Run("replica owns publication lock", func(t *testing.T) {
		d := workerFixture()
		d.locked = false
		store, ts := newObjectStore(t, lockedBucket, http.StatusOK)
		sent, err := s3Worker(t, d, ts).Check(context.Background(), testNow)
		if err != nil || sent || store.puts.Load() != 0 {
			t.Fatalf("duplicate replica publication: %v %v", sent, err)
		}
	})
}

func TestNewS3WorkerRefusesPlainHTTP(t *testing.T) {
	c := config.AuditAnchorS3Config{Region: "eu-1", Bucket: "anchors", AccessKeyID: "AKIDANCHOR", SecretAccessKey: "anchor-secret", Prefix: "mx/"}
	for _, endpoint := range []string{"http://s3.example.test", "s3.example.test", "https://"} {
		c.Endpoint = endpoint
		if _, err := NewS3Worker(nil, "mx.example.test", c); err == nil {
			t.Errorf("accepted %q", endpoint)
		}
	}
}
