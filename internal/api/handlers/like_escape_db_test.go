package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
)

// TestDeliveryLog_LikeWildcardEscaped proves an ILIKE filter param containing a
// LIKE metacharacter matches literally rather than as a wildcard (issue #202: LIKE
// wildcard injection). Two senders are seeded — "a_b@x.test" and "axb@x.test" —
// and a filter of "a_b" must return only the literal one; without escaping the
// "_" would also match "axb". Representative of the ESCAPE '\' change applied to
// every LIKE/ILIKE filter param.
func TestDeliveryLog_LikeWildcardEscaped(t *testing.T) {
	gdb := openAuthzTestDB(t)
	if err := gdb.AutoMigrate(&models.OutboundQueue{}); err != nil {
		t.Skipf("delivery-log LIKE test skipped: migrate failed (%v)", err)
	}
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })

	if err := tx.Exec("DELETE FROM outbound_queue").Error; err != nil {
		t.Fatalf("clear outbound_queue: %v", err)
	}
	for _, sender := range []string{"a_b@x.test", "axb@x.test"} {
		if err := tx.Create(&models.OutboundQueue{Sender: sender, Recipient: "r@y.test", Domain: "y.test"}).Error; err != nil {
			t.Fatalf("seed queue entry %q: %v", sender, err)
		}
	}

	h := NewLogHandler(tx)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/logs/delivery?sender=a_b", nil)
	rr := httptest.NewRecorder()
	logRoutes(h).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}

	var env listEnvelope
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data) != 1 {
		t.Fatalf("sender=a_b matched %d rows, want 1 (the literal a_b); '_' must not act as a wildcard", len(env.Data))
	}
	var row struct {
		Sender string `json:"sender"`
	}
	if err := json.Unmarshal(env.Data[0], &row); err != nil {
		t.Fatalf("decode row: %v", err)
	}
	if row.Sender != "a_b@x.test" {
		t.Errorf("matched sender = %q, want a_b@x.test", row.Sender)
	}
}
