package filters

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// capServer counts the requests that reach path, and the bytes of the last one.
type capServer struct {
	*httptest.Server
	scans atomic.Int64
	last  atomic.Int64
}

func newCapServer(t *testing.T, path, reply string) *capServer {
	t.Helper()
	s := &capServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		s.scans.Add(1)
		n, _ := io.Copy(io.Discard, r.Body)
		s.last.Store(n)
		_, _ = w.Write([]byte(reply))
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func TestFilterRequestCap_Scanners(t *testing.T) {
	raw, err := rmime.Serialize(unitEmail())
	if err != nil {
		t.Fatal(err)
	}
	size := int64(len(raw))

	cases := []struct {
		name    string
		factory func(limit int64) pipeline.FilterFactory
		path    string
		reply   string
	}{
		{"rspamd", func(l int64) pipeline.FilterFactory { return NewRspamdWithLimits("", l) }, "/checkv2", `{"action":"no action","score":0.1,"required_score":15}`},
		{"clamav", func(l int64) pipeline.FilterFactory { return NewClamAVWithLimits("", l) }, "/scan", `{"status":"OK"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/at limit is sent unchanged", func(t *testing.T) {
			srv := newCapServer(t, tc.path, tc.reply)
			f, err := tc.factory(size)([]byte(`{"url":"` + srv.URL + `","timeout_ms":2000,"fallback_action":"reject"}`))
			if err != nil {
				t.Fatal(err)
			}
			res, err := f.Execute(context.Background(), unitEmail())
			if err != nil {
				t.Fatal(err)
			}
			if srv.scans.Load() != 1 || srv.last.Load() != size {
				t.Fatalf("scans=%d bytes=%d, want 1 and %d", srv.scans.Load(), srv.last.Load(), size)
			}
			if res.Action != pipeline.ActionContinue {
				t.Fatalf("action=%q log=%+v", res.Action, res.Log)
			}
		})
		t.Run(tc.name+"/over limit is refused before any byte is sent", func(t *testing.T) {
			srv := newCapServer(t, tc.path, tc.reply)
			f, err := tc.factory(size - 1)([]byte(`{"url":"` + srv.URL + `","timeout_ms":2000,"fallback_action":"reject"}`))
			if err != nil {
				t.Fatal(err)
			}
			res, err := f.Execute(context.Background(), unitEmail())
			if err != nil {
				t.Fatal(err)
			}
			if n := srv.scans.Load(); n != 0 {
				t.Fatalf("%d scan requests reached the scanner, want 0", n)
			}
			// The same outcome as a scan that fails: the fallback action and an
			// "error" log.
			if res.Action != pipeline.ActionReject || res.Log.Result != "error" || res.Log.Filter != tc.name ||
				!strings.Contains(res.Log.Detail, tc.name+" scan failed") || !strings.Contains(res.Log.Detail, "over the limit") {
				t.Fatalf("action=%q log=%+v", res.Action, res.Log)
			}
		})
	}
}

func TestFilterRequestCap_Webhook(t *testing.T) {
	payload, err := json.Marshal(unitEmail())
	if err != nil {
		t.Fatal(err)
	}
	size := int64(len(payload))

	build := func(t *testing.T, limit int64, url string) pipeline.Filter {
		t.Helper()
		f, err := NewWebhookWithLimit(limit)([]byte(`{"url":"` + url + `","timeout_ms":2000}`))
		if err != nil {
			t.Fatal(err)
		}
		f.(*webhookFilter).client.Transport = http.DefaultTransport // loopback test server
		return f
	}

	t.Run("at limit is sent unchanged", func(t *testing.T) {
		srv := newCapServer(t, "/", `{"action":"continue"}`)
		res, err := build(t, size, srv.URL).Execute(context.Background(), unitEmail())
		if err != nil {
			t.Fatal(err)
		}
		if srv.scans.Load() != 1 || srv.last.Load() != size {
			t.Fatalf("requests=%d bytes=%d, want 1 and %d", srv.scans.Load(), srv.last.Load(), size)
		}
		if res.Log.Result != "continue" {
			t.Fatalf("log=%+v", res.Log)
		}
	})
	t.Run("over limit is refused before any byte is sent", func(t *testing.T) {
		srv := newCapServer(t, "/", `{"action":"reject"}`)
		res, err := build(t, size-1, srv.URL).Execute(context.Background(), unitEmail())
		if err != nil {
			t.Fatal(err)
		}
		if n := srv.scans.Load(); n != 0 {
			t.Fatalf("%d requests reached the webhook, want 0", n)
		}
		if res.Action != pipeline.ActionContinue || res.Log.Result != "error" ||
			!strings.HasPrefix(res.Log.Detail, "webhook failed: ") || !strings.Contains(res.Log.Detail, "over the limit") {
			t.Fatalf("action=%q log=%+v", res.Action, res.Log)
		}
	})
}
