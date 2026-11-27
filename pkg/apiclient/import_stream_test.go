package apiclient

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const importedAnswer = `{"data":{"id":9,"imported":true,"message_id":"m","folder":"Archive"}}`

// TestImportMessageFromSendsWhatImportMessageSends decodes the two requests as the API decodes
// them, and holds the streamed one to the Content-Length it declares (#2107).
func TestImportMessageFromSendsWhatImportMessageSends(t *testing.T) {
	for name, raw := range map[string]string{
		"plain":              "From: a\r\n\r\nbody\r\n",
		"empty":              "",
		"not UTF-8":          "A\xe9B\x80\xff\xfe\x00 raw",
		"escaped characters": "\"\\\n\r\t\x00\x1f<>&  ",
		"longer than chunks": strings.Repeat("x", 3*deliverChunk+2),
	} {
		t.Run(name, func(t *testing.T) {
			var bodies [][]byte
			var lengths []int64
			var queries []string
			srv, mux := newTestServer(t)
			mux.HandleFunc("/api/v1/accounts/7/messages/import", func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Authorization"); got != "Bearer tok" {
					t.Errorf("Authorization = %q", got)
				}
				if got := r.Header.Get("Content-Type"); got != "application/json" {
					t.Errorf("Content-Type = %q", got)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read body: %v", err)
				}
				bodies = append(bodies, body)
				lengths = append(lengths, r.ContentLength)
				queries = append(queries, r.URL.RawQuery)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, importedAnswer)
			})
			client := New(srv.URL)
			small := ImportRequest{Folder: "Archive <x>", Flags: []string{`\Seen`, "$Junk"}, InternalDate: time.Date(1994, 2, 7, 22, 43, 4, 0, time.FixedZone("", -8*3600)), AlwaysStore: true, MatchSubmission: true}
			whole := small
			whole.RawMessage = []byte(raw)
			if _, err := client.ImportMessage("tok", 7, "0123", &whole); err != nil {
				t.Fatalf("ImportMessage: %v", err)
			}
			resp, err := client.ImportMessageFrom("tok", 7, "0123", &small, ImportContent{Open: opener(raw), Size: int64(len(raw))})
			if err != nil {
				t.Fatalf("ImportMessageFrom: %v", err)
			}
			if resp.Data.ID != 9 || resp.Data.Folder != "Archive" {
				t.Errorf("answer = %+v", resp.Data)
			}
			if lengths[1] != int64(len(bodies[1])) {
				t.Errorf("Content-Length %d, body %d octets", lengths[1], len(bodies[1]))
			}
			if queries[0] != queries[1] {
				t.Errorf("query %q, want %q", queries[1], queries[0])
			}
			var want, got map[string]any
			if err := json.Unmarshal(bodies[0], &want); err != nil {
				t.Fatalf("decode ImportMessage's request: %v", err)
			}
			if err := json.Unmarshal(bodies[1], &got); err != nil {
				t.Fatalf("decode ImportMessageFrom's request: %v\n%.300s", err, bodies[1])
			}
			if bytes.Count(bodies[1], []byte(`"raw_message"`)) != 1 {
				t.Errorf("raw_message sent other than once: %.300s", bodies[1])
			}
			wantJSON, _ := json.Marshal(want)
			gotJSON, _ := json.Marshal(got)
			if !bytes.Equal(wantJSON, gotJSON) {
				t.Errorf("request differs:\n got %.400s\nwant %.400s", gotJSON, wantJSON)
			}
		})
	}
}

// TestImportMessageFromCanBeSentAgain is what the transport does on a connection that went stale:
// it asks for the body again, and has to get all of it.
func TestImportMessageFromCanBeSentAgain(t *testing.T) {
	client := New("http://api.test")
	var first, second []byte
	client.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var err error
		if first, err = io.ReadAll(r.Body); err != nil {
			return nil, err
		}
		_ = r.Body.Close()
		again, err := r.GetBody()
		if err != nil {
			return nil, err
		}
		defer again.Close()
		if second, err = io.ReadAll(again); err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(importedAnswer))}, nil
	})
	if _, err := client.ImportMessageFrom("tok", 7, "", &ImportRequest{Folder: "INBOX"}, ImportContent{Open: opener("raw"), Size: 3}); err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 || !bytes.Equal(first, second) {
		t.Fatalf("second send differs:\n%q\n%q", first, second)
	}
}
