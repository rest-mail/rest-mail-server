package filters

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
)

// A Sieve vacation reply carries the RFC 3834 headers, a subject encoded for its header, and
// the reason as UTF-8 text, or as the MIME entity it is with :mime (RFC 5230 §4.4, #748).
func TestBuildSieveVacationReply(t *testing.T) {
	text := buildSieveVacationReply("dana@example.test", "friend@elsewhere.test", "Abwesend – zurück am Montag", "Bin weg.\nBis bald.", false, "<m1@elsewhere.test>")
	for _, want := range []string{
		"From: dana@example.test\r\n", "To: friend@elsewhere.test\r\n",
		"Subject: =?utf-8?q?Abwesend_=E2=80=93_zur=C3=BCck_am_Montag?=\r\n",
		"In-Reply-To: <m1@elsewhere.test>\r\n", "Auto-Submitted: auto-replied\r\n",
		"Content-Type: text/plain; charset=utf-8\r\n", "\r\n\r\nBin weg.\r\nBis bald.\r\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the reply lacks %q:\n%s", want, text)
		}
	}

	entity := "Content-Type: text/html; charset=utf-8\r\n\r\n<p>Away</p>"
	withMIME := buildSieveVacationReply("dana@example.test", "friend@elsewhere.test", "Away", entity, true, "")
	if !strings.HasSuffix(withMIME, "MIME-Version: 1.0\r\n"+entity+"\r\n") || strings.Contains(withMIME, "text/plain") {
		t.Errorf("a :mime reason is not the reply's entity:\n%s", withMIME)
	}
}

// The reply a Sieve script's vacation action asked for is queued once per sender and handle
// within its interval, with a null reverse path, and answers instead of the mailbox's vacation
// settings (RFC 5230 §4, #748).
func TestVacation_SieveReply(t *testing.T) {
	cfg := &config.Config{
		DBHost: fcEnvOr("DB_HOST", "localhost"),
		DBPort: fcEnvIntOr("DB_PORT", 5432),
		DBName: fcEnvOr("DB_NAME", "restmail"),
		DBUser: fcEnvOr("DB_USER", "restmail"),
		DBPass: fcEnvOr("DB_PASS", "restmail"),
	}
	gdb, err := rmdb.Connect(cfg)
	if err != nil {
		t.Skipf("vacation DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.Domain{}, &models.Mailbox{}, &models.VacationConfig{},
		&models.VacationResponse{}, &models.OutboundQueue{}); err != nil {
		t.Skipf("vacation DB test skipped: migrate failed (%v)", err)
	}
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })

	domain := models.Domain{Name: fmt.Sprintf("sieve-vacation-%d.test", time.Now().UnixNano()), Active: true}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	box := models.Mailbox{DomainID: domain.ID, LocalPart: "dana", Address: "dana@" + domain.Name, Active: true}
	if err := tx.Create(&box).Error; err != nil {
		t.Fatal(err)
	}
	// The mailbox's vacation settings would answer too; the script's reply goes instead.
	if err := tx.Create(&models.VacationConfig{MailboxID: box.ID, Enabled: true, Subject: "Settings", Body: "From the settings."}).Error; err != nil {
		t.Fatal(err)
	}

	f, err := NewVacation(tx)(nil)
	if err != nil {
		t.Fatal(err)
	}
	arrive := func(handle, seconds string) string {
		t.Helper()
		res, err := f.Execute(context.Background(), &pipeline.EmailJSON{
			Envelope: pipeline.Envelope{MailFrom: "friend@elsewhere.test", RcptTo: []string{box.Address}},
			Headers:  pipeline.Headers{Subject: "Lunch?", MessageID: "<m1@elsewhere.test>"},
			Metadata: map[string]string{
				"vacation_reply_to": "friend@elsewhere.test", "vacation_reply_body": "I am away.",
				"vacation_handle": handle, "vacation_seconds": seconds,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return res.Log.Result
	}
	replies := func() []models.OutboundQueue {
		t.Helper()
		var rows []models.OutboundQueue
		tx.Where("recipient = ?", "friend@elsewhere.test").Order("id").Find(&rows)
		return rows
	}

	if got := arrive("h1", "604800"); got != "queued" {
		t.Fatalf("the first message: %s, want queued", got)
	}
	if got := arrive("h1", "604800"); got == "queued" {
		t.Fatal("a second message within :days was answered again")
	}
	if got := arrive("h2", "604800"); got != "queued" {
		t.Fatalf("another vacation action (handle) was not answered: %s", got)
	}
	if got := arrive("h3", "0"); got != "queued" || arrive("h3", "0") != "queued" {
		t.Fatal(":seconds 0 should answer every message")
	}

	rows := replies()
	if len(rows) != 4 {
		t.Fatalf("%d replies queued, want 4", len(rows))
	}
	first := rows[0]
	if first.Sender != "" || first.SendingDomain != domain.Name || first.Domain != "elsewhere.test" {
		t.Errorf("reply envelope: sender %q, sending as %q, to domain %q", first.Sender, first.SendingDomain, first.Domain)
	}
	for _, want := range []string{"From: " + box.Address + "\r\n", "Subject: Auto: Lunch?\r\n", "Auto-Submitted: auto-replied\r\n", "I am away.\r\n"} {
		if !strings.Contains(first.RawMessage, want) {
			t.Errorf("the reply lacks %q:\n%s", want, first.RawMessage)
		}
	}
	if strings.Contains(first.RawMessage, "From the settings.") {
		t.Error("the settings' reply went too")
	}
}
