package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// The SQL-driver fixture proves interaction/error paths locally; this proof
// checks actual PostgreSQL rollback and affected-row behavior in CI.
func TestDraftUpdatePostgresRollbackAndSuccessfulReload(t *testing.T) {
	if os.Getenv("DB_HOST") == "" {
		t.Skip("PostgreSQL draft rollback proof runs in CI with DB_HOST; no local database is started")
	}
	db := openAuthzTestDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	fx := seedAuthzFixture(t, tx)
	for _, stage := range []string{"update", "reload", "success", "headers"} {
		t.Run(stage, func(t *testing.T) {
			draft := models.Message{MailboxID: fx.attackerMailbox.ID, Folder: "Drafts", Sender: fx.attackerMailbox.Address, Subject: "original", IsDraft: true, RecipientsTo: models.JSONB(`[]`), RecipientsCc: models.JSONB(`[]`)}
			if err := tx.Create(&draft).Error; err != nil {
				t.Fatal(err)
			}
			const callback = "draft_update_persistence_fault"
			reads := 0
			remove := func() {}
			if stage == "update" {
				if err := tx.Callback().Update().Before("gorm:update").Register(callback, func(q *gorm.DB) {
					if q.Statement.Table == "messages" {
						_ = q.AddError(errors.New("private draft update failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
				remove = func() { _ = tx.Callback().Update().Remove(callback) }
			}
			if stage == "reload" {
				if err := tx.Callback().Query().After("gorm:query").Register(callback, func(q *gorm.DB) {
					if q.Statement.Table == "messages" {
						reads++
						if reads == 2 {
							_ = q.AddError(errors.New("private draft reload failure"))
						}
					}
				}); err != nil {
					t.Fatal(err)
				}
				remove = func() { _ = tx.Callback().Query().Remove(callback) }
			}
			defer remove()
			body := `{"subject":"changed"}`
			if stage == "headers" {
				body = `{"in_reply_to":"<parent@example.test>","references":"<root@example.test>"}`
			}
			request := authzRequest(http.MethodPut, "/api/v1/messages/draft/"+uintToStr(draft.ID), fx.attackerMailbox.ID, nil, body)
			claims := middleware.GetClaims(request)
			claims.CredentialID = 1
			claims.CredentialKind = "app_password"
			rr := httptest.NewRecorder()
			composeRoutes(NewMessageHandler(tx, nil, nil, "", nil)).ServeHTTP(rr, request)
			remove()
			wantStatus := 500
			wantSubject := "original"
			if stage == "headers" {
				wantStatus = 200
			}
			if stage == "success" {
				wantStatus = 200
				wantSubject = "changed"
			}
			if rr.Code != wantStatus || strings.Contains(rr.Body.String(), "private") {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body)
			}
			var stored models.Message
			if err := tx.First(&stored, draft.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Subject != wantSubject || !stored.IsDraft || stored.MailboxID != draft.MailboxID {
				t.Fatalf("draft state=%+v", stored)
			}
			if stage == "headers" && (stored.InReplyTo != "<parent@example.test>" || stored.References != "<root@example.test>") {
				t.Fatalf("thread headers not persisted: %+v", stored)
			}
			if stage == "success" && !strings.Contains(rr.Body.String(), `"subject":"changed"`) {
				t.Fatal("committed draft was not reloaded into the response")
			}
		})
	}
}
