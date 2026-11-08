package handlers

import (
	"fmt"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func TestIMAPModSeqPersistsAcrossGatewayFlagChanges(t *testing.T) {
	database := importProofDB(t)
	account := seedAccountsFixture(t, database).user
	client := newIMAPProofClient(t, imapProofAPI(t, database, account))
	_, uid := client.append("INBOX", "", imapProofMessage)
	var stored models.Message
	if err := database.First(&stored, uid).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ModSeq < 2 {
		t.Fatalf("APPEND mod-sequence = %d, want at least 2", stored.ModSeq)
	}
	selected, _ := client.ok("SELECT INBOX (CONDSTORE)")
	requireLineWith(t, selected, fmt.Sprintf("* OK [HIGHESTMODSEQ %d] Highest", stored.ModSeq))
	before := stored.ModSeq
	client.ok(fmt.Sprintf(`UID STORE %d +FLAGS (\Seen)`, uid))
	if err := database.First(&stored, uid).Error; err != nil {
		t.Fatal(err)
	}
	if !stored.IsRead || stored.ModSeq <= before {
		t.Fatalf("STORE persisted is_read=%t mod-sequence=%d, want read and above %d", stored.IsRead, stored.ModSeq, before)
	}
	selected, _ = client.ok("SELECT INBOX (CONDSTORE)")
	requireLineWith(t, selected, fmt.Sprintf("* OK [HIGHESTMODSEQ %d] Highest", stored.ModSeq))
}
