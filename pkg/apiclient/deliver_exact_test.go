package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestExactDeliveryBody(t *testing.T) {
	raw := strings.Repeat("\x00\xffdot\r\n", deliverChunk)
	for _, mismatch := range []int64{-1, 0, 1} {
		ctx, cancel := context.WithCancel(t.Context())
		body, err := NewExactDeliveryBody(ctx, map[string]string{"from": "sender@example.test"},
			io.NopCloser(strings.NewReader(raw)), int64(len(raw))+mismatch)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		wire, err := io.ReadAll(body)
		cancel()
		_ = body.Close()
		body.Wait()
		if mismatch != 0 {
			if err == nil || body.Complete() {
				t.Fatalf("recorded size off by %d produced a complete body", mismatch)
			}
			continue
		}
		var payload struct {
			From string
			Raw  []byte `json:"raw_message"`
		}
		if err != nil || !body.Complete() || body.Length != int64(len(wire)) {
			t.Fatalf("encoded length=%d, declared=%d, err=%v", len(wire), body.Length, err)
		}
		if err := json.Unmarshal(wire, &payload); err != nil || !bytes.Equal(payload.Raw, []byte(raw)) || payload.From != "sender@example.test" {
			t.Fatalf("wrong delivery fields (%d source octets): %v", len(payload.Raw), err)
		}
	}
}

func TestExactDeliveryBodyCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	raw, writer := io.Pipe()
	stop := context.AfterFunc(ctx, func() { _ = writer.CloseWithError(ctx.Err()) })
	defer stop()
	body, err := NewExactDeliveryBody(ctx, struct{}{}, raw, 1)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	if _, err := io.ReadAll(body); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled body returned %v", err)
	}
	_ = body.Close()
	body.Wait()
}
