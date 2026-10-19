package api

import (
	"net/http"
	"strconv"
	"testing"
)

// The relay host is dialled from inside the installation, so only the operator
// (domains:write at "*") sets it. A domain admin reads its own domain's
// transport and is refused setting it, even on its own domain (#143).
func TestRelay_DomainAdminReadsButCannotSet(t *testing.T) {
	f := newGrantsFixture(t, fixtureAdmin{name: "dom"})
	owner := f.token(t, "owner")
	if rr := f.grant(t, owner, f.admins["dom"].user.ID, "admin", "domain:a.test"); rr.Code != http.StatusCreated {
		t.Fatalf("grant admin on domain:a.test = %d %s", rr.Code, rr.Body.String())
	}
	token := f.admins["dom"].login(t, f.router).AccessToken
	path := "/api/v1/admin/domains/" + strconv.FormatUint(uint64(f.a.ID), 10) + "/relay"
	body := `{"transport":"relay","host":"10.0.0.5","port":6379,"tls_mode":"implicit"}`

	if rr := doRequest(f.router, http.MethodGet, path, token, ""); rr.Code != http.StatusOK {
		t.Fatalf("domain admin reading its relay = %d %s", rr.Code, rr.Body.String())
	}
	if rr := doRequest(f.router, http.MethodPut, path, token, body); !refusedForAccess(rr) {
		t.Fatalf("domain admin setting a relay = %d %s, want refused", rr.Code, rr.Body.String())
	}
	if rr := doRequest(f.router, http.MethodPut, path, owner, body); rr.Code != http.StatusOK {
		t.Fatalf("operator setting a relay = %d %s", rr.Code, rr.Body.String())
	}
}
