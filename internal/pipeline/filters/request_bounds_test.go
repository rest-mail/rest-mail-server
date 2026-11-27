package filters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

type requestBoundsCase struct {
	name      string
	newFilter func(url string, timeoutMS int) (pipeline.Filter, error)
	// fallback is the action a filter takes when its peer never answers.
	fallback pipeline.Action
}

func requestBoundsCases() []requestBoundsCase {
	return []requestBoundsCase{
		{"rspamd", func(url string, ms int) (pipeline.Filter, error) {
			return NewRspamd([]byte(fmt.Sprintf(`{"url":%q,"timeout_ms":%d}`, url, ms)))
		}, pipeline.ActionDefer},
		{"clamav", func(url string, ms int) (pipeline.Filter, error) {
			return NewClamAV([]byte(fmt.Sprintf(`{"url":%q,"timeout_ms":%d}`, url, ms)))
		}, pipeline.ActionDefer},
		{"webhook", func(url string, ms int) (pipeline.Filter, error) {
			filter, err := NewWebhook([]byte(fmt.Sprintf(`{"url":%q,"timeout_ms":%d}`, url, ms)))
			if err == nil {
				filter.(*webhookFilter).client.Transport = http.DefaultTransport
			}
			return filter, err
		}, pipeline.ActionContinue},
		{"javascript", func(url string, ms int) (pipeline.Filter, error) {
			return newJavaScript([]byte(fmt.Sprintf(`{"script":"function filter(e) { return e; }","timeout_ms":%d}`, ms)), JSSidecar{URL: url})
		}, pipeline.ActionDefer},
	}
}

func largeEmail(bytesOfText int) *pipeline.EmailJSON {
	return &pipeline.EmailJSON{
		Envelope: pipeline.Envelope{MailFrom: "a@example.test", RcptTo: []string{"b@example.test"}, Direction: "inbound"},
		Headers:  pipeline.Headers{Subject: "big"},
		Body:     pipeline.Body{ContentType: "text/plain", Content: strings.Repeat("x", bytesOfText)},
	}
}

// Every filter that posts the message to a peer sends one request whose declared length is
// the whole body, so a peer can refuse by Content-Length and the transport never streams an
// unannounced amount. The size is a function of the message alone: the filters add no
// padding or second copy of their own to the request.
func TestAC02FilterRequestsDeclareTheirLengthAndCarryOneCopy(t *testing.T) {
	const textBytes = 1 << 20
	email := largeEmail(textBytes)
	raw, err := rmime.Serialize(email)
	if err != nil {
		t.Fatal(err)
	}
	asJSON, err := json.Marshal(email)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range requestBoundsCases() {
		t.Run(test.name, func(t *testing.T) {
			type seen struct {
				length int64
				chunk  bool
				body   []byte
			}
			got := make(chan seen, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					return
				}
				body, _ := io.ReadAll(r.Body)
				got <- seen{r.ContentLength, len(r.TransferEncoding) > 0, body}
			}))
			t.Cleanup(server.Close)
			filter, err := test.newFilter(server.URL, 5000)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := filter.Execute(t.Context(), email); err != nil {
				t.Fatal(err)
			}
			req := <-got
			if req.chunk || req.length != int64(len(req.body)) {
				t.Fatalf("request length %d, chunked %v, body %d bytes", req.length, req.chunk, len(req.body))
			}
			switch test.name {
			case "rspamd", "clamav":
				if !bytes.Equal(req.body, raw) {
					t.Fatalf("body is %d bytes, want the %d-byte serialized message", len(req.body), len(raw))
				}
			case "webhook":
				if !bytes.Equal(req.body, asJSON) {
					t.Fatalf("body is %d bytes, want the %d-byte JSON message", len(req.body), len(asJSON))
				}
			case "javascript":
				// One envelope around one copy of the message and the script.
				if n := len(req.body); n < len(asJSON) || n > len(asJSON)+1024 {
					t.Fatalf("body is %d bytes for a %d-byte JSON message", n, len(asJSON))
				}
			}
		})
	}
}

// A peer that accepts the connection and never reads the upload must not hold a filter past
// its own limit: the HTTP timeout covers sending the request, not only waiting for the
// response, and the filter's context cuts the upload too. The message is far larger than
// the socket buffers, so the write itself blocks.
func TestAC02FilterRequestUploadIsBoundedByTimeoutAndContext(t *testing.T) {
	// 8 MiB is far larger than the socket buffers, so the write still blocks, but small
	// enough that serializing it (twice for the JS envelope) under -race on a CI runner
	// stays well inside the 5 s bound below. At 48 MiB the JS case spent most of its time
	// marshalling before the upload started.
	email := largeEmail(8 << 20)
	for _, test := range requestBoundsCases() {
		for _, mode := range []string{"http-timeout", "context-deadline"} {
			if test.name == "javascript" && mode == "http-timeout" {
				continue // its client limit is the script budget plus 5 s of headroom
			}
			t.Run(test.name+"/"+mode, func(t *testing.T) {
				testDone := t.Context().Done()
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet {
						return
					}
					<-testDone // never reads the body
				}))
				t.Cleanup(server.Close)
				timeoutMS := 500
				ctxLimit := 20 * time.Second
				if mode == "context-deadline" {
					// Give the HTTP timeout room so only the context can stop the upload.
					ctxLimit = 500 * time.Millisecond
					if test.name != "javascript" {
						timeoutMS = 50000
					}
				}
				filter, err := test.newFilter(server.URL, timeoutMS)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), ctxLimit)
				defer cancel()
				start := time.Now()
				result, err := filter.Execute(ctx, email)
				elapsed := time.Since(start)
				if elapsed > 5*time.Second {
					t.Fatalf("upload held the filter for %s", elapsed)
				}
				if err != nil {
					t.Fatalf("filter returned an error instead of its fallback: %v", err)
				}
				if result.Action != test.fallback || result.Log.Result != "error" {
					t.Fatalf("stalled upload: action %s, log %+v; want %s with an error log", result.Action, result.Log, test.fallback)
				}
			})
		}
	}
}
