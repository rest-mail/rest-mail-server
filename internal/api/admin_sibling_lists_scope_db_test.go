package api

import (
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/api/respond"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

func TestGrants_DomainAdminSiblingListsStayWithinScope(t *testing.T) {
	f := newGrantsFixture(t, fixtureAdmin{name: "scoped-admin", twoFactor: true})
	owner := f.token(t, "owner")
	identityStatus(t, f.grant(t, owner, f.admins["scoped-admin"].user.ID, "admin", grants.Domain(f.a.Name)), http.StatusCreated, "")
	scoped := f.token(t, "scoped-admin")

	filters := []models.CustomFilter{
		{DomainID: f.a.ID, Name: "left", FilterType: "action", Direction: "inbound", Config: []byte(`{}`)},
		{DomainID: f.b.ID, Name: "right", FilterType: "action", Direction: "inbound", Config: []byte(`{}`)},
	}
	aliases := []models.Alias{
		{DomainID: f.a.ID, SourceAddress: "alias@" + f.a.Name, DestinationAddress: f.ma.Address},
		{DomainID: f.b.ID, SourceAddress: "alias@" + f.b.Name, DestinationAddress: f.mb.Address},
	}
	rules := []models.DomainSenderRule{
		{DomainID: f.a.ID, ListType: "allow", Pattern: "allowed@sender.test"},
		{DomainID: f.a.ID, ListType: "block", Pattern: "blocked@sender.test"},
		{DomainID: f.b.ID, ListType: "allow", Pattern: "other-allowed@sender.test"},
		{DomainID: f.b.ID, ListType: "block", Pattern: "other-blocked@sender.test"},
	}
	for _, rows := range []any{&filters, &aliases, &rules} {
		if err := f.db.Create(rows).Error; err != nil {
			t.Fatal(err)
		}
	}

	checkList := func(t *testing.T, token, path string, paginated bool, want ...uint) {
		t.Helper()
		rr := doRequest(f.router, http.MethodGet, path, token, "")
		identityStatus(t, rr, http.StatusOK, "")
		var result struct {
			Data []struct {
				ID uint `json:"id"`
			} `json:"data"`
			Pagination *respond.Pagination `json:"pagination"`
		}
		decodeInto(t, rr, &result)
		got := make([]uint, 0, len(result.Data))
		for _, row := range result.Data {
			got = append(got, row.ID)
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("%s: IDs %v, want %v", path, got, want)
		}
		if paginated && (result.Pagination == nil || result.Pagination.Total != int64(len(want))) {
			t.Fatalf("%s: pagination %+v, want total %d", path, result.Pagination, len(want))
		}
	}
	for _, list := range []struct {
		name, path  string
		left, right uint
	}{
		{"custom_filters", "/api/v1/admin/custom-filters", filters[0].ID, filters[1].ID},
		{"aliases", "/api/v1/admin/aliases", aliases[0].ID, aliases[1].ID},
	} {
		t.Run(list.name, func(t *testing.T) {
			checkList(t, scoped, list.path, true, list.left)
			checkList(t, scoped, fmt.Sprintf("%s?domain_id=%d", list.path, f.b.ID), true)
			checkList(t, owner, list.path, true, list.left, list.right)
		})
	}
	for i, list := range []string{"allowlist", "blocklist"} {
		t.Run(list, func(t *testing.T) {
			own := fmt.Sprintf("/api/v1/admin/domains/%d/%s", f.a.ID, list)
			other := fmt.Sprintf("/api/v1/admin/domains/%d/%s", f.b.ID, list)
			checkList(t, scoped, own, false, rules[i].ID)
			identityStatus(t, doRequest(f.router, http.MethodGet, other, scoped, ""), http.StatusForbidden, "forbidden")
			checkList(t, owner, other, false, rules[i+2].ID)
		})
	}
	for _, path := range []string{"/api/v1/admin/sender-rules", "/api/v1/admin/dkim"} {
		identityStatus(t, doRequest(f.router, http.MethodGet, path, scoped, ""), http.StatusForbidden, "forbidden")
		identityStatus(t, doRequest(f.router, http.MethodGet, path, owner, ""), http.StatusOK, "")
	}
}
