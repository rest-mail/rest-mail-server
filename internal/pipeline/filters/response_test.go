package filters

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

type verdictPadding struct {
	read int64
}

func (p *verdictPadding) Read(buf []byte) (int, error) {
	for index := range buf {
		buf[index] = ' '
	}
	p.read += int64(len(buf))
	return len(buf), nil
}

func TestAC02FilterResponsesRefusedAtByteLimit(t *testing.T) {
	tests := []struct {
		name      string
		limit     int64
		verdict   string
		accepted  pipeline.Action
		fallback  pipeline.Action
		newFilter func(string) (pipeline.Filter, error)
	}{
		{"rspamd", 1 << 20, `{"action":"no action"}`, pipeline.ActionContinue, pipeline.ActionDefer,
			func(url string) (pipeline.Filter, error) { return NewRspamd([]byte(fmt.Sprintf(`{"url":%q}`, url))) }},
		{"clamav", 64 << 10, `{"status":"OK"}`, pipeline.ActionContinue, pipeline.ActionDefer,
			func(url string) (pipeline.Filter, error) { return NewClamAV([]byte(fmt.Sprintf(`{"url":%q}`, url))) }},
		{"webhook", 64 << 10, `{"action":"discard"}`, pipeline.ActionDiscard, pipeline.ActionContinue,
			func(url string) (pipeline.Filter, error) {
				filter, err := NewWebhook([]byte(fmt.Sprintf(`{"url":%q}`, url)))
				if err == nil {
					filter.(*webhookFilter).client.Transport = http.DefaultTransport
				}
				return filter, err
			}},
		{"javascript", 33 << 20, `{"result":{"type":"action","action":"discard"},"logs":[]}`, pipeline.ActionDiscard, pipeline.ActionDefer,
			func(url string) (pipeline.Filter, error) {
				return newJavaScript([]byte(`{"script":"function filter(e) { return e; }","timeout_ms":30000}`), JSSidecar{URL: url})
			}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, mode := range []string{"at-limit", "declared-oversize", "chunked-oversize", "gzip-oversize"} {
				t.Run(mode, func(t *testing.T) {
					var compressed bytes.Buffer
					if mode == "gzip-oversize" {
						gz := gzip.NewWriter(&compressed)
						if _, err := io.WriteString(gz, test.verdict); err != nil {
							t.Fatal(err)
						}
						if _, err := io.CopyN(gz, &verdictPadding{}, test.limit+1-int64(len(test.verdict))); err != nil {
							t.Fatal(err)
						}
						if err := gz.Close(); err != nil {
							t.Fatal(err)
						}
					}
					testDone := t.Context().Done()
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Method == http.MethodGet {
							w.WriteHeader(http.StatusOK)
							return
						}
						if mode == "declared-oversize" {
							w.Header().Set("Content-Length", strconv.FormatInt(test.limit+1, 10))
							w.(http.Flusher).Flush()
							<-testDone
							return
						}
						if mode == "gzip-oversize" {
							w.Header().Set("Content-Encoding", "gzip")
							_, _ = w.Write(compressed.Bytes())
							return
						}
						w.(http.Flusher).Flush()
						if _, err := io.WriteString(w, test.verdict); err != nil {
							return
						}
						size := test.limit
						if mode != "at-limit" {
							size++
						}
						if _, err := io.CopyN(w, &verdictPadding{}, size-int64(len(test.verdict))); err != nil {
							return
						}
						if mode == "chunked-oversize" {
							w.(http.Flusher).Flush()
							<-testDone
						}
					}))
					t.Cleanup(server.Close)
					filter, err := test.newFilter(server.URL)
					if err != nil {
						t.Fatal(err)
					}
					for _, direction := range []string{"inbound", "outbound"} {
						email := &pipeline.EmailJSON{Envelope: pipeline.Envelope{Direction: direction}}
						result, err := filter.Execute(t.Context(), email)
						if err != nil {
							t.Fatal(err)
						}
						if mode == "at-limit" {
							if result.Action != test.accepted || result.Log.Result == "error" {
								t.Fatalf("%s at-cap verdict: action %s, log %+v", direction, result.Action, result.Log)
							}
							continue
						}
						want := fmt.Sprintf("response exceeds %d bytes", test.limit)
						if result.Action != test.fallback || result.Log.Result != "error" || !strings.Contains(result.Log.Detail, want) {
							t.Fatalf("%s oversized verdict: action %s, log %.240q; want %s with %q", direction, result.Action, result.Log.Detail, test.fallback, want)
						}
						if result.Message != nil || len(email.Headers.Extra) != 0 {
							t.Fatal("oversized verdict changed the message")
						}
					}
				})
			}
		})
	}
}

func TestAC02FilterResponseReadBudgetAndAllocation(t *testing.T) {
	const capBytes = 64 << 10
	for _, size := range []int64{1 << 20, 8 << 20, 64 << 20} {
		t.Run(strconv.FormatInt(size, 10), func(t *testing.T) {
			padding := &verdictPadding{}
			response := &http.Response{ContentLength: -1, Body: io.NopCloser(io.LimitReader(padding, size))}
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			body, err := readFilterResponse(response, capBytes)
			runtime.ReadMemStats(&after)
			if err == nil || len(body) != 0 || padding.read != capBytes+1 {
				t.Fatalf("oversize response returned %d bytes, read %d bytes, error %v; want no body and %d reads", len(body), padding.read, err, capBytes+1)
			}
			allocated := after.TotalAlloc - before.TotalAlloc
			t.Logf("offered %d bytes, read %d, Go TotalAlloc delta %d bytes", size, padding.read, allocated)
			if allocated >= uint64(size) {
				t.Errorf("allocated %d bytes for %d-byte response; could hold a complete copy", allocated, size)
			}
		})
	}
	padding := &verdictPadding{}
	response := &http.Response{ContentLength: capBytes + 1, Body: io.NopCloser(padding)}
	if body, err := readFilterResponse(response, capBytes); err == nil || len(body) != 0 || padding.read != 0 {
		t.Fatalf("declared oversize response returned %d bytes after reading %d, error %v", len(body), padding.read, err)
	}
}

func TestAC02FilterHTTPTimeoutIncludesResponseBody(t *testing.T) {
	testDone := t.Context().Done()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.(http.Flusher).Flush()
		<-testDone
	}))
	t.Cleanup(server.Close)
	filter, err := NewWebhook([]byte(fmt.Sprintf(`{"url":%q,"timeout_ms":1000}`, server.URL)))
	if err != nil {
		t.Fatal(err)
	}
	filter.(*webhookFilter).client.Transport = http.DefaultTransport
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	result, err := filter.Execute(ctx, &pipeline.EmailJSON{})
	if err != nil || result.Log.Result != "error" || !strings.HasPrefix(result.Log.Detail, "read webhook response:") || !strings.Contains(result.Log.Detail, "Client.Timeout") || ctx.Err() != nil {
		t.Fatalf("stalled response: result %+v, error %v, context %v", result, err, ctx.Err())
	}

	const fallback = 5 * time.Second
	for _, value := range []int{math.MinInt, -1, 0, 30, math.MaxInt} {
		budget := filterHTTPTimeout(value, fallback, 0)
		want := fallback
		if value == 30 {
			want = 30 * time.Millisecond
		} else if value == math.MaxInt {
			want = time.Minute
		}
		if budget != want {
			t.Errorf("timeout_ms=%d: got %s, want %s", value, budget, want)
		}
	}
}
