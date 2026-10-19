package handlers

import (
	"net/http"
	"net/http/httptest"
	"runtime/debug"
	"strings"
	"testing"
)

// createBanNoDB posts body to CreateBan on a handler without a database. The
// first thing past validation is the lookup of the IP, which panics on the nil
// database, so reachedDB says the request was accepted as valid. A panic
// anywhere else fails the test.
func createBanNoDB(t *testing.T, body string) (rr *httptest.ResponseRecorder, reachedDB bool) {
	t.Helper()
	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/bans", strings.NewReader(body))
	func() {
		defer func() {
			if r := recover(); r != nil {
				stack := string(debug.Stack())
				if !strings.Contains(stack, "gorm.io/gorm.(*DB).Where") {
					t.Fatalf("%s panicked outside the lookup of the IP: %v\n%s", body, r, stack)
				}
				reachedDB = true
			}
		}()
		NewBanHandler(nil).CreateBan(rr, req)
	}()
	return rr, reachedDB
}

const banProtocolRefusal = `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"protocol":"protocol must be one of smtp, imap, pop3, all"}}}}` + "\n"

// A protocol outside the documented enum is refused with the standard 422 on
// protocol, before the ban is looked up or stored. Nothing is normalized: a
// value that only becomes valid once lowercased or trimmed is refused too.
func TestCreateBanProtocol_RefusesOutsideEnum(t *testing.T) {
	for _, protocol := range []string{
		"ftp", "SMTP", "All", "Imap", " smtp", "smtp ", "smtp\n", "\tall",
		"smtp\u0000", "smtp,imap", "*", "any", "all\u200b",
	} {
		body := `{"ip":"192.0.2.10","reason":"Repeated failed sign-ins","protocol":` +
			string(mustJSON(protocol)) + `,"duration":"1h","created_by":"admin@restmail.test"}`
		rr, reachedDB := createBanNoDB(t, body)
		if reachedDB {
			t.Errorf("protocol %q reached the database; want it refused first", protocol)
			continue
		}
		if rr.Code != http.StatusUnprocessableEntity || rr.Body.String() != banProtocolRefusal {
			t.Errorf("protocol %q: %d %s, want 422 %s", protocol, rr.Code, rr.Body, banProtocolRefusal)
		}
	}
}

// Every documented protocol passes validation and reaches the lookup of the IP.
func TestCreateBanProtocol_EnumReachesLookup(t *testing.T) {
	for name, protocol := range map[string]string{
		"smtp": `,"protocol":"smtp"`, "imap": `,"protocol":"imap"`,
		"pop3": `,"protocol":"pop3"`, "all": `,"protocol":"all"`,
	} {
		for _, duration := range []string{`,"duration":"1h"`, ""} {
			body := `{"ip":"192.0.2.10","reason":"Repeated failed sign-ins","created_by":"admin@restmail.test"` +
				protocol + duration + "}"
			rr, reachedDB := createBanNoDB(t, body)
			if !reachedDB || rr.Body.Len() != 0 {
				t.Errorf("%s %s: reached the lookup %v, answered %d %s; want only the lookup",
					name, body, reachedDB, rr.Code, rr.Body)
			}
		}
	}
}

// The other refusals keep their bodies and their order: a missing IP is
// refused before the protocol is looked at, the protocol before the duration,
// and a bad duration with a valid protocol as before. A body that does not
// decode is still a 400.
func TestCreateBanProtocol_KeepsOtherRefusals(t *testing.T) {
	const (
		ipMissing  = `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"ip":"IP address is required"}}}}` + "\n"
		badDur     = `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"duration":"invalid duration format (e.g. 24h, 168h)"}}}}` + "\n"
		badRequest = `{"error":{"code":"bad_request","message":"Invalid request body"}}` + "\n"
	)
	for _, tc := range []struct {
		body   string
		status int
		want   string
	}{
		{`{"protocol":"ftp","duration":"forever"}`, http.StatusUnprocessableEntity, ipMissing},
		{`{"ip":"","protocol":"all"}`, http.StatusUnprocessableEntity, ipMissing},
		{`{"ip":"192.0.2.10","created_by":"admin@restmail.test","protocol":"ftp","duration":"forever"}`, http.StatusUnprocessableEntity, banProtocolRefusal},
		{`{"ip":"192.0.2.10","created_by":"admin@restmail.test","protocol":"smtp","duration":"forever"}`, http.StatusUnprocessableEntity, badDur},
		{`{"ip":"192.0.2.10","created_by":"admin@restmail.test","duration":"7d"}`, http.StatusUnprocessableEntity, banProtocolRefusal},
		{`{"ip":"192.0.2.10","protocol":7}`, http.StatusBadRequest, badRequest},
		{`{"ip":"192.0.2.10","protocol":null`, http.StatusBadRequest, badRequest},
	} {
		rr, reachedDB := createBanNoDB(t, tc.body)
		if reachedDB {
			t.Errorf("%s reached the database; want it refused first", tc.body)
			continue
		}
		if rr.Code != tc.status || rr.Body.String() != tc.want {
			t.Errorf("%s: %d %s, want %d %s", tc.body, rr.Code, rr.Body, tc.status, tc.want)
		}
	}
}
