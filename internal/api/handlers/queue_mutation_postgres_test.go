package handlers

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func queueMutationPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	if os.Getenv("DB_HOST") == "" {
		t.Skip("PostgreSQL queue rollback and row-lock proofs run in CI with DB_HOST; no local database is started")
	}
	db, err := connectTestDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.OutboundQueue{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestQueueMutationPostgresRollback(t *testing.T) {
	db := queueMutationPostgres(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	for _, action := range []string{"retry", "bounce"} {
		for _, fail := range []bool{true, false} {
			t.Run(action+map[bool]string{true: "/failure", false: "/success"}[fail], func(t *testing.T) {
				originalAttempt := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
				item := models.OutboundQueue{Sender: "sender@example.test", Recipient: "recipient@example.test", Domain: "example.test", Status: "deferred", NextAttempt: originalAttempt, LastError: "original"}
				if err := tx.Create(&item).Error; err != nil {
					t.Fatal(err)
				}
				remove := func() {}
				if fail {
					const callback = "queue_mutation_postgres_fault"
					if err := tx.Callback().Update().After("gorm:update").Register(callback, func(q *gorm.DB) {
						if q.Statement.Table == "outbound_queue" && q.RowsAffected == 1 {
							_ = q.AddError(errors.New("private failure after queue SQL update"))
						}
					}); err != nil {
						t.Fatal(err)
					}
					remove = func() { _ = tx.Callback().Update().Remove(callback) }
					defer remove()
				}
				rr := httptest.NewRecorder()
				queueRoutes(NewQueueHandler(tx)).ServeHTTP(rr, httptest.NewRequest("POST", "/api/v1/admin/queue/"+uintToStr(item.ID)+"/"+action, nil))
				remove()
				want := 200
				if fail {
					want = 500
				}
				if rr.Code != want || strings.Contains(rr.Body.String(), "private") {
					t.Fatalf("status=%d want=%d body=%s", rr.Code, want, rr.Body)
				}
				var stored models.OutboundQueue
				if err := tx.First(&stored, item.ID).Error; err != nil {
					t.Fatal(err)
				}
				if fail {
					if stored.Status != "deferred" || stored.LastError != "original" || !stored.NextAttempt.Equal(originalAttempt) {
						t.Fatalf("failed queue mutation persisted: %+v", stored)
					}
				} else if action == "retry" {
					if stored.Status != "pending" || !stored.NextAttempt.Before(originalAttempt) {
						t.Fatalf("retry was not persisted: %+v", stored)
					}
				} else if stored.Status != "bounced" || stored.LastError != "manually bounced by admin" {
					t.Fatalf("bounce was not persisted: %+v", stored)
				}
			})
		}
	}
}

// A second connection holds the row lock while the registered route runs. The
// request deadline must stop the blocked SELECT and leave the row unchanged.
func TestQueueMutationPostgresLockDeadline(t *testing.T) {
	db := queueMutationPostgres(t)
	for _, action := range []string{"retry", "bounce"} {
		t.Run(action, func(t *testing.T) {
			item := models.OutboundQueue{Sender: "lock@example.test", Recipient: "recipient@example.test", Domain: "example.test", Status: "deferred", LastError: "original"}
			if err := db.Create(&item).Error; err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Delete(&models.OutboundQueue{}, item.ID).Error })
			lockCtx, cancelLock := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancelLock()
			lock := db.WithContext(lockCtx).Begin()
			if lock.Error != nil {
				t.Fatal(lock.Error)
			}
			defer lock.Rollback()
			var held models.OutboundQueue
			if err := lock.Clauses(clause.Locking{Strength: "UPDATE"}).First(&held, item.ID).Error; err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			rr := httptest.NewRecorder()
			queueRoutes(NewQueueHandler(db)).ServeHTTP(rr, httptest.NewRequest("POST", "/api/v1/admin/queue/"+uintToStr(item.ID)+"/"+action, nil).WithContext(ctx))
			if rr.Code != 500 || ctx.Err() == nil {
				t.Fatalf("blocked mutation ignored deadline: status=%d err=%v", rr.Code, ctx.Err())
			}
			if err := lock.Rollback().Error; err != nil {
				t.Fatal(err)
			}
			var stored models.OutboundQueue
			if err := db.First(&stored, item.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Status != "deferred" || stored.LastError != "original" {
				t.Fatalf("cancelled mutation changed queue row: %+v", stored)
			}
		})
	}
}
