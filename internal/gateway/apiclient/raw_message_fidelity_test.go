package apiclient

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

// TestDeliverMessage_RawBytesRoundTripNonUTF8 proves the raw RFC 2822 wire bytes
// survive the gateway→API deliver transport byte-for-byte even when the message
// contains non-UTF-8 / invalid-UTF-8 octets — 8bit or binary
// content-transfer-encoding bodies, or undeclared-charset high bytes.
//
// Carrying the raw message as a plain JSON string corrupts these octets:
// encoding/json replaces every invalid-UTF-8 byte with U+FFFD on marshal, so
// "A\xe9B" becomes "A�B". That silently mangles what is stored and later
// served via IMAP FETCH BODY[] / POP3 RETR, breaking DKIM body hashes and
// byte fidelity. Regression test for issue #173.
func TestDeliverMessage_RawBytesRoundTripNonUTF8(t *testing.T) {
	// A message whose body carries raw high bytes that are NOT valid UTF-8:
	// a lone 0xe9 (latin-1 'é') between ASCII, plus a 0x80/0xff/0xfe run.
	rawWire := []byte("From: s@example.com\r\nSubject: bin\r\n\r\nA\xe9B\x80\xff\xfe body\r\n")

	var got []byte
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/messages/deliver", func(w http.ResponseWriter, r *http.Request) {
		// Decode into the production request type — the same contract the real
		// DeliverMessage handler uses — so the test exercises the true wire format.
		var req DeliverRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode deliver request: %v", err)
		}
		got = []byte(req.RawMessage)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"id": 1, "mailbox_id": 1, "subject": "bin"},
		})
	})

	c := New(srv.URL)
	if _, err := c.DeliverMessage(&DeliverRequest{
		Address:    "user@mail1.test",
		Sender:     "s@example.com",
		Subject:    "bin",
		RawMessage: rawWire,
	}); err != nil {
		t.Fatalf("DeliverMessage: %v", err)
	}

	if !bytes.Equal(got, rawWire) {
		t.Fatalf("raw message corrupted in transport:\n sent %d bytes: %x\n  got %d bytes: %x",
			len(rawWire), rawWire, len(got), got)
	}
}
