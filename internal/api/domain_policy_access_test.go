package api

import (
	"net/http"
	"strconv"
	"testing"
)

// A domain grant reaches every scoped policy operation on its own domain only. Relay
// writes and report listing deliberately require an installation-wide grant instead.
func TestDomainMailPolicyDomainGrant(t *testing.T) {
	f := newGrantsFixture(t, fixtureAdmin{name: "policy"})
	owner := f.token(t, "owner")
	if rr := f.grant(t, owner, f.admins["policy"].user.ID, "admin", "domain:a.test"); rr.Code != http.StatusCreated {
		t.Fatalf("grant: %d %s", rr.Code, rr.Body)
	}
	token := f.admins["policy"].login(t, f.router).AccessToken
	for _, tc := range []struct{ method, suffix, body string }{
		{"GET", "allowlist", ""}, {"POST", "allowlist", `{}`}, {"DELETE", "allowlist/999999999", ""},
		{"GET", "blocklist", ""}, {"POST", "blocklist", `{}`}, {"DELETE", "blocklist/999999999", ""},
		{"GET", "suppressions", ""}, {"DELETE", "suppressions/999999999", ""},
		{"GET", "mta-sts", ""}, {"PUT", "mta-sts", `{"mode":"invalid"}`}, {"DELETE", "mta-sts", ""},
		{"GET", "relay", ""},
	} {
		t.Run(tc.method+" "+tc.suffix, func(t *testing.T) {
			own := "/api/v1/admin/domains/" + strconv.FormatUint(uint64(f.a.ID), 10) + "/" + tc.suffix
			other := "/api/v1/admin/domains/" + strconv.FormatUint(uint64(f.b.ID), 10) + "/" + tc.suffix
			if rr := doRequest(f.router, tc.method, own, token, tc.body); refusedForAccess(rr) || rr.Code >= 500 {
				t.Errorf("own domain: %d %s", rr.Code, rr.Body)
			}
			if rr := doRequest(f.router, tc.method, other, token, tc.body); rr.Code != http.StatusForbidden {
				t.Errorf("other domain: %d %s, want 403", rr.Code, rr.Body)
			}
		})
	}
	ownID := strconv.FormatUint(uint64(f.a.ID), 10)
	for _, tc := range []struct{ method, path, body string }{
		{"PUT", "/api/v1/admin/domains/" + ownID + "/relay", `{"transport":"direct"}`},
		{"GET", "/api/v1/admin/tls-reports?domain_id=" + ownID, ""},
	} {
		if rr := doRequest(f.router, tc.method, tc.path, token, tc.body); rr.Code != http.StatusForbidden {
			t.Errorf("global-only %s: %d %s, want 403", tc.path, rr.Code, rr.Body)
		}
	}
}
