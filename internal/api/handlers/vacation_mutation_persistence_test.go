package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func TestVacationMutationPersistence(t *testing.T) {
	db := openAuthzTestDB(t)
	if err := db.AutoMigrate(&models.VacationConfig{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fx := seedAuthzFixture(t, tx)
	h := NewVacationHandler(tx)
	send := func(method, body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		r := authzRequest(method, "/vacation", fx.ownerMailbox.ID, map[string]string{"id": uintToStr(fx.ownerMailbox.ID)}, body)
		r.Header.Set("Content-Type", "application/json")
		serveVacation(h, rr, r)
		return rr
	}
	const callback = "vacation_mutation_lookup_failure"
	if err := tx.Callback().Query().Before("gorm:query").Register(callback, func(query *gorm.DB) {
		if query.Statement.Table == "vacation_configs" {
			_ = query.AddError(errors.New("private vacation lookup error"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Callback().Query().Remove(callback) })
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		rr := send(method, `{"enabled":false}`)
		if rr.Code != 500 {
			t.Fatalf("%s lookup failure:%d %s", method, rr.Code, rr.Body)
		}
	}
	if err := tx.Callback().Query().Remove(callback); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := tx.Model(&models.VacationConfig{}).Where("mailbox_id = ?", fx.ownerMailbox.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed lookup created %d configurations", count)
	}
	rr := send(http.MethodPut, `{"enabled":false}`)
	if rr.Code != 200 {
		t.Fatalf("disabled creation:%d %s", rr.Code, rr.Body)
	}
	var config models.VacationConfig
	if err := tx.Where("mailbox_id = ?", fx.ownerMailbox.ID).First(&config).Error; err != nil {
		t.Fatal(err)
	}
	if config.Enabled {
		t.Fatal("disabled creation became enabled")
	}
	rr = send(http.MethodPut, `{"enabled":true,"subject":"Away","body":"Reply","start_date":"2030-01-01T00:00:00Z","end_date":"2030-01-02T00:00:00Z"}`)
	if rr.Code != 200 {
		t.Fatalf("enable:%d %s", rr.Code, rr.Body)
	}
	if err := tx.First(&config, config.ID).Error; err != nil {
		t.Fatal(err)
	}
	before := config
	if !before.Enabled || before.StartDate == nil || before.EndDate == nil {
		t.Fatalf("dates not stored:%+v", before)
	}
	rr = send(http.MethodDelete, "")
	if rr.Code != 204 || rr.Body.Len() != 0 {
		t.Fatalf("disable:%d %s", rr.Code, rr.Body)
	}
	if err := tx.First(&config, config.ID).Error; err != nil {
		t.Fatal(err)
	}
	if config.Enabled || config.Subject != before.Subject || config.Body != before.Body || !reflect.DeepEqual(config.StartDate, before.StartDate) || !reflect.DeepEqual(config.EndDate, before.EndDate) {
		t.Fatalf("disable changed reply:%+v", config)
	}
	rr = send(http.MethodPut, `{"enabled":false}`)
	if rr.Code != 200 {
		t.Fatalf("clear:%d %s", rr.Code, rr.Body)
	}
	// Read into a fresh value: NULL scan must not retain earlier pointer values.
	var cleared models.VacationConfig
	if err := tx.First(&cleared, config.ID).Error; err != nil {
		t.Fatal(err)
	}
	if cleared.Enabled || cleared.Subject != "" || cleared.Body != "" || cleared.StartDate != nil || cleared.EndDate != nil {
		t.Fatalf("replacement failed to clear configuration:%+v", cleared)
	}
}
