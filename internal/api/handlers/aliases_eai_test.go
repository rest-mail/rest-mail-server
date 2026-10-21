package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
	"github.com/restmail/restmail/internal/pipeline/filters"
)

func TestAliasCreate_EAIIdentity(t *testing.T) {
	db := openAliasUpdateScratchDB(t)
	if err := db.AutoMigrate(&models.Mailbox{}); err != nil {
		t.Fatal(err)
	}
	domain := models.Domain{Name: "xn--stdte-hra.test", Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	// A full base mailbox makes an accidental plus-address fallback observable.
	base := models.Mailbox{
		DomainID: domain.ID, LocalPart: "münchen", Address: "münchen@" + domain.Name,
		Active: true, QuotaBytes: 1, QuotaUsedBytes: 1,
	}
	if err := db.Create(&base).Error; err != nil {
		t.Fatal(err)
	}
	const destination = "外部Cafe\u0301@例え.JP"
	for _, source := range []string{
		"MU\u0308NCHEN+Shop@STA\u0308DTE.test",
		"münchen+shop@XN--STDTE-HRA.TEST",
		"münchen+shop@städte.test.",
		"Shop <MU\u0308NCHEN+SHOP@städte.test>",
	} {
		t.Run(source, func(t *testing.T) {
			body := mustJSON(map[string]string{"source_address": source, "destination_address": destination})
			rr := httptest.NewRecorder()
			NewAliasHandler(db).Create(rr, httptest.NewRequest(http.MethodPost, "/api/v1/admin/aliases", bytes.NewReader(body)))
			if rr.Code != http.StatusCreated {
				t.Fatalf("create: %d %s", rr.Code, rr.Body)
			}
			var response struct {
				Data models.Alias `json:"data"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			alias := storedAlias(t, db, response.Data.ID)
			if alias.SourceAddress != "münchen+shop@"+domain.Name || alias.DomainID != domain.ID || alias.DestinationAddress != destination {
				t.Fatalf("stored alias: %+v", alias)
			}
			if !sameAlias(alias, response.Data) {
				t.Fatalf("response differs from stored alias: %s", rr.Body)
			}
			factory := filters.NewRecipientCheck(db)
			filter, err := factory(nil)
			if err != nil {
				t.Fatal(err)
			}
			email := &pipeline.EmailJSON{Envelope: pipeline.Envelope{RcptTo: []string{"mu\u0308nchen+shop@städte.test"}}}
			result, err := filter.Execute(context.Background(), email)
			if err != nil || result.Action != pipeline.ActionContinue {
				t.Fatalf("alias did not claim equivalent recipient before quota fallback: %+v, %v", result, err)
			}
			rest := NewRestmailHandler(db, nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{})
			if _, _, ok := rest.resolveRecipientMailbox(email.Envelope.RcptTo[0]); ok {
				t.Fatal("RESTMAIL plus-address fallback bypassed created alias")
			}
			if err := db.Delete(&alias).Error; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAliasCreate_QuotedSource(t *testing.T) {
	db := openAliasUpdateScratchDB(t)
	domain := models.Domain{Name: "xn--stdte-hra.test"}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	body := mustJSON(map[string]string{
		"source_address": `Sales <"Sales @ Team"@städte.test>`, "destination_address": "remote@example.test",
	})
	rr := httptest.NewRecorder()
	NewAliasHandler(db).Create(rr, httptest.NewRequest(http.MethodPost, "/api/v1/admin/aliases", bytes.NewReader(body)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create quoted alias: %d %s", rr.Code, rr.Body)
	}
	var alias models.Alias
	if err := db.First(&alias).Error; err != nil {
		t.Fatal(err)
	}
	if alias.SourceAddress != `"sales @ team"@xn--stdte-hra.test` {
		t.Fatalf("quoted local part corrupted: %q", alias.SourceAddress)
	}
}
