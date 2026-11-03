package main

import (
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func TestDMARCReportFixtureIsRepeatSafe(t *testing.T) {
	db := fixtureDB(t)
	if err := db.Create(&models.Domain{Name: "restmail.test", Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := prepareDMARCReport(db, "restmail.test"); err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := db.Model(&models.DMARCReportReceived{}).Where("reporter = ?", "reporter.test").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("fixture count %d: %v", count, err)
	}
	if err := prepareDMARCReport(db, "production.test"); err == nil {
		t.Fatal("fixture accepted a production domain")
	}
}
