package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	yaml "go.yaml.in/yaml/v2"
)

func TestMailboxUpdate_ContractPasswordSchema(t *testing.T) {
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Required   []string `yaml:"required"`
				Properties map[string]struct {
					MinLength int `yaml:"minLength"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
		Paths map[string]struct {
			Patch struct {
				Responses map[string]struct {
					Content map[string]struct {
						Schema struct {
							Ref string `yaml:"$ref"`
						} `yaml:"schema"`
					} `yaml:"content"`
				} `yaml:"responses"`
			} `yaml:"patch"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatal(err)
	}
	schema := doc.Components.Schemas["UpdateMailboxRequest"]
	if got := schema.Properties["password"].MinLength; got != minChosenPasswordLength {
		t.Errorf("password minLength = %d, want %d", got, minChosenPasswordLength)
	}
	for _, field := range schema.Required {
		if field == "password" {
			t.Error("a mailbox patch must still allow omitting the password")
		}
	}
	response := doc.Paths["/api/v1/admin/mailboxes/{id}"].Patch.Responses["422"]
	if got := response.Content["application/json"].Schema.Ref; got != "#/components/schemas/ValidationErrorResponse" {
		t.Errorf("422 schema = %q, want ValidationErrorResponse", got)
	}
}

func TestMailboxUpdate_ContractPasswordBoundary(t *testing.T) {
	// Password changes touch the append-only audit log, so use its existing
	// isolated database helper rather than migrating or writing shared tables.
	db := openAuditTestDB(t)
	var database string
	if err := db.Raw("SELECT current_database()").Scan(&database).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(database, "restmail_auditapi_") {
		t.Fatalf("refusing to write to non-scratch database %q", database)
	}
	t.Logf("verified isolated database %s", database)
	if err := db.AutoMigrate(accountModels...); err != nil {
		t.Fatal(err)
	}
	h := NewMailboxHandler(db, nil, nil)
	for _, length := range []int{0, 1, 11, 12, 13} {
		t.Run(fmt.Sprintf("length_%d", length), func(t *testing.T) {
			mb := seedMailbox(t, db, "password-boundary")
			// Compare persisted timestamps: PostgreSQL rounds Go's nanoseconds.
			if err := db.First(&mb, mb.ID).Error; err != nil {
				t.Fatal(err)
			}
			password := strings.Repeat("x", length)
			body := fmt.Sprintf(`{"password":%q,"display_name":"Updated","active":false}`, password)
			rr := mailboxUpdate(t, h, mb.ID, body)
			want := http.StatusOK
			if length < minChosenPasswordLength {
				want = http.StatusUnprocessableEntity
			}
			if rr.Code != want {
				t.Fatalf("status = %d, want %d: %s", rr.Code, want, rr.Body)
			}
			var stored models.Mailbox
			if err := db.First(&stored, mb.ID).Error; err != nil {
				t.Fatal(err)
			}
			credentials := repositories.NewCredentialRepository(db)
			if want == http.StatusUnprocessableEntity {
				var response struct {
					Error struct {
						Code    string `json:"code"`
						Details struct {
							Fields map[string]string `json:"fields"`
						} `json:"details"`
					} `json:"error"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if response.Error.Code != "validation_failed" || response.Error.Details.Fields["password"] == "" {
					t.Errorf("missing password validation error: %s", rr.Body)
				}
				if stored.DisplayName != mb.DisplayName || stored.Active != mb.Active || stored.CredentialEpoch != mb.CredentialEpoch || !stored.UpdatedAt.Equal(mb.UpdatedAt) {
					t.Errorf("refused update changed mailbox: %+v", stored)
				}
				password = "password123" // The old credential must remain usable.
			} else if stored.DisplayName != "Updated" || stored.Active {
				t.Errorf("valid password update lost accompanying fields: %+v", stored)
			}
			if credential, err := credentials.MatchPassword(mb.ID, password, time.Now()); err != nil || credential == nil {
				t.Fatalf("expected password does not match: %v", err)
			}
		})
	}
	t.Run("password_omitted", func(t *testing.T) {
		mb := seedMailbox(t, db, "password-omitted")
		rr := mailboxUpdate(t, h, mb.ID, `{"display_name":"Updated"}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body)
		}
		if credential, err := repositories.NewCredentialRepository(db).MatchPassword(mb.ID, "password123", time.Now()); err != nil || credential == nil {
			t.Fatalf("omitting password changed the old credential: %v", err)
		}
	})
}
