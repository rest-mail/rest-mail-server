package apiclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/rand"
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
// them. The text and the octets are what JSON can mangle: invalid UTF-8, characters straddling a
// chunk, what encoding/json escapes, and a NUL, which the API refuses by name.
func TestDeliverMessageFromSendsWhatDeliverMessageSends(t *testing.T) {
	straddling := strings.Repeat("x", jsonTextChunk-1) + "é" + strings.Repeat("y", jsonTextChunk-2) + "😀" + "tail"
	for name, tc := range map[string]struct{ text, html, raw string }{
		"plain":              {"body", "<b>x</b>", "From: a\r\n\r\nbody\r\n"},
		"not UTF-8":          {"A\xe9B\x80\xff", "\xfe<\xc3", "A\xe9B\x80\xff\xfe\x00 raw"},
		"escaped characters": {"\"\\\n\r\t\x00\x1f<>&  ", "", "x"},
		"straddling chunks":  {straddling, straddling, straddling},
		"cut character":      {"ends in \xe2\x82", "", "ends in \xe2\x82"},
		"no text":            {"", "", "only the raw message"},
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
			whole.BodyText, whole.BodyHTML, whole.RawMessage = tc.text, tc.html, []byte(tc.raw)
			if _, err := client.DeliverMessage(&whole); err != nil {
				t.Fatalf("DeliverMessage: %v", err)
			}
			content := DeliverContent{RawMessage: opener(tc.raw)}
			if tc.text != "" {
				content.BodyText = opener(tc.text)
			}
			if tc.html != "" {
				content.BodyHTML = opener(tc.html)
			}
			resp, err := client.DeliverMessageFrom(&small, content)
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
	if _, err := client.DeliverMessageFrom(&DeliverRequest{Address: "a@b.test"}, DeliverContent{
		RawMessage: opener("raw"), BodyText: opener("text"),
	}); err != nil {
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

// TestCopyJSONTextIsJSONMarshal compares the two over text made to go wrong at chunk edges, read
// in pieces of every sort of size.
func TestCopyJSONTextIsJSONMarshal(t *testing.T) {
	random := rand.New(rand.NewSource(1437))
	pieces := []string{"a", "é", "€", "😀", "\xe2", "\xe2\x82", "\xf0\x9f", "\xff", "\x00", "\"", "\\", "\n", "<", "&", " "}
	for round := 0; round < 200; round++ {
		var text strings.Builder
		size := random.Intn(3 * jsonTextChunk)
		for text.Len() < size {
			text.WriteString(pieces[random.Intn(len(pieces))])
		}
		var reader io.Reader = strings.NewReader(text.String())
		switch round % 3 {
		case 1:
			reader = iotest.OneByteReader(reader)
		case 2:
			reader = iotest.HalfReader(reader)
		}
		if round%3 == 1 && text.Len() > 4096 {
			continue // A byte at a time needs no more than the small rounds to show what it shows.
		}
		var got bytes.Buffer
		if err := copyJSONText(&got, reader); err != nil {
			t.Fatal(err)
		}
		want, err := json.Marshal(text.String())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(want[1:len(want)-1], got.Bytes()) {
			t.Fatalf("round %d differs for %d bytes of text", round, text.Len())
		}
	}
}
