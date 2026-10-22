package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
)

const aliasUpdatePath = aliasesPath + "/{id}"

func TestAliasUpdate_ExamplePairs(t *testing.T) {
	requests, responses := requestExamples(t, aliasUpdatePath, "updateAlias", "patch")
	got := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		got[status] = names(examples)
	}
	want := map[string][]string{
		"request": {"destination_invalid", "updated"},
		"200":     {"updated"},
		"422":     {"destination_invalid"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("updateAlias's named examples are %v, want %v", got, want)
	}
	for name, destination := range map[string]string{
		"updated":             "admin@restmail.test",
		"destination_invalid": "not-an-email",
	} {
		if want := map[string]interface{}{"destination_address": destination}; !reflect.DeepEqual(requests[name], want) {
			t.Errorf("%s is %s, want %s", name, mustJSON(requests[name]), mustJSON(want))
		}
	}
	updated := responses["200"]["updated"].(map[string]interface{})["data"].(map[string]interface{})
	if updated["destination_address"] != "admin@restmail.test" {
		t.Errorf("updated's 200 has destination_address %v", updated["destination_address"])
	}
}

// Unlike createAlias, updateAlias resolves the stored alias before validating
// its body. Exercise the actual spec requests against an existing alias, as
// the contract run does through createAlias's link.
func TestAliasUpdate_DocumentedExamples(t *testing.T) {
	requests, responses := requestExamples(t, aliasUpdatePath, "updateAlias", "patch")
	db, err := connectTestDB()
	if err != nil {
		t.Skipf("alias update examples require PostgreSQL: %v", err)
	}
	if err := db.AutoMigrate(&models.Domain{}, &models.Alias{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	domain := models.Domain{Name: "aliasupdate.test"}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	before := seedAlias(t, tx, domain, "sales@aliasupdate.test")
	h := NewAliasHandler(tx)

	t.Run("destination_invalid", func(t *testing.T) {
		rr := patchAlias(h, fmt.Sprint(before.ID), string(mustJSON(requests["destination_invalid"])))
		if rr.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status %d, want 422; body %s", rr.Code, rr.Body)
		}
		var got interface{}
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if want := responses["422"]["destination_invalid"]; !reflect.DeepEqual(got, want) {
			t.Errorf("answered %s, want documented body %s", rr.Body, mustJSON(want))
		}
		if after := storedAlias(t, tx, before.ID); !sameAlias(after, before) {
			t.Errorf("refused request changed stored alias: before %+v, after %+v", before, after)
		}
	})

	t.Run("updated", func(t *testing.T) {
		rr := patchAlias(h, fmt.Sprint(before.ID), string(mustJSON(requests["updated"])))
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d, want 200; body %s", rr.Code, rr.Body)
		}
		var got struct {
			Data models.Alias `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		want := before
		want.DestinationAddress = "admin@restmail.test"
		if !sameAlias(got.Data, want) || !sameAlias(storedAlias(t, tx, before.ID), want) {
			t.Errorf("updated request did not return and store the expected alias: %s", rr.Body)
		}
	})
}
