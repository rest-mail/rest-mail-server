package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func TestDNSRecords_MTASTSActivePoliciesOnly(t *testing.T) {
	database := scratchDatabase(t)
	if err := database.Migrator().CreateTable(&models.Domain{}, &models.MTASTSPolicy{}, &models.DKIMRotationKey{}); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		name   string
		policy bool
		active bool
	}{
		{name: "bücher.example", policy: true, active: true},
		{name: "inactive.example", policy: true},
		{name: "no-policy.example"},
	} {
		domain := models.Domain{Name: fixture.name}
		if err := database.Create(&domain).Error; err != nil {
			t.Fatal(err)
		}
		if fixture.policy {
			policy := models.MTASTSPolicy{DomainID: domain.ID, Mode: "enforce", MXHosts: "mail.example.com", MaxAge: 86400, Active: fixture.active}
			if err := database.Create(&policy).Error; err != nil {
				t.Fatal(err)
			}
			if err := database.Model(&policy).Update("active", fixture.active).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, command := range []string{"dns", "dns-records"} {
		t.Run(command, func(t *testing.T) {
			status, output, errorOutput := run(command, "--mail-host", "mail.bücher.example")
			if status != exitOK || errorOutput != "" {
				t.Fatalf("status %d, stderr %s", status, errorOutput)
			}
			var result struct {
				Domains []domainRecords `json:"domains"`
			}
			if err := json.Unmarshal([]byte(output), &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Domains) != 3 {
				t.Fatalf("domains = %+v", result.Domains)
			}
			for _, entry := range result.Domains {
				count := 0
				for _, record := range entry.Records {
					if strings.HasPrefix(record.Name, "mta-sts.") || strings.HasPrefix(record.Name, "_mta-sts.") {
						count++
						if record.Type == "CNAME" && (record.Name != "mta-sts.xn--bcher-kva.example" || record.Value != "mail.xn--bcher-kva.example") {
							t.Errorf("policy host record = %+v", record)
						}
					}
				}
				if entry.Domain == "bücher.example" {
					if count != 2 || !strings.Contains(entry.Problem, "HTTPS route and certificate are unverified") || !strings.Contains(entry.Problem, "no DKIM key") {
						t.Errorf("active policy: %+v", entry)
					}
				} else if count != 0 || strings.Contains(entry.Problem, "MTA-STS") {
					t.Errorf("no active policy: %+v", entry)
				}
			}
		})
	}
}
