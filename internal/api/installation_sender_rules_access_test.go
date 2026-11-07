package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// The installation's sender rules bind every domain, so only the operator
// (domains:read / domains:write at "*") reads or edits them; a domain admin is
// refused even reading them (#1576). Every change, refused or not, is audited
// like a domain rule change.
func TestInstallationSenderRules_OperatorOnlyAndAudited(t *testing.T) {
	f := newGrantsFixture(t, fixtureAdmin{name: "dom"})
	owner := f.token(t, "owner")
	if rr := f.grant(t, owner, f.admins["dom"].user.ID, "admin", "domain:a.test"); rr.Code != http.StatusCreated {
		t.Fatalf("grant admin on domain:a.test = %d %s", rr.Code, rr.Body.String())
	}
	domainAdmin := f.admins["dom"].login(t, f.router).AccessToken
	const path = "/api/v1/admin/sender-rules"
	body := `{"pattern":"@blocked.example","list_type":"block","reason":"Sends spam"}`

	if rr := doRequest(f.router, http.MethodGet, path, domainAdmin, ""); !refusedForAccess(rr) {
		t.Fatalf("domain admin listing installation rules = %d %s, want refused", rr.Code, rr.Body.String())
	}
	if rr := doRequest(f.router, http.MethodPost, path, domainAdmin, body); !refusedForAccess(rr) {
		t.Fatalf("domain admin adding an installation rule = %d %s, want refused", rr.Code, rr.Body.String())
	}
	var refused int64
	f.db.Model(&models.InstallationSenderRule{}).Count(&refused)
	if refused != 0 {
		t.Fatalf("a refused create stored %d rules", refused)
	}

	rr := doRequest(f.router, http.MethodPost, path, owner, body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("operator adding an installation rule = %d %s", rr.Code, rr.Body.String())
	}
	var created struct {
		Data struct {
			ID       uint   `json:"id"`
			Pattern  string `json:"pattern"`
			ListType string `json:"list_type"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Data.Pattern != "@blocked.example" || created.Data.ListType != "block" || created.Data.ID == 0 {
		t.Fatalf("created %+v", created.Data)
	}
	if rr := doRequest(f.router, http.MethodPost, path, owner, body); rr.Code != http.StatusConflict {
		t.Fatalf("adding the same rule again = %d %s, want 409", rr.Code, rr.Body.String())
	}
	// The refusals openapi-example.json pairs with these requests, exactly.
	for _, tc := range []struct {
		method, path, body string
		status             int
		want               string
	}{
		{http.MethodPost, path, `{"pattern":"@unknown-list.example","list_type":"maybe"}`, http.StatusUnprocessableEntity,
			`{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"list_type":"Must be one of: allow, block"}}}}`},
		{http.MethodPost, path, `{"list_type":"block"}`, http.StatusUnprocessableEntity,
			`{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"pattern":"required"}}}}`},
		{http.MethodDelete, path + "/2147483647", "", http.StatusNotFound,
			`{"error":{"code":"not_found","message":"Sender rule not found"}}`},
	} {
		rr := doRequest(f.router, tc.method, tc.path, owner, tc.body)
		if rr.Code != tc.status || !sameJSON(t, rr.Body.Bytes(), tc.want) {
			t.Errorf("%s %s %s = %d %s, want %d %s", tc.method, tc.path, tc.body, rr.Code, rr.Body.String(), tc.status, tc.want)
		}
	}

	rr = doRequest(f.router, http.MethodGet, path, owner, "")
	var listed struct {
		Data []struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &listed) != nil || len(listed.Data) != 1 || listed.Data[0].ID != created.Data.ID {
		t.Fatalf("operator listing = %d %s", rr.Code, rr.Body.String())
	}

	one := path + "/" + strconv.FormatUint(uint64(created.Data.ID), 10)
	if rr := doRequest(f.router, http.MethodDelete, one, domainAdmin, ""); !refusedForAccess(rr) {
		t.Fatalf("domain admin removing an installation rule = %d %s, want refused", rr.Code, rr.Body.String())
	}
	if rr := doRequest(f.router, http.MethodDelete, one, owner, ""); rr.Code != http.StatusNoContent {
		t.Fatalf("operator removing the rule = %d %s", rr.Code, rr.Body.String())
	}
	if rr := doRequest(f.router, http.MethodDelete, one, owner, ""); rr.Code != http.StatusNotFound {
		t.Fatalf("removing it again = %d %s, want 404", rr.Code, rr.Body.String())
	}

	audited := func(method, route, outcome string) int64 {
		var n int64
		f.db.Model(&models.AuditEntry{}).
			Where("action = ? AND method = ? AND route = ? AND outcome = ?", "admin.change", method, route, outcome).
			Count(&n)
		return n
	}
	for _, want := range []struct {
		method, route, outcome string
		n                      int64
	}{
		{http.MethodPost, path, models.AuditOutcomeDenied, 1},
		{http.MethodPost, path, models.AuditOutcomeSuccess, 1},
		{http.MethodDelete, path + "/{id}", models.AuditOutcomeDenied, 1},
		{http.MethodDelete, path + "/{id}", models.AuditOutcomeSuccess, 1},
	} {
		if got := audited(want.method, want.route, want.outcome); got != want.n {
			t.Errorf("audit %s %s %s: %d entries, want %d", want.method, want.route, want.outcome, got, want.n)
		}
	}
}

func sameJSON(t *testing.T, got []byte, want string) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(a, b)
}
