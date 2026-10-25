package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Stop at an explicit dry-run query callback, without opening a connection.
// A valid body reaches the ban lookup; validation failures never reach it.
func createBanNoDB(t *testing.T, body string) (rr *httptest.ResponseRecorder, reachedDB bool) {
	t.Helper()
	db, err := gorm.Open(postgres.Open("host=127.0.0.1 port=1 user=unused dbname=unused sslmode=disable"), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	stop := &struct{ name string }{"ban lookup reached"}
	if err := db.Callback().Query().Before("gorm:query").Register("ban_protocol_lookup_boundary", func(tx *gorm.DB) {
		if tx.Statement.Table != "bans" {
			t.Fatalf("unexpected query table: %s", tx.Statement.Table)
		}
		reachedDB = true
		panic(stop)
	}); err != nil {
		t.Fatal(err)
	}
	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/bans", strings.NewReader(body))
	func() {
		defer func() {
			if r := recover(); r != nil {
				if r != stop {
					t.Fatalf("%s panicked outside the controlled lookup: %v", body, r)
				}
			}
		}()
		banRoutes(NewBanHandler(db)).ServeHTTP(rr, req)
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

// Binding reports all broken rules together, preserving each field's documented
// message. A malformed JSON body remains a 400.
func TestCreateBanProtocol_KeepsOtherRefusals(t *testing.T) {
	const badRequest = `{"error":{"code":"bad_request","message":"Invalid request body"}}` + "\n"
	refusal := func(fields map[string]string) string {
		return string(mustJSON(map[string]any{"error": map[string]any{"code": "validation_failed", "message": "Request body failed validation", "details": map[string]any{"fields": fields}}})) + "\n"
	}

	for _, tc := range []struct {
		body   string
		status int
		want   string
	}{
		{`{"protocol":"ftp","duration":"forever"}`, http.StatusUnprocessableEntity, refusal(map[string]string{"ip": "IP address is required", "created_by": "required", "protocol": "protocol must be one of smtp, imap, pop3, all", "duration": "invalid duration format (e.g. 24h, 168h)"})},
		{`{"ip":"","protocol":"all"}`, http.StatusUnprocessableEntity, refusal(map[string]string{"ip": "IP address is required", "created_by": "required"})},
		{`{"ip":"192.0.2.10","created_by":"admin@restmail.test","protocol":"ftp","duration":"forever"}`, http.StatusUnprocessableEntity, refusal(map[string]string{"protocol": "protocol must be one of smtp, imap, pop3, all", "duration": "invalid duration format (e.g. 24h, 168h)"})},
		{`{"ip":"192.0.2.10","created_by":"admin@restmail.test","protocol":"smtp","duration":"forever"}`, http.StatusUnprocessableEntity, refusal(map[string]string{"duration": "invalid duration format (e.g. 24h, 168h)"})},
		{`{"ip":"192.0.2.10","created_by":"admin@restmail.test","duration":"7d"}`, http.StatusUnprocessableEntity, refusal(map[string]string{"protocol": "protocol must be one of smtp, imap, pop3, all", "duration": "invalid duration format (e.g. 24h, 168h)"})},
		{`{"ip":"192.0.2.10","protocol":7}`, http.StatusUnprocessableEntity, refusal(map[string]string{"protocol": "must be a string", "created_by": "required"})},
		{`{"ip":"192.0.2.10","protocol":null`, http.StatusBadRequest, badRequest},
	} {
		rr, reachedDB := createBanNoDB(t, tc.body)
		if reachedDB {
			t.Errorf("%s reached the database; want it refused first", tc.body)
			continue
		}
		var got, want any
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
			t.Fatal(err)
		}
		if rr.Code != tc.status || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %d %s, want %d %s", tc.body, rr.Code, rr.Body, tc.status, tc.want)
		}
	}
}
