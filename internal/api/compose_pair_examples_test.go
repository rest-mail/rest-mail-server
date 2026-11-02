package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

// The forward and calendar-reply pairs no other test sends: the description's forward,
// the refusals of a body that is not an object or not JSON, a zero id and an absent
// message, each through the production router against PostgreSQL (#638).
var composeExampleNames = map[string][]string{
	"forwardMessage":    {"example", "body_array", "malformed_json", "missing"},
	"respondToCalendar": {"body_array", "invalid_id", "malformed_json", "missing"},
}

func composeExampleRoute(operation string) string {
	if operation == "forwardMessage" {
		return "/api/v1/messages/{id}/forward"
	}
	return "/api/v1/messages/{id}/calendar-reply"
}

func TestComposeNamedExamples(t *testing.T) {
	if os.Getenv("DB_HOST") == "" {
		t.Skip("set DB_HOST for private PostgreSQL production-router forward and calendar-reply proofs")
	}
	useRealGrants(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for operation, names := range composeExampleNames {
		for _, name := range names {
			t.Run(operation+"/"+name, func(t *testing.T) {
				pair, ok := pairs[operation][name]
				examplepairs.Proven(t, operation, name)
				if !ok {
					t.Fatal("the named request/response pair is missing")
				}
				f := newCredentialFixtureWith(t, &config.Config{Environment: "test", APIMaxBodyBytes: 512})
				// A forward charges the sender's quota, which needs its usage row.
				if err := f.db.Create(&models.QuotaUsage{MailboxID: f.mailboxID}).Error; err != nil {
					t.Fatal(err)
				}
				if err := f.db.Model(&models.Mailbox{}).Where("id = ?", f.mailboxID).Updates(map[string]any{"address": "admin@restmail.test", "local_part": "admin"}).Error; err != nil {
					t.Fatal(err)
				}
				if err := f.db.Model(&models.Domain{}).Where("name = ?", "example.test").Update("name", "restmail.test").Error; err != nil {
					t.Fatal(err)
				}
				// The receive grant is scoped by address: the renamed mailbox needs its own.
				var renamed models.Mailbox
				if err := f.db.First(&renamed, f.mailboxID).Error; err != nil {
					t.Fatal(err)
				}
				if err := grants.EnsureReceive(f.db, &renamed); err != nil {
					t.Fatal(err)
				}
				path := composeExampleRoute(operation)
				var seeded uint
				if _, named := pair.Request.Path["id"]; !named {
					// The pair names no message: the live run forwards one of its seed, so
					// this forwards one seeded here.
					source := models.Message{MailboxID: f.mailboxID, Folder: "INBOX", Sender: "organizer@remote.test",
						RecipientsTo: models.JSONB(`["admin@restmail.test"]`), RecipientsCc: models.JSONB(`[]`),
						Subject: "Planning", BodyText: "Shall we meet?", SizeBytes: len("Planning") + len("Shall we meet?")}
					if err := f.db.Create(&source).Error; err != nil {
						t.Fatal(err)
					}
					seeded = source.ID
					path = strings.ReplaceAll(path, "{id}", strconv.FormatUint(uint64(seeded), 10))
				}
				body := string(pair.Request.Body)
				if name == "malformed_json" {
					body = "{"
				}
				rr := f.do(http.MethodPost, messageCollectionReadURI(path, pair), f.session, body)

				var queued, stored int64
				if err := f.db.Model(&models.OutboundQueue{}).Count(&queued).Error; err != nil {
					t.Fatal(err)
				}
				if err := f.db.Model(&models.Message{}).Where("id <> ?", seeded).Count(&stored).Error; err != nil {
					t.Fatal(err)
				}
				if seeded == 0 {
					assertRequirementResponse(t, pair, rr)
					if queued != 0 || stored != 0 {
						t.Fatalf("refusal stored or queued mail: queue=%d messages=%d", queued, stored)
					}
					return
				}
				// The description's forward names only its status: what is sent is the
				// session's own, so the answer is held to what was stored.
				if rr.Code != pair.Response.Status || !strings.HasPrefix(rr.Header().Get("Content-Type"), "application/json") {
					t.Fatalf("%s/%s = %d %q %s; want %d", operation, name, rr.Code, rr.Header().Get("Content-Type"), rr.Body, pair.Response.Status)
				}
				var answer struct {
					Data struct {
						ID      uint   `json:"id"`
						Folder  string `json:"folder"`
						Subject string `json:"subject"`
					} `json:"data"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &answer); err != nil {
					t.Fatal(err)
				}
				var sent models.Message
				if err := f.db.First(&sent, answer.Data.ID).Error; err != nil {
					t.Fatalf("the answered message is not stored: %v", err)
				}
				if answer.Data.Folder != "Sent" || sent.Folder != "Sent" || sent.MailboxID != f.mailboxID || answer.Data.Subject != "Fwd: Planning" || sent.Subject != answer.Data.Subject {
					t.Fatalf("answer and Sent copy differ: %s; stored %+v", rr.Body, sent)
				}
			})
		}
	}
}
