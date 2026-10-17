package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

// A domain admin lists and clears its own domain's suppressions, and can't
// list or clear another domain's: not through the other domain's path, and
// not by naming the other's suppression under its own domain (#141).
func TestSuppressions_DomainAdminReachesOnlyItsOwn(t *testing.T) {
	f := newGrantsFixture(t, fixtureAdmin{name: "dom"})
	owner := f.token(t, "owner")
	if rr := f.grant(t, owner, f.admins["dom"].user.ID, "admin", "domain:a.test"); rr.Code != http.StatusCreated {
		t.Fatalf("grant admin on domain:a.test = %d %s", rr.Code, rr.Body.String())
	}
	token := f.admins["dom"].login(t, f.router).AccessToken

	suppressed := func(domainID uint, address string, until time.Time) models.Suppression {
		s := models.Suppression{DomainID: domainID, Address: address, Reason: models.SuppressionHardBounce,
			FailureCategory: models.FailureBadMailbox, LastError: "550 5.1.1 User unknown", ExpiresAt: until}
		if err := f.db.Create(&s).Error; err != nil {
			t.Fatal(err)
		}
		return s
	}
	ownS := suppressed(f.a.ID, "gone@remote.test", time.Now().Add(24*time.Hour))
	suppressed(f.a.ID, "expired@remote.test", time.Now().Add(-time.Minute))
	otherS := suppressed(f.b.ID, "private@remote.test", time.Now().Add(24*time.Hour))

	id := func(v uint) string { return strconv.FormatUint(uint64(v), 10) }
	list := func(domainID uint) []models.Suppression {
		t.Helper()
		rr := doRequest(f.router, "GET", "/api/v1/admin/domains/"+id(domainID)+"/suppressions", token, "")
		if rr.Code != http.StatusOK {
			t.Fatalf("list = %d %s", rr.Code, rr.Body.String())
		}
		var env struct{ Data []models.Suppression }
		if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		return env.Data
	}

	// Its own list has its unexpired suppression only.
	if got := list(f.a.ID); len(got) != 1 || got[0].ID != ownS.ID || got[0].Address != "gone@remote.test" {
		t.Fatalf("a.test's suppressions = %+v, want only %d", got, ownS.ID)
	}

	// The other domain's are out of reach.
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/admin/domains/" + id(f.b.ID) + "/suppressions"},
		{"DELETE", "/api/v1/admin/domains/" + id(f.b.ID) + "/suppressions/" + id(otherS.ID)},
	} {
		if rr := doRequest(f.router, c.method, c.path, token, ""); !refusedForAccess(rr) {
			t.Errorf("%s %s = %d %s, want refused", c.method, c.path, rr.Code, rr.Body.String())
		}
	}
	rr := doRequest(f.router, "DELETE", "/api/v1/admin/domains/"+id(f.a.ID)+"/suppressions/"+id(otherS.ID), token, "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("clearing b.test's suppression under a.test = %d %s, want 404", rr.Code, rr.Body.String())
	}
	var n int64
	f.db.Model(&models.Suppression{}).Where("id = ?", otherS.ID).Count(&n)
	if n != 1 {
		t.Fatal("a.test's admin cleared b.test's suppression")
	}

	// Clearing its own takes it off the list.
	rr = doRequest(f.router, "DELETE", "/api/v1/admin/domains/"+id(f.a.ID)+"/suppressions/"+id(ownS.ID), token, "")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("clear = %d %s", rr.Code, rr.Body.String())
	}
	if got := list(f.a.ID); len(got) != 0 {
		t.Errorf("after clearing, a.test's suppressions = %+v", got)
	}
}
