package handlers

import (
	"fmt"
	"math"
	"net/http"
	"testing"
)

func TestUpdateMailboxAcceptsLegacyNonPositiveQuotas(t *testing.T) {
	// PATCH has always accepted signed quotas. Non-positive values impose no
	// local cap; an owned mailbox still shares its owner's storage budget.
	for _, quota := range []int64{math.MinInt64, -1, 0} {
		t.Run(fmt.Sprint(quota), func(t *testing.T) {
			db := &mailboxDB{}
			rr := serveMailbox(t, db, http.MethodPatch, "/api/v1/admin/mailboxes/9", fmt.Sprintf(`{"quota_bytes":%d}`, quota))
			if rr.Code != http.StatusOK {
				t.Fatalf("status %d (%s), want 200", rr.Code, rr.Body.String())
			}
			if db.commits != 1 || db.rollbacks != 0 {
				t.Errorf("%d commits, %d rollbacks; accepted quota must commit", db.commits, db.rollbacks)
			}
		})
	}
}
