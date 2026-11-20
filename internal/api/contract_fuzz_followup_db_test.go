package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// updateMessage's unchanged_since is compared with mod_seq, a PostgreSQL bigint, so the
// largest mod-sequence there can be is 2^63-1. The schema said only "at least 1", and a
// value from 2^63 to 2^64-1 decoded into the field and overflowed the comparison: 500
// internal_error for a schema-valid request (#2051). The schema now states the bigint's
// maximum, and a value above it is the 422 validation failure naming the field, through
// the production router on PostgreSQL, with the message left as it was.
func TestFuzzFindingUnchangedSinceAboveTheBigintMaximum(t *testing.T) {
	useRealGrants(t)
	f := newCredentialFixture(t)
	stamp := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	message := models.Message{ID: 1, MailboxID: f.mailboxID, Folder: "INBOX", MsgID: "<message@example.net>",
		Sender: "sender@example.net", SenderName: "Sender", RecipientsTo: models.JSONB(`["recipient@example.com"]`),
		RecipientsCc: models.JSONB(`[]`), Subject: "Subject", SizeBytes: 80,
		ReceivedAt: stamp, CreatedAt: stamp, UpdatedAt: stamp}
	if err := f.db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	read := func() models.Message {
		t.Helper()
		var stored models.Message
		if err := f.db.First(&stored, 1).Error; err != nil {
			t.Fatal(err)
		}
		return stored
	}

	for _, value := range []string{
		"9223372036854775808",  // the maximum plus one: 2^63
		"9223372036854775809",  // and the next
		"18446744073709551615", // the largest uint64
		"18446744073709551616", // past uint64
		"100000000000000000000000000000",
	} {
		t.Run("above/"+value, func(t *testing.T) {
			before := read()
			response := f.do(http.MethodPatch, "/api/v1/messages/1", f.session, `{"is_read":true,"unchanged_since":`+value+`}`)
			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("unchanged_since %s: %d %s, want 422", value, response.Code, response.Body)
			}
			var refusal struct {
				Error struct {
					Code    string
					Details struct{ Fields map[string]string }
				}
			}
			if err := json.Unmarshal(response.Body.Bytes(), &refusal); err != nil {
				t.Fatal(err)
			}
			if refusal.Error.Code != "validation_failed" || refusal.Error.Details.Fields["unchanged_since"] != "must be at most 9223372036854775807" {
				t.Fatalf("unchanged_since %s: %s, want validation_failed naming unchanged_since at most 9223372036854775807", value, response.Body)
			}
			if after := read(); !reflect.DeepEqual(before, after) {
				t.Fatalf("a refused unchanged_since changed the message: before=%+v after=%+v", before, after)
			}
		})
	}

	t.Run("at_the_maximum", func(t *testing.T) {
		before := read()
		response := f.do(http.MethodPatch, "/api/v1/messages/1", f.session, `{"is_read":true,"unchanged_since":9223372036854775807}`)
		if response.Code != http.StatusOK {
			t.Fatalf("unchanged_since 2^63-1: %d %s, want 200", response.Code, response.Body)
		}
		after := read()
		if !after.IsRead || after.ModSeq <= before.ModSeq {
			t.Fatalf("the change at the maximum: is_read %t, mod_seq %d after %d; want true and a higher mod_seq", after.IsRead, after.ModSeq, before.ModSeq)
		}
	})

	// The refusals below the range are unchanged: 400 invalid_mod_sequence.
	for _, value := range []string{"0", "-1", "-9223372036854775809"} {
		t.Run("below/"+value, func(t *testing.T) {
			response := f.do(http.MethodPatch, "/api/v1/messages/1", f.session, `{"is_read":false,"unchanged_since":`+value+`}`)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_mod_sequence"`) {
				t.Fatalf("unchanged_since %s: %d %s, want 400 invalid_mod_sequence", value, response.Code, response.Body)
			}
		})
	}
}

// What the description says of unchanged_since: at most the bigint's maximum, written as the
// exact integer. A bound that went through a float64 would read 9223372036854775808, one
// past what the column can hold, and permit the request that overflowed.
func TestUnchangedSinceDescribesTheBigintMaximum(t *testing.T) {
	data, err := json.Marshal(func() map[string]interface{} {
		doc, warnings := describedRouters(t).Description("test")
		if len(warnings) > 0 {
			t.Fatalf("the registered routes describe with warnings: %v", warnings)
		}
		return doc
	}())
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	var generated map[string]interface{}
	if err := decoder.Decode(&generated); err != nil {
		t.Fatal(err)
	}
	property := updateMessageProperty(t, generated, "unchanged_since")
	if got := property["maximum"]; got != json.Number("9223372036854775807") {
		t.Errorf("generated unchanged_since maximum = %v, want 9223372036854775807", got)
	}
	if got := property["minimum"]; got != json.Number("1") {
		t.Errorf("generated unchanged_since minimum = %v, want 1", got)
	}
	description, _ := property["description"].(string)
	if !strings.Contains(description, "9223372036854775807") || !strings.Contains(description, "422 validation_failed") {
		t.Errorf("generated unchanged_since description does not state the maximum and its refusal: %s", description)
	}

	oracle, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(oracle), "unchanged_since:\n          type: [integer, \"null\"]\n          minimum: 1\n          maximum: 9223372036854775807\n") {
		t.Errorf("openapi.yaml's UpdateMessageRequest.unchanged_since does not state maximum: 9223372036854775807")
	}
	if description == "" || !strings.Contains(string(oracle), "\n            "+description+"\n") {
		t.Errorf("openapi.yaml does not say of unchanged_since what the generated description says: %s", description)
	}
}

func updateMessageProperty(t *testing.T, doc map[string]interface{}, name string) map[string]interface{} {
	t.Helper()
	operation := doc["paths"].(map[string]interface{})[messageUpdatePath].(map[string]interface{})["patch"].(map[string]interface{})
	schema := operation["requestBody"].(map[string]interface{})["content"].(map[string]interface{})["application/json"].(map[string]interface{})["schema"].(map[string]interface{})
	if ref, ok := schema["$ref"].(string); ok {
		schema = doc["components"].(map[string]interface{})["schemas"].(map[string]interface{})[strings.TrimPrefix(ref, "#/components/schemas/")].(map[string]interface{})
	}
	property, ok := schema["properties"].(map[string]interface{})[name].(map[string]interface{})
	if !ok {
		t.Fatalf("updateMessage's body has no %s", name)
	}
	return property
}
