package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// TestDeleteBanAnswersNotFoundForNoBan pins #1768 against PostgreSQL: deleteBan
// removes the ban its id names with a 204, and answers the documented 404 when
// no ban has that id, whether it was just removed or never existed, as unbanIP
// already does for an address.
func TestDeleteBanAnswersNotFoundForNoBan(t *testing.T) {
	if os.Getenv("DB_HOST") == "" {
		t.Skip("PostgreSQL ban deletion proof runs in CI with DB_HOST; no local database is started")
	}
	db := openAuthzTestDB(t)
	if err := db.AutoMigrate(&models.Ban{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })

	ban := models.Ban{IP: "192.0.2.177", Reason: "regression #1768", Protocol: "all", CreatedBy: "admin@example.test"}
	if err := tx.Create(&ban).Error; err != nil {
		t.Fatal(err)
	}
	routes := banRoutes(NewBanHandler(tx))
	remove := func(id uint) *httptest.ResponseRecorder {
		t.Helper()
		rr := httptest.NewRecorder()
		routes.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/api/v1/admin/bans/"+strconv.FormatUint(uint64(id), 10), nil))
		return rr
	}
	const notFound = `{"error":{"code":"not_found","message":"Ban not found"}}` + "\n"

	if rr := remove(ban.ID); rr.Code != http.StatusNoContent || rr.Body.Len() != 0 {
		t.Fatalf("existing ban: got %d %q, want an empty 204", rr.Code, rr.Body.String())
	}
	var left int64
	if err := tx.Model(&models.Ban{}).Where("id = ?", ban.ID).Count(&left).Error; err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("the ban is still there after its 204")
	}
	if rr := remove(ban.ID); rr.Code != http.StatusNotFound || rr.Body.String() != notFound {
		t.Fatalf("removed ban: got %d %q, want 404 %q", rr.Code, rr.Body.String(), notFound)
	}
	if rr := remove(2147483647); rr.Code != http.StatusNotFound || rr.Body.String() != notFound {
		t.Fatalf("never-existing ban: got %d %q, want 404 %q", rr.Code, rr.Body.String(), notFound)
	}
}
