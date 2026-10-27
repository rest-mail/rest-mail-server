package handlers

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func TestContactPostgresRollbackAndSuccessfulReload(t *testing.T) {
	if os.Getenv("DB_HOST") == "" {
		t.Skip("PostgreSQL contact transaction proof runs in CI with DB_HOST; no local database is started")
	}
	db := openAuthzTestDB(t)
	if err := db.AutoMigrate(&models.Contact{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	fx := seedAuthzFixture(t, tx)
	for _, index := range []int{2, 3, 4} {
		for _, stage := range []string{"write fault", "reload fault", "success"} {
			if index == 3 && stage == "reload fault" {
				continue
			}
			t.Run(contactFaultRoutes[index].name+"/"+stage, func(t *testing.T) {
				contact := models.Contact{MailboxID: fx.attackerMailbox.ID, Email: fmt.Sprintf("friend-%d-%s@example.test", index, strings.ReplaceAll(stage, " ", "-")), Name: "Original", TrustLevel: "trusted", Source: "manual"}
				if err := tx.Create(&contact).Error; err != nil {
					t.Fatal(err)
				}
				const callback = "contact_transaction_persistence_fault"
				remove := func() {}
				if stage == "write fault" {
					fail := func(q *gorm.DB) {
						if q.Statement.Table == "contacts" {
							_ = q.AddError(errors.New("private contact write failure"))
						}
					}
					if index == 3 {
						if err := tx.Callback().Delete().Before("gorm:delete").Register(callback, fail); err != nil {
							t.Fatal(err)
						}
						remove = func() { _ = tx.Callback().Delete().Remove(callback) }
					} else {
						if err := tx.Callback().Update().Before("gorm:update").Register(callback, fail); err != nil {
							t.Fatal(err)
						}
						remove = func() { _ = tx.Callback().Update().Remove(callback) }
					}
				}
				if stage == "reload fault" {
					reads := 0
					if err := tx.Callback().Query().After("gorm:query").Register(callback, func(q *gorm.DB) {
						if q.Statement.Table == "contacts" {
							reads++
							if reads == 2 {
								_ = q.AddError(errors.New("private contact reload failure"))
							}
						}
					}); err != nil {
						t.Fatal(err)
					}
					remove = func() { _ = tx.Callback().Query().Remove(callback) }
				}
				defer remove()
				route := contactFaultRoutes[index]
				path := fmt.Sprintf("/api/v1/accounts/%d/contacts/%d", fx.attackerMailbox.ID, contact.ID)
				body := route.body
				if index == 4 {
					path = fmt.Sprintf("/api/v1/accounts/%d/contacts/block", fx.attackerMailbox.ID)
					body = fmt.Sprintf(`{"email":%q}`, contact.Email)
				}
				r := authzRequest(route.method, path, fx.attackerMailbox.ID, nil, body)
				claims := middleware.GetClaims(r)
				claims.CredentialID = 11
				claims.CredentialKind = "app_password"
				r.Header.Set("Content-Type", "application/json")
				rr := httptest.NewRecorder()
				contactFaultRouter(tx).ServeHTTP(rr, r)
				remove()
				want := 500
				if stage == "success" {
					want = 200
					if index == 3 {
						want = 204
					}
				}
				if rr.Code != want || strings.Contains(rr.Body.String(), "private") {
					t.Fatalf("status=%d body=%s", rr.Code, rr.Body)
				}
				var stored models.Contact
				err := tx.First(&stored, contact.ID).Error
				if index == 3 && stage == "success" {
					if !errors.Is(err, gorm.ErrRecordNotFound) {
						t.Fatalf("deleted contact remains: %v %+v", err, stored)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				name, trust := "Original", "trusted"
				if stage == "success" {
					if index == 2 {
						name = "Changed"
					} else if index == 4 {
						trust = "blocked"
					}
				}
				if stored.Name != name || stored.TrustLevel != trust || stored.Email != contact.Email || stored.MailboxID != contact.MailboxID {
					t.Fatalf("unexpected persisted contact: %+v", stored)
				}
			})
		}
	}
}
