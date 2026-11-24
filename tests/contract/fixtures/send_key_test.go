package main

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func TestFuzzSendKeyPreparationClearsOnlyTheRequestedPrincipalsExactKey(t *testing.T) {
	database := fixtureDB(t)
	domain := models.Domain{Name: "restmail.test", Active: true}
	if err := database.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	accounts := []models.Mailbox{
		{DomainID: domain.ID, LocalPart: "admin", Address: "admin@restmail.test", Active: true},
		{DomainID: domain.ID, LocalPart: "other", Address: "other@restmail.test", Active: true},
	}
	if err := database.Create(&accounts).Error; err != nil {
		t.Fatal(err)
	}
	firstKey := fmt.Sprintf("%x", sha256.Sum256([]byte("d")))
	secondKey := fmt.Sprintf("%x", sha256.Sum256([]byte("e")))
	for _, probe := range []struct {
		account uint
		hash    string
	}{{accounts[0].ID, firstKey}, {accounts[0].ID, secondKey}, {accounts[1].ID, firstKey}} {
		receipt := models.SendIdempotency{
			AccountID: probe.account, KeyHash: probe.hash, RequestHash: "request",
			Status: 201, Response: []byte("{}"), ExpiresAt: time.Now().Add(time.Hour), From: "admin@restmail.test",
		}
		if err := database.Create(&receipt).Error; err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		request := strings.NewReader(`{"address":"admin@restmail.test","key":"d"}`)
		if err := prepareSendKeyRequest(database, domain.Name, request); err != nil {
			t.Fatal(err)
		}
	}
	if err := prepareSendKeyProbe(database, domain.Name, mfaAdminAddress, "d"); err == nil {
		t.Fatal("accepted preparation for a missing fixture account")
	}
	var receipts []models.SendIdempotency
	if err := database.Order("account_id").Find(&receipts).Error; err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 2 ||
		receipts[0].AccountID != accounts[0].ID || receipts[0].KeyHash != secondKey ||
		receipts[1].AccountID != accounts[1].ID || receipts[1].KeyHash != firstKey {
		t.Fatalf("want the other key and the other account, got %+v", receipts)
	}
}

func TestFuzzSendKeyPreparationRefusesNonFixtureAccountsAndInvalidKeys(t *testing.T) {
	if err := prepareSendKeyRequest(nil, "restmail.test", strings.NewReader("{")); err == nil {
		t.Fatal("accepted malformed fixture request")
	}
	for _, probe := range []struct{ domain, address, key string }{
		{"other.test", "admin@restmail.test", "d"},
		{"restmail.test", "other@restmail.test", "d"},
		{"restmail.test", "admin@restmail.test", ""},
		{"restmail.test", "admin@restmail.test", strings.Repeat("é", 128)},
	} {
		if err := prepareSendKeyProbe(nil, probe.domain, probe.address, probe.key); err == nil {
			t.Fatalf("accepted invalid fixture preparation: %+v", probe)
		}
	}
}
