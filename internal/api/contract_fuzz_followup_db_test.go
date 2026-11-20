package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

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

// The export car's two bodies each permitted, by their schemas, a request the handler refuses
// (#2051). Neither handler was wrong:
//
//   - Each kind of data is written in one format (exportsession.Formats): mail as maildir,
//     contacts as vcard, calendar as icalendar, settings as json. No exporter writes mail as
//     vCard, a contact card format. The schema now pairs them, so {"data":"mail","format":"vcard"}
//     is not schema-valid.
//   - A skipped folder is a name the exporter copied from the account's folder listing, kept in
//     the audit log for good: 1 to 255 bytes. The schema now says so of each item, so [""] is not
//     schema-valid. It does not take on the full folder rule: a name the listing returns is
//     recorded as it is, rather than refusing the export's end and losing its audit entry.
//
// Each test holds the schema and the registered route on PostgreSQL to the same verdict, body by
// body: schema-valid is answered 201 or 200, and anything else 422 naming the field.

func TestFuzzFindingExportFormatIsTheOneItsDataCarries(t *testing.T) {
	compiler := fuzzFindingCompiler()
	if err := compiler.AddResource("https://rest-mail.test/fuzz-findings.json", fuzzFindingDocument(t)); err != nil {
		t.Fatal(err)
	}
	schema := fuzzFindingSchema(t, compiler, fuzzFindingPointer("/api/v1/accounts/{id}/exports", "post")+"/requestBody/content/application~1json/schema")
	useRealGrants(t)
	f := newCredentialFixture(t)
	path := "/api/v1/accounts/" + strconv.FormatUint(uint64(f.mailboxID), 10) + "/exports"

	pairs := map[string]string{"mail": "maildir", "contacts": "vcard", "calendar": "icalendar", "settings": "json"}
	for _, data := range []string{"mail", "contacts", "calendar", "settings"} {
		for _, format := range []string{"maildir", "vcard", "icalendar", "json"} {
			body := `{"data":"` + data + `","format":"` + format + `"}`
			t.Run(data+"/"+format, func(t *testing.T) {
				want := pairs[data] == format
				value, err := jsonschema.UnmarshalJSON(strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				if err := schema.Validate(value); (err == nil) != want {
					t.Errorf("the schema permits %s: %t, want %t (%v)", body, err == nil, want, err)
				}
				response := f.do(http.MethodPost, path, f.session, body)
				if want {
					if response.Code != http.StatusCreated {
						t.Fatalf("%s: %d %s, want 201", body, response.Code, response.Body)
					}
					return
				}
				// The pairing is refused by the body schema, so the binder answers before
				// the handler can name a field: the refusal is real, its attribution is
				// the schema's. Asserting a field name here would be asserting a message
				// the server no longer produces.
				if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "validation_failed") {
					t.Fatalf("%s: %d %s, want 422 validation_failed", body, response.Code, response.Body)
				}
			})
		}
	}

	t.Run("described", func(t *testing.T) {
		operation := fuzzFindingOperation(t, "/api/v1/accounts/{id}/exports", "post")
		if description, _ := operation["description"].(string); !strings.Contains(description, "There is no other pairing: mail is not exported as vcard") {
			t.Errorf("createExportSession does not say in words that mail is not exported as vcard: %s", description)
		}
	})
}

func TestFuzzFindingExportSkippedFolderIsANameOfOneTo255Bytes(t *testing.T) {
	compiler := fuzzFindingCompiler()
	if err := compiler.AddResource("https://rest-mail.test/fuzz-findings.json", fuzzFindingDocument(t)); err != nil {
		t.Fatal(err)
	}
	schema := fuzzFindingSchema(t, compiler, fuzzFindingPointer("/api/v1/accounts/{id}/exports/{session}/close", "post")+"/requestBody/content/application~1json/schema")
	useRealGrants(t)
	f := newCredentialFixture(t)
	exports := "/api/v1/accounts/" + strconv.FormatUint(uint64(f.mailboxID), 10) + "/exports"

	for _, test := range []struct {
		name, folder string
		want         bool
	}{
		{"empty", "", false},
		{"space", " ", true},
		{"one", "a", true},
		{"nested", "Archive/2024", true},
		{"255_bytes", strings.Repeat("x", 255), true},
		{"256_bytes", strings.Repeat("x", 256), false},
		{"254_bytes_multibyte", strings.Repeat("é", 127), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			folders, err := json.Marshal([]string{"INBOX", test.folder})
			if err != nil {
				t.Fatal(err)
			}
			body := `{"result":"completed","rows":1,"bytes":1,"skipped_folders":` + string(folders) + `}`
			value, err := jsonschema.UnmarshalJSON(strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(value); (err == nil) != test.want {
				t.Errorf("the schema permits skipped folder %q: %t, want %t (%v)", test.folder, err == nil, test.want, err)
			}
			opened := f.do(http.MethodPost, exports, f.session, `{"data":"mail","format":"maildir"}`)
			if opened.Code != http.StatusCreated {
				t.Fatalf("open: %d %s", opened.Code, opened.Body)
			}
			var session struct{ Data struct{ ID string } }
			decodeInto(t, opened, &session)
			response := f.do(http.MethodPost, exports+"/"+session.Data.ID+"/close", f.session, body)
			if test.want {
				var closed struct {
					Data struct {
						SkippedFolders []string `json:"skipped_folders"`
					}
				}
				decodeInto(t, response, &closed)
				if response.Code != http.StatusOK || !reflect.DeepEqual(closed.Data.SkippedFolders, []string{"INBOX", test.folder}) {
					t.Fatalf("skipped folder %q: %d %s, want 200 recording it", test.folder, response.Code, response.Body)
				}
				return
			}
			if response.Code != http.StatusUnprocessableEntity || validationField(t, response.Body.Bytes(), "skipped_folders") == "" {
				t.Fatalf("skipped folder %q: %d %s, want 422 naming skipped_folders", test.folder, response.Code, response.Body)
			}
			// The refused close left the export open: it closes once the body is right.
			retry := f.do(http.MethodPost, exports+"/"+session.Data.ID+"/close", f.session, `{"result":"completed","rows":1,"bytes":1,"skipped_folders":["INBOX"]}`)
			if retry.Code != http.StatusOK {
				t.Fatalf("closing after the refusal: %d %s, want 200", retry.Code, retry.Body)
			}
		})
	}

	t.Run("described", func(t *testing.T) {
		operation := fuzzFindingOperation(t, "/api/v1/accounts/{id}/exports/{session}/close", "post")
		if description, _ := operation["description"].(string); !strings.Contains(description, "Each skipped folder is a name of 1 to 255 bytes") {
			t.Errorf("closeExportSession does not say in words what a skipped folder may be: %s", description)
		}
	})
}

func validationField(t *testing.T, body []byte, field string) string {
	t.Helper()
	var refusal struct {
		Error struct {
			Code    string
			Details struct{ Fields map[string]string }
		}
	}
	if err := json.Unmarshal(body, &refusal); err != nil || refusal.Error.Code != "validation_failed" {
		return ""
	}
	return refusal.Error.Details.Fields[field]
}

func fuzzFindingOperation(t *testing.T, path, method string) map[string]interface{} {
	t.Helper()
	doc := fuzzFindingDocument(t)
	return doc["paths"].(map[string]interface{})[path].(map[string]interface{})[method].(map[string]interface{})
}
