package handlers

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

// assertAliasRefusal holds a create to the canonical 422 naming exactly fields.
func assertAliasRefusal(t *testing.T, body map[string]string, fields map[string]interface{}) {
	t.Helper()
	rr, reached := createAliasNoDB(t, string(mustJSON(body)))
	if reached {
		t.Fatalf("%s reached the database before it was refused", mustJSON(body))
	}
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("%s: status %d, want 422; body %s", mustJSON(body), rr.Code, rr.Body)
	}
	want := map[string]interface{}{
		"error": map[string]interface{}{
			"code":    "validation_failed",
			"message": "Request body failed validation",
			"details": map[string]interface{}{"fields": fields},
		},
	}
	var got interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", rr.Body, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s answered\n  %s\nwant\n  %s", mustJSON(body), rr.Body, mustJSON(want))
	}
}

// A destination that is not a mail address is refused with the canonical 422
// before the source's domain is looked up or the alias stored (#388): the
// spec declares destination_address format: email.
func TestAliasCreate_MalformedDestinationRefused(t *testing.T) {
	for name, destination := range map[string]string{
		"bareword":        "not-an-email",
		"no local part":   "@restmail.test",
		"no domain":       "admin@",
		"two addresses":   "admin@restmail.test, eve@restmail.test",
		"header injected": "admin@restmail.test\r\nBcc: eve@restmail.test",
		"only spaces":     "   ",
	} {
		t.Run(name, func(t *testing.T) {
			assertAliasRefusal(t,
				map[string]string{"source_address": "sales@restmail.test", "destination_address": destination},
				map[string]interface{}{"destination_address": "must be a valid email address"})
		})
	}
}

// An empty or absent destination is still only "required", and a missing
// source beside a valid destination is still only the source's refusal.
func TestAliasCreate_RequiredFieldsUnchanged(t *testing.T) {
	assertAliasRefusal(t,
		map[string]string{"source_address": "sales@restmail.test", "destination_address": ""},
		map[string]interface{}{"destination_address": "required"})
	assertAliasRefusal(t,
		map[string]string{"source_address": "sales@restmail.test"},
		map[string]interface{}{"destination_address": "required"})
	assertAliasRefusal(t,
		map[string]string{"destination_address": "admin@restmail.test"},
		map[string]interface{}{"source_address": "required"})
}

// Well-formed destinations, including the forms a simple pattern would wrongly
// refuse, pass validation untouched and reach the domain lookup.
func TestAliasCreate_WellFormedDestinationReachesDatabase(t *testing.T) {
	for name, destination := range map[string]string{
		"ordinary":           "admin@restmail.test",
		"plus tag":           "admin+alias@restmail.test",
		"quoted local part":  `"admin team"@restmail.test`,
		"utf-8 local part":   "jürgen@restmail.test",
		"internationalized":  "admin@münchen.test",
		"subdomain and dots": "first.last@mail.restmail.test",
	} {
		t.Run(name, func(t *testing.T) {
			body := map[string]string{"source_address": "sales@restmail.test", "destination_address": destination}
			rr, reached := createAliasNoDB(t, string(mustJSON(body)))
			if !reached {
				t.Fatalf("%q was answered %d before the domain lookup: %s", destination, rr.Code, rr.Body)
			}
		})
	}
}
