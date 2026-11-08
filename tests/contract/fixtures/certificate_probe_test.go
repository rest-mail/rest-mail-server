package main

import (
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/tests/contract/fixtureplan"
)

func TestCertificateProbeDomainRequiresTheDisposableTopology(t *testing.T) {
	if err := prepareCertificateProbeDomain(nil, "production.example"); err == nil {
		t.Fatal("certificate fixture accepted a different topology")
	}
}

func TestCertificateProbeDomainIsIsolatedAndDoesNotMoveTheSequence(t *testing.T) {
	database := fixtureDB(t)
	plan, err := fixtureplan.Default()
	if err != nil {
		t.Fatal(err)
	}
	var before, after int64
	if err := database.Raw("SELECT nextval('domains_id_seq')").Scan(&before).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := prepareCertificateProbeDomain(database, "restmail.test"); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Raw("SELECT nextval('domains_id_seq')").Scan(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Fatalf("certificate fixture moved the domain sequence: %d then %d", before, after)
	}
	var stored models.Domain
	if err := database.First(&stored, plan.CertificateDomain.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Name != plan.CertificateDomain.Name || stored.Name == plan.Domain || stored.Active {
		t.Fatalf("certificate fixture = %+v", stored)
	}
}

func TestCertificateProbeDomainDoesNotReplaceAnotherDomain(t *testing.T) {
	database := fixtureDB(t)
	plan, err := fixtureplan.Default()
	if err != nil {
		t.Fatal(err)
	}
	other := models.Domain{ID: plan.CertificateDomain.ID, Name: "other.invalid", ServerType: "traditional"}
	if err := database.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := prepareCertificateProbeDomain(database, "restmail.test"); err == nil {
		t.Fatal("certificate fixture reused another domain")
	}
	if err := database.First(&other, other.ID).Error; err != nil {
		t.Fatal(err)
	}
	if other.Name != "other.invalid" {
		t.Fatalf("certificate fixture changed another domain: %+v", other)
	}
}
