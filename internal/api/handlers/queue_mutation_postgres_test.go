package handlers

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
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
	if err := migrateModels(db, &models.OutboundQueue{}); err != nil {
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

// A bounce acts on mail still in flight, by ID or in bulk. Any other state is
// final: the single route refuses it and the bulk route passes over it, and the
// row keeps its state and its last error either way.
func TestQueueBounceStatesPostgres(t *testing.T) {
	db := queueMutationPostgres(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	handler := queueRoutes(NewQueueHandler(tx))
	for _, tc := range []struct {
		status string
		bounce bool
	}{
		{"pending", true},
		{"deferred", true},
		{"delivering", true},
		{"delivered", false},
		{"bounced", false},
		{"expired", false},
	} {
		entry := func(t *testing.T) models.OutboundQueue {
			t.Helper()
			item := models.OutboundQueue{Sender: "sender@example.test", Recipient: "recipient@example.test", Domain: "example.test", Status: tc.status, LastError: "original"}
			if err := tx.Create(&item).Error; err != nil {
				t.Fatal(err)
			}
			return item
		}
		stored := func(t *testing.T, id uint) models.OutboundQueue {
			t.Helper()
			var item models.OutboundQueue
			if err := tx.First(&item, id).Error; err != nil {
				t.Fatal(err)
			}
			return item
		}
		t.Run(tc.status+"/single", func(t *testing.T) {
			item := entry(t)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, httptest.NewRequest("POST", "/api/v1/admin/queue/"+uintToStr(item.ID)+"/bounce", nil))
			got := stored(t, item.ID)
			if tc.bounce {
				if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"status":"bounced"`) {
					t.Fatalf("status=%d body=%s", rr.Code, rr.Body)
				}
				if got.Status != "bounced" || got.LastError != "manually bounced by admin" {
					t.Fatalf("bounce was not persisted: %+v", got)
				}
				return
			}
			if rr.Code != 400 || errorCodeOf(rr) != "bad_request" || !strings.Contains(rr.Body.String(), "Only pending, deferred or delivering entries can be bounced") {
				t.Fatalf("status=%d code=%s body=%s", rr.Code, errorCodeOf(rr), rr.Body)
			}
			if got.Status != tc.status || got.LastError != "original" {
				t.Fatalf("refused bounce changed the entry: %+v", got)
			}
		})
		t.Run(tc.status+"/bulk", func(t *testing.T) {
			item := entry(t)
			rr := httptest.NewRecorder()
			request := httptest.NewRequest("POST", "/api/v1/admin/queue/bulk-bounce", strings.NewReader(`{"ids":[`+uintToStr(item.ID)+`]}`))
			request.Header.Set("Content-Type", "application/json")
			handler.ServeHTTP(rr, request)
			got := stored(t, item.ID)
			wantStatus, wantAffected, wantError := tc.status, `"affected":0`, "original"
			if tc.bounce {
				wantStatus, wantAffected, wantError = "bounced", `"affected":1`, "manually bounced by admin (bulk)"
			}
			if rr.Code != 200 || !strings.Contains(rr.Body.String(), wantAffected) {
				t.Fatalf("status=%d body=%s, want %s", rr.Code, rr.Body, wantAffected)
			}
			if got.Status != wantStatus || got.LastError != wantError {
				t.Fatalf("bulk bounce left %+v, want status %s and error %s", got, wantStatus, wantError)
			}
		})
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
