package api

import (
	"encoding/json"
	"github.com/restmail/restmail/internal/examplepairs"
	"os"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	rmime "github.com/restmail/restmail/internal/mime"
)

type forwardScenario struct {
	name, mode string
	status     int
}

var forwardScenarios = []forwardScenario{
	{"with_attachments", "", 201}, {"unauthenticated", "anonymous", 401}, {"not_authorized", "denied", 403},
	{"mime_too_large", "size", 413}, {"mailbox_full", "full", 422},
	{"source_unavailable", "source_failure", 500}, {"missing_object", "missing_object", 500}, {"checksum_mismatch", "checksum", 500},
	{"attachment_write_failed", "attachment_failure", 500}, {"queue_write_failed", "queue_failure", 500}, {"quota_write_failed", "quota_failure", 500},
}
var forwardExampleRequest = json.RawMessage(`{"from":"sender@example.test","to":["target@remote.test"],"body_text":"please review"}`)

// Only entropy and timestamps are normalized, after the response, stored Sent
// source and queued source agree. MIME size and attachment flags stay observed.
func normalizeForwardExample(t *testing.T, f *forwardFixture, status int, raw []byte) []byte {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if status == 201 {
		data := body["data"].(map[string]any)
		if len(f.c.state.messages) != 1 || len(f.c.queue) != 1 || len(f.c.attachments) != 3 {
			t.Fatal("success omitted stored work")
		}
		sent := f.c.state.messages[0]
		source := sent["raw_message"].(string)
		parsed, err := rmime.Parse([]byte(source))
		if err != nil {
			t.Fatal(err)
		}
		if data["message_id"] != sent["message_id"] || data["message_id"] != parsed.Headers.MessageID || f.c.queue[0]["raw_message"] != source || data["raw_size"] != float64(len(source)) || data["has_attachments"] != true {
			t.Fatal("response, Sent and queue identity/source disagree")
		}
		for _, field := range []string{"received_at", "created_at", "updated_at"} {
			stamp, err := time.Parse(time.RFC3339Nano, data[field].(string))
			if err != nil || time.Since(stamp) < -time.Second || time.Since(stamp) > time.Minute {
				t.Fatal("sent timestamp is not current")
			}
			data[field] = "2030-01-02T03:04:05Z"
		}
		data["message_id"] = "<00000000-0000-4000-8000-000000000001@example.test>"
		data["thread_id"] = data["message_id"]
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestForwardAttachmentsCanonicalExamples(t *testing.T) {
	useRealGrants(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	recorded := oapifly.ExampleFile{"forwardMessage": {}}
	for _, tc := range forwardScenarios {
		t.Run(tc.name, func(t *testing.T) {
			f := newForwardFixture(t, tc.mode)
			if tc.mode == "anonymous" {
				f.token = ""
			}
			w := f.request(t)
			if w.Code != tc.status {
				t.Fatalf("got%d want%d: %s", w.Code, tc.status, w.Body)
			}
			response := normalizeForwardExample(t, f, w.Code, w.Body.Bytes())
			pair := oapifly.ExamplePair{Request: oapifly.ExampleRequest{Path: map[string]any{"id": 7}, Body: forwardExampleRequest, MediaType: "application/json"}, Response: oapifly.ExampleResponse{Status: w.Code, Body: response, MediaType: "application/json"}}
			recorded["forwardMessage"][tc.name] = pair
			if os.Getenv("RESTMAIL_FORWARD_RECORD") == "" {
				want, ok := pairs["forwardMessage"][tc.name]
				if !ok {
					t.Fatal("canonical forward pair missing")
				}
				if want.Response.Status != w.Code {
					t.Fatal("canonical status differs")
				}
				assertPublicExampleBody(t, want.Response.Body, response)
				if string(mustImportJSON(t, want.Request.Path)) != string(mustImportJSON(t, pair.Request.Path)) {
					t.Fatal("canonical target differs")
				}
				var a, b any
				_ = json.Unmarshal(want.Request.Body, &a)
				_ = json.Unmarshal(pair.Request.Body, &b)
				if string(mustImportJSON(t, a)) != string(mustImportJSON(t, b)) {
					t.Fatal("canonical request differs")
				}
			}
		})
	}
	if path := os.Getenv("RESTMAIL_FORWARD_RECORD"); path != "" {
		data, err := oapifly.MarshalExamples(recorded)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
