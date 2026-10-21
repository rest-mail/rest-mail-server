package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"testing"
)

// postAliasSource sends raw to the alias create handler, which has no
// database. lookedUp reports whether the request got past validation to the
// source domain's lookup: the nil database panics there, inside gorm's
// (*DB).Where called from Create. A panic anywhere else fails the test, so a
// crash cannot pass for having reached the lookup.
func postAliasSource(t *testing.T, raw []byte) (rr *httptest.ResponseRecorder, lookedUp bool) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/aliases", bytes.NewReader(raw))
	rr = httptest.NewRecorder()
	var panicked interface{}
	inLookup := false
	func() {
		defer func() {
			if panicked = recover(); panicked != nil {
				inLookup = panickedInAliasDomainLookup()
			}
		}()
		NewAliasHandler(nil).Create(rr, req)
	}()
	if panicked != nil && !inLookup {
		t.Fatalf("%s panicked outside the domain lookup: %v", raw, panicked)
	}
	return rr, panicked != nil
}

// panickedInAliasDomainLookup reports whether the panic being recovered was
// raised inside gorm's (*DB).Where, called directly by AliasHandler.Create. It
// must be called from the deferred function that recovers, while the panicking
// frames are still on the stack.
func panickedInAliasDomainLookup() bool {
	pcs := make([]uintptr, 64)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(0, pcs)])
	for {
		frame, more := frames.Next()
		if frame.Function == "gorm.io/gorm.(*DB).Where" {
			caller, _ := frames.Next()
			return caller.Function == "github.com/restmail/restmail/internal/api/handlers.(*AliasHandler).Create"
		}
		if !more {
			return false
		}
	}
}

// assertAliasSourceAnswer holds a create that is answered before the lookup to
// exactly status and body.
func assertAliasSourceAnswer(t *testing.T, raw []byte, status int, want map[string]interface{}) {
	t.Helper()
	rr, lookedUp := postAliasSource(t, raw)
	if lookedUp {
		t.Fatalf("%s reached the domain lookup before it was answered", raw)
	}
	if rr.Code != status {
		t.Fatalf("%s: status %d, want %d; body %s", raw, rr.Code, status, rr.Body)
	}
	var got interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", rr.Body, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s answered\n  %s\nwant\n  %s", raw, rr.Body, mustJSON(want))
	}
}

// aliasSourceValidation is the canonical 422 naming exactly fields.
func aliasSourceValidation(fields map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"error": map[string]interface{}{
			"code":    "validation_failed",
			"message": "Request body failed validation",
			"details": map[string]interface{}{"fields": fields},
		},
	}
}

// A source that is not a single mail address is refused with the canonical 422
// before its domain is looked up or the alias stored (#388): the spec declares
// source_address format: email, and a "@" alone let these through.
func TestAliasCreate_MalformedSourceRefused(t *testing.T) {
	for name, source := range map[string]string{
		"no local part":      "@restmail.test",
		"no domain":          "sales@",
		"two addresses":      "sales@restmail.test, eve@restmail.test",
		"CRLF header inject": "sales@restmail.test\r\nBcc: eve@restmail.test",
		"LF header inject":   "sales@restmail.test\nBcc: eve@restmail.test",
		"CR only":            "sales@restmail.test\rBcc: eve@restmail.test",
		"invalid IDN label":  "sales@bad_domain.test",
		"leading hyphen":     "sales@-bad.test",
	} {
		t.Run(name, func(t *testing.T) {
			assertAliasSourceAnswer(t,
				mustJSON(map[string]string{"source_address": source, "destination_address": "admin@restmail.test"}),
				http.StatusUnprocessableEntity,
				aliasSourceValidation(map[string]interface{}{"source_address": "must be a valid email address"}))
		})
	}
}

// The refusals that came before the source check are unchanged: an empty or
// absent field is only "required" (a malformed source next to a missing
// destination is not reported yet), and a source with no "@" keeps its 422.
func TestAliasCreate_SourceRequiredAndNoAtUnchanged(t *testing.T) {
	required := "required"
	malformed := "must be a valid email address"
	for name, c := range map[string]struct {
		body   map[string]string
		fields map[string]interface{}
	}{
		"source missing (the source_missing example)": {
			map[string]string{"destination_address": "admin@restmail.test"},
			map[string]interface{}{"source_address": required}},
		"source empty": {
			map[string]string{"source_address": "", "destination_address": "admin@restmail.test"},
			map[string]interface{}{"source_address": required}},
		"both missing": {
			map[string]string{},
			map[string]interface{}{"source_address": required, "destination_address": required}},
		"both empty": {
			map[string]string{"source_address": "", "destination_address": ""},
			map[string]interface{}{"source_address": required, "destination_address": required}},
		"malformed source, destination missing": {
			map[string]string{"source_address": "@restmail.test"},
			map[string]interface{}{"destination_address": required}},
		"bareword source": {
			map[string]string{"source_address": "sales", "destination_address": "admin@restmail.test"},
			map[string]interface{}{"source_address": malformed}},
		"only spaces": {
			map[string]string{"source_address": "   ", "destination_address": "admin@restmail.test"},
			map[string]interface{}{"source_address": malformed}},
	} {
		t.Run(name, func(t *testing.T) {
			assertAliasSourceAnswer(t, mustJSON(c.body), http.StatusUnprocessableEntity, aliasSourceValidation(c.fields))
		})
	}
}

// A body that does not decode is still the 400 it was, before any field check.
func TestAliasCreate_UndecodableBodyStill400(t *testing.T) {
	want := map[string]interface{}{
		"error": map[string]interface{}{"code": "bad_request", "message": "Invalid request body"},
	}
	for name, raw := range map[string]string{
		"truncated":         `{"source_address":`,
		"source not string": `{"source_address": 5, "destination_address": "admin@restmail.test"}`,
	} {
		t.Run(name, func(t *testing.T) {
			assertAliasSourceAnswer(t, []byte(raw), http.StatusBadRequest, want)
		})
	}
}

// Well-formed sources, including forms a simple pattern would wrongly refuse
// and any other form the shared address parser accepts, pass the check as
// sent and reach the domain lookup with nothing written yet.
func TestAliasCreate_WellFormedSourceReachesLookup(t *testing.T) {
	for name, source := range map[string]string{
		"ordinary":            "sales@restmail.test",
		"plus tag":            "sales+eu@restmail.test",
		"quoted local part":   `"sales team"@restmail.test`,
		"utf-8 local part":    "jürgen@restmail.test",
		"internationalized":   "sales@münchen.test",
		"subdomain and dots":  "first.last@mail.restmail.test",
		"literal star local":  "*@restmail.test",
		"display name parsed": "Sales <sales@restmail.test>",
	} {
		t.Run(name, func(t *testing.T) {
			raw := mustJSON(map[string]string{"source_address": source, "destination_address": "admin@restmail.test"})
			rr, lookedUp := postAliasSource(t, raw)
			if !lookedUp {
				t.Fatalf("%q was answered %d before the domain lookup: %s", source, rr.Code, rr.Body)
			}
			if rr.Body.Len() != 0 || len(rr.Header()) != 0 {
				t.Fatalf("%q wrote a response before the lookup: %v %s", source, rr.Header(), rr.Body)
			}
		})
	}
}
