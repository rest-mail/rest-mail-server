package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func TestAliasMutationPersistence(t *testing.T) {
	db := openAliasUpdateScratchDB(t)
	domain := models.Domain{Name: "xn--bcher-kva.example", Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	h := NewAliasHandler(db)
	create := func(source string) *httptest.ResponseRecorder {
		body, err := json.Marshal(map[string]string{"source_address": source, "destination_address": "Remote@Destination.TEST"})
		if err != nil {
			t.Fatal(err)
		}
		rr := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/aliases", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		aliasRoutes(h).ServeHTTP(rr, r)
		return rr
	}
	rr := create("Sales <Cafe\u0301@BÜCHER.example>")
	if rr.Code != 201 {
		t.Fatalf("create:%d %s", rr.Code, rr.Body)
	}
	var response struct{ Data Alias }
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	stored := storedAlias(t, db, response.Data.ID)
	if stored.SourceAddress != "café@xn--bcher-kva.example" || stored.DestinationAddress != "Remote@Destination.TEST" || stored.DomainID != domain.ID || !stored.Active || response.Data.Domain != nil {
		t.Fatalf("source/destination/association:%+v %s", stored, rr.Body)
	}
	rr = create("café@xn--bcher-kva.example")
	if rr.Code != 409 {
		t.Fatalf("duplicate:%d %s", rr.Code, rr.Body)
	}
	rr = patchAlias(h, fmt.Sprint(stored.ID), `{"active":false}`)
	if rr.Code != 200 {
		t.Fatalf("disable:%d %s", rr.Code, rr.Body)
	}
	after := storedAlias(t, db, stored.ID)
	if after.Active || after.SourceAddress != stored.SourceAddress || after.DestinationAddress != stored.DestinationAddress || after.DomainID != stored.DomainID {
		t.Fatalf("partial update:%+v", after)
	}
	const callback = "alias_mutation_create_failure"
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(db *gorm.DB) {
		if _, ok := db.Statement.Dest.(*models.Alias); ok {
			_ = db.AddError(errors.New("private alias dependency failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callback) })
	rr = create("another@xn--bcher-kva.example")
	if rr.Code != 500 {
		t.Fatalf("non-unique creation failure:%d %s", rr.Code, rr.Body)
	}
	if aliasCount(t, db) != 1 {
		t.Fatal("failed insertion changed aliases")
	}
}

func TestAliasMutationReloadFailureIsReported(t *testing.T) {
	db := openAliasUpdateScratchDB(t)
	domain := models.Domain{Name: "restmail.test", Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	stored := seedAlias(t, db, domain, "sales@restmail.test")
	const callback = "alias_mutation_reload_failure"
	reads := 0
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(db *gorm.DB) {
		if db.Statement.Table == "aliases" {
			reads++
			if reads == 2 {
				_ = db.AddError(errors.New("private alias reload dependency failure"))
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	rr := patchAlias(NewAliasHandler(db), fmt.Sprint(stored.ID), `{"active":false}`)
	if rr.Code != 500 {
		t.Fatalf("reload failure:%d %s", rr.Code, rr.Body)
	}
	if err := db.Callback().Query().Remove(callback); err != nil {
		t.Fatal(err)
	}
	// The checked reload does not claim to roll back the preceding committed write.
	if storedAlias(t, db, stored.ID).Active {
		t.Fatal("expected the earlier update to have committed")
	}
}
