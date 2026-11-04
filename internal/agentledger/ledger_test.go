package agentledger

import (
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func TestHash_CoversEveryField(t *testing.T) {
	actor, credential := uint(3), uint(4)
	until := time.Date(2026, 11, 4, 10, 30, 0, 0, time.UTC)
	undoes := int64(1)
	entry := func() *models.AgentLedgerEntry {
		id, cred, at, seq := actor, credential, until, undoes
		return &models.AgentLedgerEntry{MailboxID: 1, Seq: 2, OccurredAt: until.Add(-time.Hour), ActorKind: "account", ActorID: &id,
			ActorName: "chris@example.test", CredentialID: &cred, Capability: "file_newsletters", Level: "auto", Action: "archive",
			Target: models.AuditTarget{"message_id": "9"}, Undo: models.AuditTarget{"folder": "INBOX"}, UndoUntil: &at, Undoes: &seq,
			RequestID: "req-1", PrevHash: GenesisHash}
	}
	base := Hash(entry())
	if Hash(entry()) != base {
		t.Fatal("the same entry hashed differently")
	}
	other := uint(9)
	later := until.Add(time.Second)
	seven := int64(7)
	for name, change := range map[string]func(*models.AgentLedgerEntry){
		"mailbox":     func(e *models.AgentLedgerEntry) { e.MailboxID = 2 },
		"seq":         func(e *models.AgentLedgerEntry) { e.Seq = 3 },
		"occurred at": func(e *models.AgentLedgerEntry) { e.OccurredAt = later },
		"actor kind":  func(e *models.AgentLedgerEntry) { e.ActorKind = "system" },
		"actor id":    func(e *models.AgentLedgerEntry) { e.ActorID = &other },
		"no actor id": func(e *models.AgentLedgerEntry) { e.ActorID = nil },
		"actor name":  func(e *models.AgentLedgerEntry) { e.ActorName = "eve@example.test" },
		"credential":  func(e *models.AgentLedgerEntry) { e.CredentialID = &other },
		"capability":  func(e *models.AgentLedgerEntry) { e.Capability = "send_mail" },
		"level":       func(e *models.AgentLedgerEntry) { e.Level = "approve" },
		"action":      func(e *models.AgentLedgerEntry) { e.Action = "delete" },
		"target":      func(e *models.AgentLedgerEntry) { e.Target = models.AuditTarget{"message_id": "10"} },
		"undo":        func(e *models.AgentLedgerEntry) { e.Undo = models.AuditTarget{"folder": "Trash"} },
		"undo until":  func(e *models.AgentLedgerEntry) { e.UndoUntil = &later },
		"no window":   func(e *models.AgentLedgerEntry) { e.UndoUntil = nil },
		"undoes":      func(e *models.AgentLedgerEntry) { e.Undoes = &seven },
		"request id":  func(e *models.AgentLedgerEntry) { e.RequestID = "req-2" },
		"prev hash":   func(e *models.AgentLedgerEntry) { e.PrevHash = "f" },
	} {
		changed := entry()
		change(changed)
		if Hash(changed) == base {
			t.Errorf("changing the %s left the hash the same", name)
		}
	}
	// Nil and empty pairs are one thing in the hash, as they are in the table.
	empty, none := entry(), entry()
	empty.Target, none.Target = models.AuditTarget{}, nil
	if Hash(empty) != Hash(none) {
		t.Error("an empty target hashes differently from none")
	}
}
