package api

import (
	"net/http"
	"slices"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/api/respond"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

func TestGrants_DomainAdminPipelinePagesMatchTheirDomain(t *testing.T) {
	fixture := newGrantsFixture(t, fixtureAdmin{name: "left-admin", twoFactor: true}, fixtureAdmin{name: "right-admin", twoFactor: true})
	ownerToken := fixture.token(t, "owner")
	left := []models.Pipeline{
		{DomainID: fixture.a.ID, Direction: "inbound", Filters: []byte(`[]`)},
		{DomainID: fixture.a.ID, Direction: "outbound", Filters: []byte(`[]`)},
	}
	right := []models.Pipeline{{DomainID: fixture.b.ID, Direction: "inbound", Filters: []byte(`[]`)}}
	for _, pipelines := range [][]models.Pipeline{left, right} {
		if err := fixture.db.Create(&pipelines).Error; err != nil {
			t.Fatal(err)
		}
	}

	checkPage := func(t *testing.T, token, query string, domainID uint, wantIDs []uint, total int64, hasMore bool) {
		t.Helper()
		response := doRequest(fixture.router, http.MethodGet, "/api/v1/admin/pipelines"+query, token, "")
		identityStatus(t, response, http.StatusOK, "")
		var result struct {
			Data       []apitypes.Pipeline `json:"data"`
			Pagination *respond.Pagination `json:"pagination"`
		}
		decodeInto(t, response, &result)
		gotIDs := make([]uint, 0, len(result.Data))
		for _, pipeline := range result.Data {
			if pipeline.DomainID != domainID {
				t.Errorf("pipeline %d belongs to domain %d, want %d", pipeline.ID, pipeline.DomainID, domainID)
			}
			gotIDs = append(gotIDs, pipeline.ID)
		}
		if !slices.Equal(gotIDs, wantIDs) || result.Pagination == nil || result.Pagination.Total != total || result.Pagination.HasMore != hasMore {
			t.Fatalf("pipeline IDs %v, pagination %+v, want IDs %v, total %d, has_more %t", gotIDs, result.Pagination, wantIDs, total, hasMore)
		}
	}

	t.Run("left_domain", func(t *testing.T) {
		identityStatus(t, fixture.grant(t, ownerToken, fixture.admins["left-admin"].user.ID, "admin", grants.Domain(fixture.a.Name)), http.StatusCreated, "")
		token := fixture.token(t, "left-admin")
		checkPage(t, token, "?limit=1", fixture.a.ID, []uint{left[0].ID}, 2, true)
		checkPage(t, token, "?limit=1&offset=1", fixture.a.ID, []uint{left[1].ID}, 2, false)
	})
	t.Run("right_domain", func(t *testing.T) {
		identityStatus(t, fixture.grant(t, ownerToken, fixture.admins["right-admin"].user.ID, "admin", grants.Domain(fixture.b.Name)), http.StatusCreated, "")
		token := fixture.token(t, "right-admin")
		checkPage(t, token, "?limit=1", fixture.b.ID, []uint{right[0].ID}, 1, false)
	})
}
