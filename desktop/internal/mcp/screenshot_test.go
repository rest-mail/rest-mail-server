package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestScreenshotPrefersNativeCaptureWithoutBridge(t *testing.T) {
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 4, 3))); err != nil {
		t.Fatal(err)
	}
	_, discovery := startTestServer(t, Options{Screenshot: func(context.Context) ([]byte, error) { return buffer.Bytes(), nil }})
	result := call(t, connect(t, discovery), "ui_screenshot", map[string]any{})
	content, ok := result.Content[0].(*sdk.ImageContent)
	if result.IsError || !ok || !bytes.Equal(content.Data, buffer.Bytes()) {
		t.Fatalf("native PNG unavailable: %#v", result)
	}
}

func TestScreenshotFallsBackToBridgeAndLogsReason(t *testing.T) {
	var buffer bytes.Buffer
	png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 4, 3)))
	bridge := NewBridge(func(UIRequest) {})
	bridge.emit = func(request UIRequest) {
		bridge.Respond(UIResponse{ID: request.ID, OK: true, Result: map[string]any{"data": base64.StdEncoding.EncodeToString(buffer.Bytes())}})
	}
	server, discovery := startTestServer(t, Options{UI: bridge, Screenshot: func(context.Context) ([]byte, error) { return nil, errors.New("screen recording permission denied") }})
	result := call(t, connect(t, discovery), "ui_screenshot", map[string]any{})
	if result.IsError {
		t.Fatal(resultText(t, result))
	}
	if !strings.Contains(strings.Join(server.logs.Recent(10), "\n"), "screen recording permission denied") {
		t.Fatal("native failure reason missing from app_logs")
	}
}

// TestScreenshotFallbackGetsItsOwnDeadline covers a native capture that uses
// up its whole deadline: the bridge fallback must still get a live one.
func TestScreenshotFallbackGetsItsOwnDeadline(t *testing.T) {
	var buffer bytes.Buffer
	png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 4, 3)))
	bridge := NewBridge(func(UIRequest) {})
	// The webview answers on its own schedule, as the real event round trip does.
	bridge.emit = func(request UIRequest) {
		go func() {
			time.Sleep(20 * time.Millisecond)
			bridge.Respond(UIResponse{ID: request.ID, OK: true, Result: map[string]any{"data": base64.StdEncoding.EncodeToString(buffer.Bytes())}})
		}()
	}
	slowCapture := func(ctx context.Context) ([]byte, error) {
		<-ctx.Done()
		return nil, errors.New("native window capture failed: signal: killed")
	}
	_, discovery := startTestServer(t, Options{UI: bridge, Timeout: 300 * time.Millisecond, Screenshot: slowCapture})
	result := call(t, connect(t, discovery), "ui_screenshot", map[string]any{})
	if result.IsError {
		t.Fatalf("fallback inherited the spent native deadline: %s", resultText(t, result))
	}
}

func TestToolFailureLogsRedactedReason(t *testing.T) {
	const secret = "api-sensitive-value"
	bridge := NewBridge(func(UIRequest) {})
	bridge.emit = func(request UIRequest) {
		bridge.Respond(UIResponse{ID: request.ID, Error: "unsupported CSS color lab(): " + secret})
	}
	_, discovery := startTestServer(t, Options{
		UI:         bridge,
		Secrets:    func() []string { return []string{secret} },
		Screenshot: func(context.Context) ([]byte, error) { return nil, errors.New("native failed: " + secret) },
		Domain: DomainFunc(func(context.Context, string, json.RawMessage) (any, error) {
			return nil, errors.New("upstream unreachable: " + secret)
		}),
	})
	client := connect(t, discovery)
	for _, name := range []string{"session_status", "ui_snapshot", "ui_screenshot"} {
		if !call(t, client, name, map[string]any{}).IsError {
			t.Fatalf("expected %s error", name)
		}
	}
	text := resultText(t, call(t, client, "app_logs", map[string]any{}))
	for _, reason := range []string{"upstream unreachable", "unsupported CSS color lab()", "native failed"} {
		if !strings.Contains(text, reason) {
			t.Fatalf("missing %s in %s", reason, text)
		}
	}
	if strings.Contains(text, secret) {
		t.Fatalf("error log leaked API token: %s", text)
	}
}

func TestRedactedResultsRemainValidJSON(t *testing.T) {
	server, err := New(Options{Secrets: func() []string { return []string{"api-secret"} }})
	if err != nil {
		t.Fatal(err)
	}
	result := server.result(map[string]any{"text": `password="hidden"`, "api-secret": []string{"Authorization: Bearer api-secret"}})
	text := result.Content[0].(*sdk.TextContent).Text
	var decoded any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatalf("redaction corrupted JSON: %s: %v", text, err)
	}
	if strings.Contains(text, "hidden") || strings.Contains(text, "api-secret") {
		t.Fatalf("redaction leaked secret: %s", text)
	}
}
