package apitypes

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestMessageMailboxPreservesAssociationWireBytes(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	for _, mailbox := range []Mailbox{{}, {ID: 7, DomainID: 2, Address: "reader@example.test", Active: true, LastLoginAt: &now, CreatedAt: now, UpdatedAt: now, Domain: &Domain{ID: 2}, QuotaUsage: &QuotaUsage{}}} {
		before, err := json.Marshal(mailbox)
		if err != nil {
			t.Fatal(err)
		}
		after, err := json.Marshal(MessageMailbox(mailbox))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("message association wire changed: before=%s after=%s", before, after)
		}
	}
}
