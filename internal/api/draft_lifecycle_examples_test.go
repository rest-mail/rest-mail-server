package api

import (
	"encoding/json"
	"github.com/restmail/restmail/internal/examplepairs"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
)

var draftExampleNames = map[string][]string{
	"saveDraft":   {"empty", "with_sender", "wrong_type", "invalid_recipient", "malformed_json", "foreign_sender", "request_too_large"},
	"updateDraft": {"example", "empty_patch", "missing", "not_draft", "wrong_type", "invalid_id", "malformed_json", "foreign_owner", "request_too_large"},
	"sendDraft":   {"sent", "discarded", "no_recipients", "missing", "not_draft", "invalid_id", "foreign_owner", "request_too_large"},
}

func draftExampleRoute(operation string) (string, string) {
	switch operation {
	case "saveDraft":
		return http.MethodPost, "/api/v1/messages/draft"
	case "updateDraft":
		return http.MethodPut, "/api/v1/messages/draft/{id}"
	default:
		return http.MethodPost, "/api/v1/messages/draft/{id}/send"
	}
}

type draftExampleFixture struct {
	f     *credentialFixture
	seed  models.Message
	since time.Time
}

func newDraftExampleFixture(t *testing.T, operation, name string) draftExampleFixture {
	t.Helper()
	f := newCredentialFixtureWith(t, &config.Config{Environment: "test", APIMaxBodyBytes: 512})
	if err := f.db.Model(&models.Mailbox{}).Where("id = ?", f.mailboxID).Updates(map[string]any{"address": "admin@restmail.test", "local_part": "admin"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&models.Domain{}).Where("name = ?", "example.test").Update("name", "restmail.test").Error; err != nil {
		t.Fatal(err)
	}
	f.address = "admin@restmail.test"
	if name == "discarded" {
		if err := f.db.Create(&models.Pipeline{DomainID: 1, Direction: "outbound", Active: true,
			Filters: json.RawMessage(`[{"name":"sieve","type":"action","enabled":true,"config":{"script":"discard;"}}]`)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	// This identity is deliberately outside the signed-in account's authority.
	other := models.Mailbox{ID: 42, DomainID: 1, LocalPart: "other", Address: "other@restmail.test"}
	if err := f.db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	seed := models.Message{ID: 12, MailboxID: f.mailboxID, Folder: "Drafts", IsDraft: true, IsRead: true,
		Sender: f.address, RecipientsTo: models.JSONB(`["admin@restmail.test"]`), RecipientsCc: models.JSONB(`[]`),
		Subject: "Draft", BodyText: "Not sent yet.", SizeBytes: len("Draft") + len("Not sent yet.")}
	if operation == "sendDraft" {
		seed.RecipientsTo = models.JSONB(`["someone@imap.restmail.test"]`)
	}
	if name == "foreign_owner" {
		seed.MailboxID, seed.Sender = other.ID, other.Address
	}
	if name == "not_draft" {
		seed.IsDraft, seed.Folder = false, "INBOX"
	}
	if name == "no_recipients" {
		seed.RecipientsTo = models.JSONB(`[]`)
	}
	if operation != "saveDraft" && name != "missing" {
		if err := f.db.Create(&seed).Error; err != nil {
			t.Fatal(err)
		}
	}
	return draftExampleFixture{f: f, seed: seed, since: time.Now()}
}

func (fx draftExampleFixture) request(t *testing.T, operation, name string, pair oapifly.ExamplePair) *httptest.ResponseRecorder {
	t.Helper()
	method, path := draftExampleRoute(operation)
	body := string(pair.Request.Body)
	if name == "malformed_json" {
		body = "{"
	}
	if name == "request_too_large" {
		body = strings.Repeat("x", 513)
	}
	return fx.f.do(method, messageCollectionReadURI(path, pair), fx.f.session, body)
}

// Assert persisted identities/content and outgoing side effects before replacing
// only allocated IDs, generated MIME metadata and clocks with illustrative values.
func (fx draftExampleFixture) verify(t *testing.T, operation, name string, pair oapifly.ExamplePair, rr *httptest.ResponseRecorder) {
	t.Helper()
	f := fx.f
	if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
		t.Fatalf("%s/%s = %d %s; want %d %s", operation, name, rr.Code, rr.Body, pair.Response.Status, pair.Response.MediaType)
	}
	var queued int64
	if err := f.db.Model(&models.OutboundQueue{}).Count(&queued).Error; err != nil {
		t.Fatal(err)
	}
	var messages []models.Message
	if err := f.db.Order("id").Find(&messages).Error; err != nil {
		t.Fatal(err)
	}
	if operation == "sendDraft" && name == "sent" {
		if queued != 1 || len(messages) != 1 || messages[0].Folder != "Sent" || messages[0].IsDraft {
			t.Fatalf("send must replace the draft with one Sent row and one recipient: queue=%d messages=%+v", queued, messages)
		}
		var row models.OutboundQueue
		if err := f.db.First(&row).Error; err != nil || row.MessageID == nil || *row.MessageID != messages[0].ID || row.RawMessage != messages[0].RawMessage || row.Recipient != "someone@imap.restmail.test" {
			t.Fatal("outgoing work does not refer to the committed Sent source")
		}
	} else {
		if queued != 0 {
			t.Fatal("saving, updating or refusing a draft queued mail")
		}
		want := 1
		if name == "missing" || name == "discarded" || (operation == "saveDraft" && rr.Code != http.StatusCreated) {
			want = 0
		}
		if len(messages) != want {
			t.Fatalf("stored messages=%d, want %d", len(messages), want)
		}
		if rr.Code >= 400 && len(messages) == 1 {
			got := messages[0]
			if got.ID != fx.seed.ID || got.MailboxID != fx.seed.MailboxID || got.Subject != fx.seed.Subject || got.Sender != fx.seed.Sender || got.IsDraft != fx.seed.IsDraft || string(got.RecipientsTo) != string(fx.seed.RecipientsTo) || got.BodyText != fx.seed.BodyText {
				t.Fatal("refusal changed the saved draft")
			}
		}
	}
	body := rr.Body.Bytes()
	if name == "discarded" {
		assertPublicExampleBody(t, pair.Response.Body, body)
		return
	}
	if rr.Code < 300 {
		var actual, expected map[string]map[string]any
		if err := json.Unmarshal(body, &actual); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(pair.Response.Body, &expected); err != nil {
			t.Fatal(err)
		}
		data, want := actual["data"], expected["data"]
		stored := messages[0]
		if data["id"] != float64(stored.ID) || data["mailbox_id"] != float64(stored.MailboxID) || data["sender"] != stored.Sender || data["subject"] != stored.Subject || data["size_bytes"] != float64(stored.SizeBytes) || data["raw_size"] != float64(len(stored.RawMessage)) || stored.RawSize != len(stored.RawMessage) {
			t.Fatalf("response and stored content/identity/byte counts differ: %s", body)
		}
		for _, field := range []string{"received_at", "created_at", "updated_at"} {
			stamp, err := time.Parse(time.RFC3339Nano, data[field].(string))
			if err != nil || stamp.Before(fx.since.Add(-time.Minute)) || stamp.After(time.Now().Add(time.Second)) {
				t.Fatalf("unexpected generated %s: %v", field, data[field])
			}
		}
		for _, field := range []string{"id", "mailbox_id", "received_at", "created_at", "updated_at"} {
			data[field] = want[field]
		}
		if name == "sent" {
			if data["message_id"] != stored.MsgID || data["thread_id"] != stored.ThreadID || stored.MsgID == "" {
				t.Fatal("sent identities differ from storage")
			}
			for _, field := range []string{"message_id", "thread_id", "raw_size"} {
				data[field] = want[field]
			}
		}
		var err error
		body, err = json.Marshal(actual)
		if err != nil {
			t.Fatal(err)
		}
	}
	assertPublicExampleBody(t, pair.Response.Body, body)
}

func TestDraftLifecycleNamedExamples(t *testing.T) {
	if os.Getenv("DB_HOST") == "" {
		t.Skip("set DB_HOST for private PostgreSQL production-router draft proofs")
	}
	useRealGrants(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for operation, names := range draftExampleNames {
		for _, name := range names {
			t.Run(operation+"/"+name, func(t *testing.T) {
				pair, ok := pairs[operation][name]
				examplepairs.Proven(t, operation, name)
				if !ok || len(pair.Response.Body) == 0 {
					t.Fatal("the named request/response pair is incomplete")
				}
				fx := newDraftExampleFixture(t, operation, name)
				fx.verify(t, operation, name, pair, fx.request(t, operation, name, pair))
			})
		}
	}
}

func TestDraftLifecycleExamplesFitDescription(t *testing.T) {
	for _, w := range describedRouters(t).generator("draft lifecycle").Report() {
		if _, selected := draftExampleNames[w.OperationID]; selected && (w.Kind == oapifly.WarnExampleInvalid || w.Kind == oapifly.WarnExampleOrphaned || (w.Kind == oapifly.WarnExampleMissing && !phaseStatuses[w.Status])) {
			t.Errorf("%s/%s status %s: %s", w.OperationID, w.Example, w.Status, w.Message)
		}
	}
}

func TestDraftLifecycleExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" || os.Getenv("DB_HOST") == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY and DB_HOST for live local draft exchanges")
	}
	useRealGrants(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, names := range draftExampleNames {
		method, path := draftExampleRoute(operation)
		for _, name := range names {
			t.Run(operation+"/"+name, func(t *testing.T) {
				pair, ok := pairs[operation][name]
				if !ok || len(pair.Response.Body) == 0 {
					t.Fatal("the named request/response pair is incomplete")
				}
				fx := newDraftExampleFixture(t, operation, name)
				observed := make(chan *httptest.ResponseRecorder, 2)
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					r.Header.Set("Authorization", fx.f.session)
					rr := httptest.NewRecorder()
					fx.f.router.ServeHTTP(rr, r)
					observed <- rr
					for key, values := range rr.Header() {
						w.Header()[key] = values
					}
					w.WriteHeader(rr.Code)
					_, _ = w.Write(rr.Body.Bytes())
				})
				checkQueueMutationPairWithVertrag(t, binary, bulkQueueExampleScenario{accountReadScenario: accountReadScenario{handler: handler}}, method, path, name, pair, 513)
				select {
				case rr := <-observed:
					fx.verify(t, operation, name, pair, rr)
				default:
					t.Fatal("Vertrag did not replay the published pair")
				}
				select {
				case <-observed:
					t.Fatal("Vertrag replayed more than one request")
				default:
				}
			})
		}
	}
}
