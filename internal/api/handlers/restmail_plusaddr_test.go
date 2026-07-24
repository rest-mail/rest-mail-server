package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// openPlusAddrTestDB connects to the unit-test Postgres and migrates the tables
// the RESTMAIL delivery path touches, skipping (never failing) when no database
// is reachable — matching the repo's DB-in-CI convention.
func openPlusAddrTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg := &config.Config{
		DBHost: envOr("DB_HOST", "localhost"),
		DBPort: envIntOr("DB_PORT", 5432),
		DBName: envOr("DB_NAME", "restmail"),
		DBUser: envOr("DB_USER", "restmail"),
		DBPass: envOr("DB_PASS", "restmail"),
	}
	gdb, err := rmdb.Connect(cfg)
	if err != nil {
		t.Skipf("restmail plus-addressing DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.Domain{}, &models.Mailbox{}, &models.Alias{}, &models.Message{}, &models.QuotaUsage{}, &models.Pipeline{}); err != nil {
		t.Skipf("restmail plus-addressing DB test skipped: migrate failed (%v)", err)
	}
	return gdb
}

// deliverRESTMAIL POSTs a message to the RESTMAIL Deliver handler and returns the
// delivered + failed recipient lists.
func deliverRESTMAIL(t *testing.T, h *RestmailHandler, from, rawMessage string, to ...string) (delivered, failed []string) {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{
		"from":        from,
		"to":          to,
		"subject":     "Hi",
		"body_text":   "hello body",
		"raw_message": rawMessage,
	})
	req := httptest.NewRequest(http.MethodPost, "/restmail/messages", bytes.NewReader(body))
	req.RemoteAddr = "203.0.113.9:5000"
	rr := httptest.NewRecorder()
	h.Deliver(rr, req)

	var resp struct {
		Data struct {
			Delivered []string `json:"delivered"`
			Failed    []string `json:"failed"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode deliver response %q: %v", rr.Body.String(), err)
	}
	return resp.Data.Delivered, resp.Data.Failed
}

// TestRESTMAILDeliver_Subaddressing proves RFC 5233 plus-addressing on the
// RESTMAIL inbound delivery path: an exact address delivers to its mailbox, a
// "+detail" recipient falls back to the base mailbox and records a Delivered-To
// header preserving the tag, and a "+detail" recipient claimed by an explicit
// alias is NOT rerouted to the base mailbox (alias wins over the strip).
func TestRESTMAILDeliver_Subaddressing(t *testing.T) {
	db := openPlusAddrTestDB(t)

	suffix := time.Now().UnixNano()
	domainName := fmt.Sprintf("plusaddr-%d.test", suffix)
	baseAddr := "user@" + domainName

	dom := models.Domain{Name: domainName, Active: true}
	if err := db.Create(&dom).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}
	base := models.Mailbox{DomainID: dom.ID, LocalPart: "user", Address: baseAddr, Password: "x", Active: true, QuotaBytes: 1 << 30}
	if err := db.Create(&base).Error; err != nil {
		t.Fatalf("create base mailbox: %v", err)
	}
	// A base mailbox "claimed@domain" exists, AND an alias claims the full
	// "+detail" address claimed+promo@domain. Absent the alias, a "+detail"
	// delivery would strip to this base mailbox — so the alias-wins case is only
	// meaningful because this mailbox is here to (not) receive it.
	claimedAddr := "claimed@" + domainName
	claimedBox := models.Mailbox{DomainID: dom.ID, LocalPart: "claimed", Address: claimedAddr, Password: "x", Active: true, QuotaBytes: 1 << 30}
	if err := db.Create(&claimedBox).Error; err != nil {
		t.Fatalf("create claimed mailbox: %v", err)
	}
	alias := models.Alias{DomainID: dom.ID, SourceAddress: "claimed+promo@" + domainName, DestinationAddress: baseAddr, Active: true}
	if err := db.Create(&alias).Error; err != nil {
		t.Fatalf("create alias: %v", err)
	}
	t.Cleanup(func() {
		db.Where("mailbox_id IN ?", []uint{base.ID, claimedBox.ID}).Delete(&models.Message{})
		db.Where("id = ?", alias.ID).Delete(&models.Alias{})
		db.Where("id IN ?", []uint{base.ID, claimedBox.ID}).Delete(&models.Mailbox{})
		db.Where("id = ?", dom.ID).Delete(&models.Domain{})
	})

	h := NewRestmailHandler(db, nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{})

	raw := func(rcpt string) string {
		return "From: sender@remote.test\r\nTo: " + rcpt + "\r\nSubject: Hi\r\n\r\nhello body\r\n"
	}

	// 1. Exact address delivers to the mailbox.
	t.Run("exact match", func(t *testing.T) {
		delivered, failed := deliverRESTMAIL(t, h, "sender@remote.test", raw(baseAddr), baseAddr)
		if len(delivered) != 1 || delivered[0] != baseAddr {
			t.Fatalf("delivered = %v, failed = %v; want [%s]", delivered, failed, baseAddr)
		}
	})

	// 2. "+detail" falls back to the base mailbox and records Delivered-To.
	t.Run("plus-detail fallback + Delivered-To", func(t *testing.T) {
		tagged := "user+shop@" + domainName
		delivered, failed := deliverRESTMAIL(t, h, "sender@remote.test", raw(tagged), tagged)
		if len(delivered) != 1 || delivered[0] != tagged {
			t.Fatalf("delivered = %v, failed = %v; want [%s]", delivered, failed, tagged)
		}
		// The stored message must land in the BASE mailbox with a Delivered-To
		// header recording the original "+detail" recipient.
		var msg models.Message
		if err := db.Where("mailbox_id = ? AND subject = ?", base.ID, "Hi").
			Order("id DESC").First(&msg).Error; err != nil {
			t.Fatalf("stored message for base mailbox not found: %v", err)
		}
		if !strings.Contains(msg.RawMessage, "Delivered-To: "+tagged) {
			t.Errorf("stored raw missing Delivered-To for %s:\n%s", tagged, msg.RawMessage)
		}
	})

	// 3. An explicit alias for the full "+detail" address wins: no strip to base.
	t.Run("alias wins over strip", func(t *testing.T) {
		claimed := "claimed+promo@" + domainName
		delivered, failed := deliverRESTMAIL(t, h, "sender@remote.test", raw(claimed), claimed)
		if len(delivered) != 0 {
			t.Fatalf("delivered = %v, want none (alias claims the address, no base strip)", delivered)
		}
		if len(failed) != 1 || failed[0] != claimed {
			t.Fatalf("failed = %v, want [%s]", failed, claimed)
		}
	})
}
