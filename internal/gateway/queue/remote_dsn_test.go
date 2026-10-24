package queue

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

// A message whose sender is outside REST Mail (a forward a Sieve redirect made, say) and
// fails for good is reported to that sender by mail, with a null reverse path, and no more
// often than the per-recipient DSN limit allows (#715, RFC 5321 §6.1).
func TestGenerateBounce_SenderOutsideGetsAQueuedDSN(t *testing.T) {
	gdb := openDSNTestDB(t)
	if err := gdb.AutoMigrate(&models.Domain{}, &models.Mailbox{}, &models.OutboundQueue{}); err != nil {
		t.Skipf("remote DSN test skipped: migrate failed (%v)", err)
	}
	const hostname = "mail.remote-dsn.invalid"
	author := fmt.Sprintf("author-%d@elsewhere.example", time.Now().UnixNano())
	t.Cleanup(func() { gdb.Where("recipient = ?", author).Delete(&models.OutboundQueue{}) })

	w := &Worker{db: gdb, hostname: hostname, bounceMax: 2, bounceWindow: time.Hour}
	failed := models.OutboundQueue{ID: 41, Sender: author, Recipient: "gone@far.example",
		Domain: "far.example", RawMessage: "Subject: forwarded\r\n\r\nbody\r\n"}
	for i := 0; i < 3; i++ {
		w.generateBounce(failed, &SMTPError{Code: 550, Enhanced: "5.1.1", Message: "no such user"})
	}

	var notices []models.OutboundQueue
	if err := gdb.Where("recipient = ?", author).Order("id").Find(&notices).Error; err != nil {
		t.Fatal(err)
	}
	if len(notices) != 2 {
		t.Fatalf("queued %d DSNs, want 2: the limit is 2 per recipient per window", len(notices))
	}
	for _, n := range notices {
		if n.Sender != "" || !n.IsDSN || n.SendingDomain != hostname || n.Domain != "elsewhere.example" || n.Status != "pending" {
			t.Errorf("DSN row = %+v: want a null sender, is_dsn, sent as %s, to elsewhere.example, pending", n, hostname)
		}
		if !strings.Contains(n.RawMessage, "multipart/report") || !strings.Contains(n.RawMessage, "mailer-daemon@"+hostname) {
			t.Errorf("the DSN is not an RFC 3464 report from mailer-daemon@%s:\n%s", hostname, n.RawMessage)
		}
	}
}

// A sender that is not an address gets nothing: there is nowhere to send the notice. The
// worker has no database, so trying would panic.
func TestQueueRemoteDSN_NotAnAddress(t *testing.T) {
	w := &Worker{hostname: "mail.example"}
	w.queueRemoteDSN(models.OutboundQueue{ID: 9, Sender: "not an address"}, &SMTPError{Code: 550})
}
