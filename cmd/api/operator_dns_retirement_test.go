package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	dkim "github.com/rest-mail/go-dkim/pkg/dkim"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/dkimrotate"
)

func TestDNSRecords_RevokeThenRemovePreviousSelector(t *testing.T) {
	database := scratchDatabase(t)
	if err := database.AutoMigrate(&models.Domain{}, &models.MTASTSPolicy{}, &models.DKIMRotationKey{}); err != nil {
		t.Fatal(err)
	}
	private, public := sharedDKIMKey(t)
	value, err := dkim.RecordValue(public)
	if err != nil {
		t.Fatal(err)
	}
	domain := models.Domain{Name: "retirement.test", DKIMSelector: "current", DKIMPrivateKey: private}
	if err := database.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Hour)
	previous := models.DKIMRotationKey{
		DomainID: domain.ID, Slot: models.DKIMRotationPrevious, Selector: "old",
		PrivateKey: private, TTLSeconds: 600, RetireAfter: &deadline,
	}
	if err := database.Create(&previous).Error; err != nil {
		t.Fatal(err)
	}
	observed := value
	worker := dkimrotate.NewWorker(database, "", func(context.Context, string) ([]string, error) {
		return []string{observed}, nil
	}, time.Minute)
	step := func() {
		t.Helper()
		if err := worker.Step(context.Background(), domain.ID); err != nil {
			t.Fatal(err)
		}
	}
	record := func(want string) {
		t.Helper()
		status, out, errOut := run("dns-records", "--mail-host", "mail.retirement.test")
		if status != exitOK || errOut != "" {
			t.Fatalf("DNS record list: status %d, stderr %s", status, errOut)
		}
		var response struct {
			Domains []domainRecords `json:"domains"`
		}
		if err := json.Unmarshal([]byte(out), &response); err != nil {
			t.Fatal(err)
		}
		found, current := false, false
		for _, entry := range response.Domains {
			for _, record := range entry.Records {
				if record.Name == "old._domainkey.retirement.test" {
					found = true
					if record.Type != "TXT" || record.Value != want || record.TTL != 600 {
						t.Fatalf("previous DNS record: %+v, want %q", record, want)
					}
				}
				if record.Name == "current._domainkey.retirement.test" && record.Value == value {
					current = true
				}
			}
		}
		if found != (want != "") || !current {
			t.Fatalf("previous selector present = %t, want %q; current preserved = %t: %s", found, want, current, out)
		}
	}
	step()
	record(value)
	deadline = time.Now().Add(-time.Hour)
	if err := database.Model(&previous).Update("retire_after", deadline).Error; err != nil {
		t.Fatal(err)
	}
	step()
	record("v=DKIM1; p=")
	step()
	record("v=DKIM1; p=")
	if err := database.First(&previous, previous.ID).Error; err != nil || previous.RevocationPublishedAt != nil {
		t.Fatalf("revocation was observed before publication: %v", err)
	}
	observed = "v=DKIM1; p="
	step()
	record(observed)
	if err := database.First(&previous, previous.ID).Error; err != nil || previous.RevocationPublishedAt == nil {
		t.Fatalf("published revocation was not recorded: %v", err)
	}
	step()
	record(observed)
	published := time.Now().Add(-600 * time.Second)
	if err := database.Model(&previous).Update("revocation_published_at", published).Error; err != nil {
		t.Fatal(err)
	}
	step()
	record("")
}
