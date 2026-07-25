package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
)

// TestActivityLog_LikeWildcardEscaped proves an ILIKE filter param containing a
// LIKE metacharacter matches literally rather than as a wildcard (issue #202: LIKE
// wildcard injection). Two actors are seeded — "a_b" and "axb" — and a filter of
// "a_b" must return only the literal "a_b"; without escaping the "_" would also
// match "axb". Representative of the ESCAPE '\' change applied to every LIKE/ILIKE
// filter param.
func TestActivityLog_LikeWildcardEscaped(t *testing.T) {
	gdb := openAuthzTestDB(t)
	if err := gdb.AutoMigrate(&models.ActivityLog{}); err != nil {
		t.Skipf("activity-log LIKE test skipped: migrate failed (%v)", err)
	}
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })

	if err := tx.Exec("DELETE FROM activity_logs").Error; err != nil {
		t.Fatalf("clear activity_logs: %v", err)
	}
	for _, actor := range []string{"a_b", "axb"} {
		if err := tx.Create(&models.ActivityLog{Actor: actor, Action: "login", ResourceType: "mailbox"}).Error; err != nil {
			t.Fatalf("seed activity log %q: %v", actor, err)
		}
	}

	h := NewLogHandler(tx)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/logs/activity?actor=a_b", nil)
	rr := httptest.NewRecorder()
	h.ActivityLog(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}

	var env listEnvelope
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data) != 1 {
		t.Fatalf("actor=a_b matched %d rows, want 1 (the literal a_b); '_' must not act as a wildcard", len(env.Data))
	}
	var row struct {
		Actor string `json:"actor"`
	}
	if err := json.Unmarshal(env.Data[0], &row); err != nil {
		t.Fatalf("decode row: %v", err)
	}
	if row.Actor != "a_b" {
		t.Errorf("matched actor = %q, want a_b", row.Actor)
	}
}
