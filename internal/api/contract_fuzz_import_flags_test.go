package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func TestFuzzFindingImportFlagSchemaMatchesStoredFlags(t *testing.T) {
	compiler := fuzzFindingCompiler()
	if err := compiler.AddResource("https://rest-mail.test/fuzz-findings.json", fuzzFindingDocument(t)); err != nil {
		t.Fatal(err)
	}
	schema := fuzzFindingSchema(t, compiler, "/components/schemas/ImportMessageBody/properties/flags/items")
	values := []string{"", "é", strings.Repeat("a", 255), strings.Repeat("a", 256), `\Seen`, `\sEeN`, `\Answered`, `\Flagged`, `\Draft`, `\Deleted`, `\Recent`, "$Junk", "two words"}
	for character := 0; character < 128; character++ {
		values = append(values, string(rune(character)))
	}
	for _, value := range values {
		t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
			_, err := goimap.NormalizeImportFlags([]string{value})
			if accepts := schema.Validate(value) == nil; accepts != (err == nil) {
				t.Errorf("schema accepts %t, storage accepts %t", accepts, err == nil)
			}
		})
	}
}

func TestFuzzFindingImportEmptyFlagIsRefusedWithoutWriting(t *testing.T) {
	useRealGrants(t)
	fixture := newCredentialFixture(t)
	fixture.own(fixture.mailboxID, fixture.address)
	if err := fixture.db.Create(&models.QuotaUsage{MailboxID: fixture.mailboxID}).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/accounts/%d/messages/import", fixture.mailboxID)
	for _, test := range []struct {
		name   string
		flags  []string
		status int
	}{
		{"empty_flag", []string{""}, http.StatusUnprocessableEntity},
		{"unknown_system_flag", []string{`\Recent`}, http.StatusUnprocessableEntity},
		{"keyword", []string{"a"}, http.StatusOK},
		{"mixed_case_system_flags", []string{`\sEeN`, `\aNsWeReD`, `\fLaGgEd`, `\dRaFt`, `\dElEtEd`}, http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			var before int64
			if err := fixture.db.Model(&models.Message{}).Count(&before).Error; err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(map[string]any{"raw_message": fuzzProbeRawMessage, "internal_date": "2020-01-02T03:04:05Z", "flags": test.flags, "always_store": true})
			if err != nil {
				t.Fatal(err)
			}
			response := fixture.do(http.MethodPost, path, fixture.session, string(body))
			if response.Code != test.status {
				t.Fatalf("got %d %s, want %d", response.Code, response.Body, test.status)
			}
			var after int64
			if err := fixture.db.Model(&models.Message{}).Count(&after).Error; err != nil {
				t.Fatal(err)
			}
			if test.status == http.StatusUnprocessableEntity {
				if after != before || !strings.Contains(response.Body.String(), `"flags":`) || !strings.Contains(response.Body.String(), `"code":"validation_failed"`) {
					t.Fatalf("refusal changed storage or failed to name flags: before %d, after %d: %s", before, after, response.Body)
				}
			} else if after != before+1 {
				t.Fatalf("accepted import stored %d messages, want 1", after-before)
			}
		})
	}
}
