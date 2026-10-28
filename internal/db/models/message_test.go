package models

import (
	"encoding/json"
	"testing"

	rmail "github.com/restmail/restmail/internal/mail"
)

// TestMessage_JSONSerialization_RawSize pins the API wire form of a message:
// the handlers (ListMessages, GetMessage, DeliverMessage) marshal
// models.Message directly, so this JSON is exactly what API consumers — the
// IMAP/POP3 gateway apiclient included — receive. raw_size must be present
// (the exact stored-raw octet count gateways report as RFC822.SIZE / POP3
// LIST), size_bytes must be unchanged alongside it, and the raw message body
// itself must stay excluded.
func TestMessage_JSONSerialization_RawSize(t *testing.T) {
	raw := "From: a@example.com\r\nSubject: hi\r\n\r\nbody\r\n"
	msg := Message{
		ID:         42,
		Sender:     "a@example.com",
		Subject:    "hi",
		RawMessage: raw,
		RawSize:    len(raw),
		SizeBytes:  7,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	gotRawSize, ok := out["raw_size"]
	if !ok {
		t.Fatalf("serialized message missing raw_size field: %s", data)
	}
	if int(gotRawSize.(float64)) != len(raw) {
		t.Errorf("raw_size = %v, want %d", gotRawSize, len(raw))
	}
	if int(out["size_bytes"].(float64)) != 7 {
		t.Errorf("size_bytes = %v, want 7 (must be untouched by raw_size)", out["size_bytes"])
	}
	// RawMessage carries json:"-": the raw body must never leak into list or
	// detail responses (it is served only via the dedicated /raw endpoint).
	if _, leaked := out["raw_message"]; leaked {
		t.Errorf("raw_message leaked into serialized message: %s", data)
	}
	for k := range out {
		if k == "RawMessage" {
			t.Errorf("RawMessage leaked into serialized message: %s", data)
		}
	}
}

// TestMessage_JSONSerialization_RawSizeZeroPresent proves raw_size is emitted
// even when zero (no omitempty): gateway consumers distinguish "no stored
// raw" (0, fall back to size_bytes) from an absent field, and an additive API
// field must be observable for every row.
func TestMessage_JSONSerialization_RawSizeZeroPresent(t *testing.T) {
	data, err := json.Marshal(Message{ID: 1, SizeBytes: 9})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	v, ok := out["raw_size"]
	if !ok {
		t.Fatalf("raw_size absent when zero; want explicit 0: %s", data)
	}
	if int(v.(float64)) != 0 {
		t.Errorf("raw_size = %v, want 0", v)
	}
}

// TestMessage_BeforeCreate_MeasuresDowngradedSize proves every stored raw gets
// the size a client that has not enabled UTF-8 is sent: the downgraded form
// for UTF-8 headers, the raw size for ASCII headers.
func TestMessage_BeforeCreate_MeasuresDowngradedSize(t *testing.T) {
	for _, raw := range []string{
		"From: Jörg <joerg@bücher.example>\r\nSubject: Grüße\r\n\r\nHallo, Jörg.\r\n",
		"From: a@example.com\r\nSubject: hi\r\n\r\nbody with ü stays\r\n",
	} {
		m := Message{RawMessage: raw, RawSize: len(raw)}
		if err := m.BeforeCreate(nil); err != nil {
			t.Fatal(err)
		}
		if want := len(rmail.DowngradeHeaders(raw)); m.DowngradedSize != want {
			t.Fatalf("downgraded_size = %d, want %d for %q", m.DowngradedSize, want, raw)
		}
	}
	none := Message{}
	if err := none.BeforeCreate(nil); err != nil || none.DowngradedSize != 0 {
		t.Fatalf("message without raw: downgraded_size = %d, err %v", none.DowngradedSize, err)
	}
}
