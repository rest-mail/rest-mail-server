package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/handlers"
	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func fixtureDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := os.Getenv("DB_NAME")
	if !strings.HasPrefix(name, "restmail_contract_fixture_") {
		t.Skip("set DB_NAME to an isolated restmail_contract_fixture_* database")
	}
	port, _ := strconv.Atoi(os.Getenv("DB_PORT"))
	db, err := rmdb.Connect(&config.Config{DBHost: os.Getenv("DB_HOST"), DBPort: port,
		DBName: name, DBUser: os.Getenv("DB_USER"), DBPass: os.Getenv("DB_PASS")})
	if err != nil {
		t.Fatal("connect to isolated fixture database:", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.AutoMigrate(&models.Domain{}, &models.OutboundQueue{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

func retry(t *testing.T, db *gorm.DB, id uint) int {
	t.Helper()
	router := chi.NewRouter()
	router.Post("/queue/{id}/retry", handlers.NewQueueHandler(db).RetryQueueEntry)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/queue/%d/retry", id), nil))
	return response.Code
}

func TestRetryFixtureWaitsForTheActualRetry(t *testing.T) {
	db := fixtureDB(t)
	if err := db.Create(&models.Domain{Name: "restmail.test", Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	// Reproduce the old contract failure: the sent message has already completed.
	live := models.OutboundQueue{Sender: "admin@restmail.test", Recipient: "someone@imap.restmail.test",
		Domain: "imap.restmail.test", Status: "delivered"}
	if err := db.Create(&live).Error; err != nil {
		t.Fatal(err)
	}
	if status := retry(t, db, live.ID); status != http.StatusBadRequest {
		t.Fatalf("retry of the already delivered example: %d, want 400", status)
	}
	id, err := prepareRetryFixture(db, "restmail.test")
	if err != nil {
		t.Fatal(err)
	}
	var due int64
	claimable := func() int64 {
		if err := db.Model(&models.OutboundQueue{}).Where("id = ? AND status IN ? AND next_attempt <= ? AND expires_at > ?",
			id, []string{"pending", "deferred"}, time.Now(), time.Now()).Count(&due).Error; err != nil {
			t.Fatal(err)
		}
		return due
	}
	if claimable() != 0 {
		t.Fatal("background worker can take the fixture before the retry example")
	}
	if status := retry(t, db, id); status != http.StatusOK {
		t.Fatalf("retry of the held fixture: %d, want 200", status)
	}
	if claimable() != 1 {
		t.Fatal("the actual retry did not make the fixture due")
	}
	var preserved models.OutboundQueue
	if err := db.First(&preserved, live.ID).Error; err != nil || preserved.Status != "delivered" {
		t.Fatal("fixture setup changed the existing sent message")
	}
}

func TestRetryFixtureIsFreshOnEveryRun(t *testing.T) {
	db := fixtureDB(t)
	if err := db.Create(&models.Domain{Name: "restmail.test", Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	first, err := prepareRetryFixture(db, "restmail.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.OutboundQueue{}).Where("id = ?", first).Update("status", "delivered").Error; err != nil {
		t.Fatal(err)
	}
	second, err := prepareRetryFixture(db, "restmail.test")
	if err != nil || first == second {
		t.Fatal("a repeat reused its consumed fixture:", err)
	}
	var newest models.OutboundQueue
	if err := db.Where("recipient = ?", retryRecipient).Order("created_at DESC").First(&newest).Error; err != nil || newest.ID != second {
		t.Fatal("listQueue does not reach the new held entry:", err)
	}
}

func TestRetryFixtureRequiresTheDisposableDomain(t *testing.T) {
	if _, err := prepareRetryFixture(nil, "production.example"); err == nil {
		t.Fatal("fixture accepted a different domain")
	}
	db := fixtureDB(t)
	if _, err := prepareRetryFixture(db, "restmail.test"); err == nil {
		t.Fatal("fixture accepted a database without restmail.test")
	}
	var rows int64
	if err := db.Model(&models.OutboundQueue{}).Count(&rows).Error; err != nil || rows != 0 {
		t.Fatal("refused setup changed the queue:", err)
	}
}
