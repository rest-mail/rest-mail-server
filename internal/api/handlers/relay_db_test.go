package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/db/models"
)

const relayTestMasterKey = "relay-handler-master-key"

// relayTestServer serves the relay routes over a rolled-back transaction with
// one seeded domain. Needs the unit-test Postgres; skips when none is reachable.
func relayTestServer(t *testing.T, masterKey string) (http.Handler, *RelayHandler, uint) {
	t.Helper()
	gdb, err := connectTestDB()
	if err != nil {
		t.Skipf("relay settings test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.Domain{}, &models.DomainRelay{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	dom := models.Domain{Name: fmt.Sprintf("relay-%d.test", time.Now().UnixNano())}
	if err := tx.Create(&dom).Error; err != nil {
		t.Fatalf("seed the domain: %v", err)
	}
	h := NewRelayHandler(tx, masterKey)
	r := chi.NewRouter()
	r.Get("/domains/{id}/relay", h.Get)
	r.Put("/domains/{id}/relay", h.Set)
	return r, h, dom.ID
}

func relayCall(t *testing.T, srv http.Handler, method string, domainID uint, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, fmt.Sprintf("/domains/%d/relay", domainID), strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestRelaySettingsRoundTrip(t *testing.T) {
	srv, h, id := relayTestServer(t, relayTestMasterKey)
	const secret = "s3cret-relay-password"

	code, body := relayCall(t, srv, http.MethodGet, id, "")
	if code != http.StatusOK || !strings.Contains(body, `"transport":"direct"`) || !strings.Contains(body, `"password_set":false`) {
		t.Fatalf("unset relay: %d %s; want direct", code, body)
	}

	code, body = relayCall(t, srv, http.MethodPut, id, `{"transport":"relay","host":"smtp.example.net","port":587,"tls_mode":"starttls","username":"apikey","password":"`+secret+`"}`)
	if code != http.StatusOK {
		t.Fatalf("set relay: %d %s", code, body)
	}
	if strings.Contains(body, secret) || !strings.Contains(body, `"password_set":true`) {
		t.Fatalf("set response leaks the password or hides that it is set: %s", body)
	}

	var stored models.DomainRelay
	if err := h.db.Where("domain_id = ?", id).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored.PasswordEnc, secret) || !strings.HasPrefix(stored.PasswordEnc, models.RelayPasswordCipherPrefix) {
		t.Fatal("password not encrypted at rest")
	}
	if got, err := models.LoadRelayPassword(stored.PasswordEnc, relayTestMasterKey); err != nil || got != secret {
		t.Fatalf("stored password does not decrypt: %v", err)
	}

	code, body = relayCall(t, srv, http.MethodGet, id, "")
	if code != http.StatusOK || strings.Contains(body, secret) || strings.Contains(body, stored.PasswordEnc) {
		t.Fatalf("read leaks the password: %s", body)
	}
	var got struct {
		Data relayView `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	want := relayView{DomainID: id, Transport: "relay", Host: "smtp.example.net", Port: 587, TLSMode: "starttls", Username: "apikey", PasswordSet: true}
	if got.Data != want {
		t.Fatalf("read %+v, want %+v", got.Data, want)
	}

	// Leaving the password out keeps it; the ciphertext is unchanged.
	code, body = relayCall(t, srv, http.MethodPut, id, `{"transport":"relay","host":"smtp2.example.net","port":465,"tls_mode":"implicit","username":"apikey"}`)
	if code != http.StatusOK || !strings.Contains(body, `"password_set":true`) {
		t.Fatalf("update without password: %d %s", code, body)
	}
	var kept models.DomainRelay
	h.db.Where("domain_id = ?", id).First(&kept)
	if kept.PasswordEnc != stored.PasswordEnc {
		t.Fatal("password changed when it was left out")
	}

	// Back to direct drops the relay and its credentials.
	code, body = relayCall(t, srv, http.MethodPut, id, `{"transport":"direct"}`)
	if code != http.StatusOK || !strings.Contains(body, `"password_set":false`) {
		t.Fatalf("set direct: %d %s", code, body)
	}
	var cleared models.DomainRelay
	h.db.Where("domain_id = ?", id).First(&cleared)
	if cleared.PasswordEnc != "" || cleared.Host != "" || cleared.Username != "" {
		t.Fatalf("direct kept relay settings: %+v", cleared)
	}
}

// The contract generator's relay enum probe needs the companions from the
// schema's if/then branch. It must not require credentials for a relay.
func TestRelaySettingsContractTransportProbe(t *testing.T) {
	srv, _, id := relayTestServer(t, relayTestMasterKey)
	code, body := relayCall(t, srv, http.MethodPut, id,
		`{"transport":"relay","host":"smtp.example.net","port":587,"tls_mode":"starttls"}`)
	if code != http.StatusOK || !strings.Contains(body, `"password_set":false`) {
		t.Fatalf("relay enum with companions: %d %s; want 200 without credentials", code, body)
	}
	code, body = relayCall(t, srv, http.MethodPut, id, `{"transport":"relay"}`)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("relay enum without companions: %d %s; want 422", code, body)
	}
}

func TestRelaySettingsValidation(t *testing.T) {
	srv, _, id := relayTestServer(t, relayTestMasterKey)
	cases := map[string]struct {
		body  string
		field string
	}{
		"unknown transport": {`{"transport":"carrier-pigeon"}`, "transport"},
		"scheme in host":    {`{"transport":"relay","host":"smtp://smtp.example.net","port":587,"tls_mode":"starttls"}`, "host"},
		"path in host":      {`{"transport":"relay","host":"smtp.example.net/x","port":587,"tls_mode":"starttls"}`, "host"},
		"port in host":      {`{"transport":"relay","host":"smtp.example.net:587","port":587,"tls_mode":"starttls"}`, "host"},
		"empty host":        {`{"transport":"relay","port":587,"tls_mode":"starttls"}`, "host"},
		"port zero":         {`{"transport":"relay","host":"smtp.example.net","port":0,"tls_mode":"starttls"}`, "port"},
		"port too high":     {`{"transport":"relay","host":"smtp.example.net","port":65536,"tls_mode":"starttls"}`, "port"},
		"plaintext":         {`{"transport":"relay","host":"smtp.example.net","port":25,"tls_mode":"none"}`, "tls_mode"},
		"user no password":  {`{"transport":"relay","host":"smtp.example.net","port":587,"tls_mode":"starttls","username":"u"}`, "password"},
		"password no user":  {`{"transport":"relay","host":"smtp.example.net","port":587,"tls_mode":"starttls","password":"p"}`, "password"},
		"CRLF in username":  {`{"transport":"relay","host":"smtp.example.net","port":587,"tls_mode":"starttls","username":"u\r\nMAIL","password":"p"}`, "username"},
		"relay no port":     {`{"transport":"relay","host":"smtp.example.net","tls_mode":"starttls"}`, "port"},
		"relay no tls mode": {`{"transport":"relay","host":"smtp.example.net","port":587}`, "tls_mode"},
		// Direct keeps none of a relay's fields, but each is still held to
		// its schema (#388).
		"direct port zero":     {`{"transport":"direct","port":0}`, "port"},
		"direct port too high": {`{"transport":"direct","port":65536}`, "port"},
		"direct tls mode":      {`{"transport":"direct","tls_mode":"none"}`, "tls_mode"},
		"direct long username": {`{"transport":"direct","username":"` + strings.Repeat("u", 256) + `"}`, "username"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			code, body := relayCall(t, srv, http.MethodPut, id, tc.body)
			if code != http.StatusUnprocessableEntity && code != http.StatusBadRequest {
				t.Fatalf("got %d %s; want a validation refusal", code, body)
			}
			if !strings.Contains(body, `"`+tc.field+`"`) {
				t.Fatalf("refusal does not name %s: %s", tc.field, body)
			}
		})
	}

	if code, _ := relayCall(t, srv, http.MethodPut, 999999999, `{"transport":"direct"}`); code != http.StatusNotFound {
		t.Fatalf("unknown domain: got %d, want 404", code)
	}
}

// TestRelaySettingsRefusesPasswordWithoutMasterKey: a relay credential is never
// stored in the clear, so without a MASTER_KEY it is refused.
func TestRelaySettingsRefusesPasswordWithoutMasterKey(t *testing.T) {
	srv, h, id := relayTestServer(t, "")
	code, body := relayCall(t, srv, http.MethodPut, id, `{"transport":"relay","host":"smtp.example.net","port":587,"tls_mode":"starttls","username":"u","password":"p"}`)
	if code != http.StatusConflict {
		t.Fatalf("got %d %s; want 409", code, body)
	}
	var n int64
	h.db.Model(&models.DomainRelay{}).Where("domain_id = ?", id).Count(&n)
	if n != 0 {
		t.Fatal("a relay was stored without encryption")
	}
}
