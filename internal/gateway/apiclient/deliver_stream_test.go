package apiclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func opener(content string) func() io.Reader {
	return func() io.Reader { return strings.NewReader(content) }
}

// roundTripFunc lets a test stand where the transport stands.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const deliveredAnswer = `{"data":{"id":7,"mailbox_id":1,"subject":"s"}}`

func deliveredResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusCreated,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(deliveredAnswer)),
	}
}

// TestDeliverMessageFromSendsWhatDeliverMessageSends decodes the two requests as the API decodes
// them. The octets are what JSON can mangle: invalid UTF-8, a source longer than a chunk, what
// encoding/json escapes, and a NUL, which the API refuses by name.
func TestDeliverMessageFromSendsWhatDeliverMessageSends(t *testing.T) {
	for name, raw := range map[string]string{
		"plain":              "From: a\r\n\r\nbody\r\n",
		"not UTF-8":          "A\xe9B\x80\xff\xfe\x00 raw",
		"escaped characters": "\"\\\n\r\t\x00\x1f<>&  ",
		"longer than chunks": strings.Repeat("x", 3*deliverChunk+1),
		"cut character":      "ends in \xe2\x82",
	} {
		t.Run(name, func(t *testing.T) {
			var bodies [][]byte
			srv, mux := newTestServer(t)
			mux.HandleFunc("/api/v1/messages/deliver", func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Content-Type"); got != "application/json" {
					t.Errorf("Content-Type = %q", got)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read body: %v", err)
				}
				bodies = append(bodies, body)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, deliveredAnswer)
			})
			client := New(srv.URL)
			tls := true
			small := DeliverRequest{Address: "user@mail1.test", Sender: "s@example.com", Subject: "subject <x>", RecipientsTo: json.RawMessage(`["a@b.test"]`), ReceivedTLS: &tls}
			whole := small
			whole.RawMessage = []byte(raw)
			if _, err := client.DeliverMessage(&whole); err != nil {
				t.Fatalf("DeliverMessage: %v", err)
			}
			resp, err := client.DeliverMessageFrom(&small, DeliverContent{RawMessage: opener(raw)})
			if err != nil {
				t.Fatalf("DeliverMessageFrom: %v", err)
			}
			if resp.Data.ID != 7 {
				t.Errorf("answer = %+v", resp.Data)
			}

			var want, got map[string]any
			if err := json.Unmarshal(bodies[0], &want); err != nil {
				t.Fatalf("decode DeliverMessage's request: %v", err)
			}
			if err := json.Unmarshal(bodies[1], &got); err != nil {
				t.Fatalf("decode DeliverMessageFrom's request: %v\n%.300s", err, bodies[1])
			}
			wantJSON, _ := json.Marshal(want)
			gotJSON, _ := json.Marshal(got)
			if !bytes.Equal(wantJSON, gotJSON) {
				t.Errorf("request differs:\n got %.400s\nwant %.400s", gotJSON, wantJSON)
			}
		})
	}
}

// TestDeliverMessageFromEncodesAnEmptyRequest keeps the object well formed with nothing before the
// large fields, and with nothing at all.
func TestDeliverMessageFromEncodesAnEmptyRequest(t *testing.T) {
	for name, content := range map[string]DeliverContent{
		"raw message only": {RawMessage: opener("x")},
		"nothing":          {},
	} {
		t.Run(name, func(t *testing.T) {
			var body bytes.Buffer
			if err := content.encode(&body, []byte("{}")); err != nil {
				t.Fatal(err)
			}
			if !json.Valid(body.Bytes()) {
				t.Fatalf("not JSON: %s", body.String())
			}
		})
	}
}

// TestDeliverMessageFromCanBeSentAgain is what the transport does on a connection that went stale:
// it asks for the body again, and has to get all of it. The body held in memory could always do
// this.
func TestDeliverMessageFromCanBeSentAgain(t *testing.T) {
	client := New("http://api.test")
	var first, second []byte
	client.internalHTTPClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var err error
		if first, err = io.ReadAll(r.Body); err != nil {
			return nil, err
		}
		_ = r.Body.Close()
		if r.GetBody == nil {
			t.Fatal("the request cannot be sent again")
		}
		again, err := r.GetBody()
		if err != nil {
			return nil, err
		}
		defer again.Close()
		if second, err = io.ReadAll(again); err != nil {
			return nil, err
		}
		return deliveredResponse(), nil
	})
	if _, err := client.DeliverMessageFrom(&DeliverRequest{Address: "a@b.test"}, DeliverContent{RawMessage: opener("raw")}); err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 || !bytes.Equal(first, second) {
		t.Fatalf("second send differs:\n%q\n%q", first, second)
	}
}

// TestDeliverMessageFromStopsWhenTheAPIRefuses answers the refusal without the API reading the
// message, and leaves nothing running: a refusal must not hold the file open behind it.
func TestDeliverMessageFromStopsWhenTheAPIRefuses(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/messages/deliver", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"mailbox_not_found"}`)
	})
	before := runtime.NumGoroutine()
	client := New(srv.URL)
	endless := func() io.Reader { return strings.NewReader(strings.Repeat("x", 8<<20)) }
	_, err := client.DeliverMessageFrom(&DeliverRequest{Address: "a@b.test"}, DeliverContent{RawMessage: endless})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		t.Fatalf("error = %v, want the 404", err)
	}
	client.internalHTTPClient.CloseIdleConnections()
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before+2 {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines = %d, started with %d", runtime.NumGoroutine(), before)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestDeliverMessageFromReportsAFileThatCannotBeRead fails the request: a truncated message must
// not reach the API looking complete.
func TestDeliverMessageFromReportsAFileThatCannotBeRead(t *testing.T) {
	broken := errors.New("disk failure")
	var body bytes.Buffer
	err := DeliverContent{RawMessage: func() io.Reader {
		return io.MultiReader(strings.NewReader("partial"), iotest.ErrReader(broken))
	}}.encode(&body, []byte(`{"address":"a"}`))
	if !errors.Is(err, broken) {
		t.Fatalf("error = %v, want %v", err, broken)
	}
}
