package handlers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/pipeline"
	"github.com/restmail/restmail/internal/pipeline/filters"
)

func TestLegacyAliasEAIClaims(t *testing.T) {
	db := openAliasUpdateScratchDB(t)
	if err := db.AutoMigrate(&models.Mailbox{}); err != nil {
		t.Fatal(err)
	}
	domain := models.Domain{Name: "xn--stdte-hra.test", Active: true}
	other := models.Domain{Name: "other.test", Active: true}
	for _, d := range []*models.Domain{&domain, &other} {
		if err := db.Create(d).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, local := range []string{"münchen", "sales"} {
		base := models.Mailbox{
			DomainID: domain.ID, LocalPart: local, Address: local + "@" + domain.Name,
			Active: true, QuotaBytes: 1, QuotaUsedBytes: 1,
		}
		if err := db.Create(&base).Error; err != nil {
			t.Fatal(err)
		}
		seedReceiveFixture(t, db, base)
	}
	for _, tc := range []struct {
		name, source, recipient string
		wrongDomain, wantClaim  bool
	}{
		{"Unicode domain", "münchen+shop@städte.test", "münchen+shop@" + domain.Name, false, true},
		{"decomposed local", "mu\u0308nchen+shop@" + domain.Name, "münchen+shop@städte.test", false, true},
		{"decomposed and uppercase", "MU\u0308NCHEN+SHOP@STA\u0308DTE.TEST", "münchen+shop@" + domain.Name, false, true},
		{"ASCII local with IDN", "sales+shop@städte.test", "SALES+SHOP@" + domain.Name, false, true},
		{"unrelated local", "munchen+shop@städte.test", "münchen+shop@" + domain.Name, false, false},
		{"different tag", "münchen+other@städte.test", "münchen+shop@" + domain.Name, false, false},
		{"different domain", "münchen+shop@stadte.test", "münchen+shop@" + domain.Name, false, false},
		{"wrong owning domain", "münchen+shop@" + domain.Name, "münchen+shop@" + domain.Name, true, false},
		{"invalid legacy source", "münchen+shop@@städte.test", "münchen+shop@" + domain.Name, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := db.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			t.Cleanup(func() { tx.Rollback() })
			ownerID := domain.ID
			if tc.wrongDomain {
				ownerID = other.ID
			}
			alias := models.Alias{DomainID: ownerID, SourceAddress: tc.source, DestinationAddress: "外部@例え.jp", Active: true}
			if err := tx.Create(&alias).Error; err != nil {
				t.Fatal(err)
			}
			claims, err := repositories.AliasClaimsAddress(tx, tc.recipient, true)
			if err != nil || claims != tc.wantClaim {
				t.Fatalf("alias claim = %v, %v; want %v", claims, err, tc.wantClaim)
			}
			filter, err := filters.NewRecipientCheck(tx)(nil)
			if err != nil {
				t.Fatal(err)
			}
			email := &pipeline.EmailJSON{Envelope: pipeline.Envelope{RcptTo: []string{tc.recipient}}}
			result, err := filter.Execute(context.Background(), email)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantClaim {
				if result.Action != pipeline.ActionContinue {
					t.Fatalf("legacy alias bypassed before quota fallback: %+v", result)
				}
			} else if result.Action != pipeline.ActionReject || !strings.HasPrefix(result.RejectMsg, "452 4.2.2") {
				t.Fatalf("unrelated alias bypassed base quota: %+v", result)
			}
			rest := NewRestmailHandler(tx, nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{})
			mailbox, deliveredTo, resolveErr := rest.resolveRecipientMailbox(t.Context(), tc.recipient)
			ok := resolveErr == nil
			if ok == tc.wantClaim {
				t.Fatalf("plus fallback = %v, mailbox %d; alias claim = %v", ok, mailbox.ID, tc.wantClaim)
			}
			if ok && deliveredTo != tc.recipient {
				t.Fatal("plus fallback lost original recipient")
			}
			if email.Envelope.RcptTo[0] != tc.recipient {
				t.Fatal("alias lookup rewrote envelope")
			}
			alias.CreatedAt = alias.CreatedAt.Truncate(time.Microsecond)
			if stored := storedAlias(t, tx, alias.ID); !sameAlias(stored, alias) {
				t.Fatalf("lookup rewrote legacy alias: %+v", stored)
			}
		})
	}
}

// Claim detection must reserve an equivalent address without selecting between
// distinct legacy destinations, including when an alias is disabled. This pins
// the existing inactive-alias claim policy rather than introducing forwarding.
func TestLegacyAliasEAIClaimPolicies(t *testing.T) {
	db := openAliasUpdateScratchDB(t)
	domain := models.Domain{Name: "xn--stdte-hra.test", Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	aliases := []models.Alias{
		{DomainID: domain.ID, SourceAddress: "cafe\u0301+tag@städte.test", DestinationAddress: "first@remote.test", Active: true},
		{DomainID: domain.ID, SourceAddress: "CAFÉ+TAG@städte.test", DestinationAddress: "second@remote.test", Active: true},
	}
	if err := db.Create(&aliases).Error; err != nil {
		t.Fatal(err)
	}
	address := "café+tag@" + domain.Name
	assertClaim := func(liveOnly, want bool) {
		t.Helper()
		if claims, err := repositories.AliasClaimsAddress(db, address, liveOnly); err != nil || claims != want {
			t.Fatalf("liveOnly=%v: claim=%v, err=%v; want %v", liveOnly, claims, err, want)
		}
	}
	assertClaim(true, true)
	if err := db.Model(&models.Alias{}).Where("domain_id = ?", domain.ID).Update("active", false).Error; err != nil {
		t.Fatal(err)
	}
	assertClaim(true, true)
	if err := db.Model(&domain).Update("active", false).Error; err != nil {
		t.Fatal(err)
	}
	assertClaim(true, false)
	assertClaim(false, true)
}

func TestAliasClaimLookupFailureBlocksPlusFallback(t *testing.T) {
	db := openAliasUpdateScratchDB(t)
	if err := db.AutoMigrate(&models.Mailbox{}); err != nil {
		t.Fatal(err)
	}
	domain := models.Domain{Name: "aliases.test", Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	base := models.Mailbox{DomainID: domain.ID, LocalPart: "user", Address: "user@aliases.test", Active: true}
	if err := db.Create(&base).Error; err != nil {
		t.Fatal(err)
	}
	// This database belongs only to this test. A missing alias table models an
	// actual query failure, which must not be mistaken for an unclaimed address.
	if err := db.Migrator().DropTable(&models.Alias{}); err != nil {
		t.Fatal(err)
	}
	filter, err := filters.NewRecipientCheck(db)(nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := filter.Execute(context.Background(), &pipeline.EmailJSON{
		Envelope: pipeline.Envelope{RcptTo: []string{"user+tag@aliases.test"}},
	})
	if err != nil || result.Action != pipeline.ActionDefer {
		t.Fatalf("alias lookup failure did not defer: %+v, %v", result, err)
	}
	rest := NewRestmailHandler(db, nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{})
	if _, _, resolveErr := rest.resolveRecipientMailbox(t.Context(), "user+tag@aliases.test"); resolveErr == nil {
		t.Fatal("alias query failure allowed plus-address delivery")
	}
}
