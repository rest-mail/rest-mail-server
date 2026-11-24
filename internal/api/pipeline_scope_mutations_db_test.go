package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

func TestGrants_DomainAdminPipelineMutationsStayWithinScope(t *testing.T) {
	fixture := newGrantsFixture(t,
		fixtureAdmin{name: "left-admin", twoFactor: true},
		fixtureAdmin{name: "right-admin", twoFactor: true},
	)
	ownerToken := fixture.token(t, "owner")
	admins := [2]struct {
		name     string
		domain   models.Domain
		pipeline models.Pipeline
		token    string
	}{
		{name: "left-admin", domain: fixture.a},
		{name: "right-admin", domain: fixture.b},
	}
	for i := range admins {
		admin := &admins[i]
		grant := fixture.grant(t, ownerToken, fixture.admins[admin.name].user.ID, "admin", grants.Domain(admin.domain.Name))
		identityStatus(t, grant, http.StatusCreated, "")
		admin.token = fixture.token(t, admin.name)
		admin.pipeline = models.Pipeline{DomainID: admin.domain.ID, Direction: "inbound", Filters: []byte(`[]`), Active: true}
		if err := fixture.db.Create(&admin.pipeline).Error; err != nil {
			t.Fatal(err)
		}
	}

	const disable = `{"active":false}`
	for i, admin := range admins {
		t.Run(admin.name+"_foreign_pipeline", func(t *testing.T) {
			foreign := admins[1-i].pipeline
			path := fmt.Sprintf("/api/v1/admin/pipelines/%d", foreign.ID)
			identityStatus(t, doRequest(fixture.router, http.MethodPatch, path, admin.token, disable), http.StatusForbidden, "forbidden")
			identityStatus(t, doRequest(fixture.router, http.MethodDelete, path, admin.token, ""), http.StatusForbidden, "forbidden")

			var stored models.Pipeline
			if err := fixture.db.First(&stored, foreign.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !stored.Active {
				t.Fatalf("refused update disabled pipeline %d", foreign.ID)
			}
		})
	}
	for _, admin := range admins {
		t.Run(admin.name+"_own_pipeline", func(t *testing.T) {
			path := fmt.Sprintf("/api/v1/admin/pipelines/%d", admin.pipeline.ID)
			identityStatus(t, doRequest(fixture.router, http.MethodPatch, path, admin.token, disable), http.StatusOK, "")
			var stored models.Pipeline
			if err := fixture.db.First(&stored, admin.pipeline.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Active {
				t.Fatalf("pipeline %d is still active after update", stored.ID)
			}

			identityStatus(t, doRequest(fixture.router, http.MethodDelete, path, admin.token, ""), http.StatusOK, "")
			var count int64
			if err := fixture.db.Model(&models.Pipeline{}).Where("id = ?", stored.ID).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("pipeline %d still exists after delete", stored.ID)
			}
		})
	}
}
