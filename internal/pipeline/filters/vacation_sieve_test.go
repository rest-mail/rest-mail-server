package filters

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/config"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/internal/testdb"
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
		testdb.Unreachable(t, "vacation DB test skipped: no database reachable (%v)", err)
	}
	testdb.ReuseConnections(t, gdb)
	if err := gdb.AutoMigrate(&models.Domain{}, &models.Mailbox{}, &models.VacationConfig{},
		&models.VacationResponse{}, &models.OutboundQueue{}); err != nil {
		t.Skipf("vacation DB test skipped: migrate failed (%v)", err)
	}
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_vacation_responses_handle ON vacation_responses(mailbox_id, sender, handle)`).Error; err != nil {
		t.Fatal(err)
	}

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

// RFC 5230 §4.2: the arguments a vacation response is tracked by MUST NOT have undergone
// variable expansion (RFC 5229). Without a :handle the server tracks a response by its
// :subject, :from and reason as the script wrote them (go-sieve's Vacation.Tracking), so a
// subject built from a variable is one response whatever it expands to, and is never the
// same response as a literal subject that happens to expand to the same text. The reply
// itself carries the expanded subject. Each message runs the real sieve filter and then the
// real vacation filter that queues the reply.
func TestVacation_SieveTracksTheScriptsWordsNotTheirExpansion(t *testing.T) {
	cfg := &config.Config{
		DBHost: fcEnvOr("DB_HOST", "localhost"),
		DBPort: fcEnvIntOr("DB_PORT", 5432),
		DBName: fcEnvOr("DB_NAME", "restmail"),
		DBUser: fcEnvOr("DB_USER", "restmail"),
		DBPass: fcEnvOr("DB_PASS", "restmail"),
	}
	gdb, err := rmdb.Connect(cfg)
	if err != nil {
		testdb.Unreachable(t, "vacation DB test skipped: no database reachable (%v)", err)
	}
	testdb.ReuseConnections(t, gdb)
	if err := gdb.AutoMigrate(&models.Domain{}, &models.Mailbox{}, &models.VacationConfig{},
		&models.VacationResponse{}, &models.OutboundQueue{}); err != nil {
		t.Skipf("vacation DB test skipped: migrate failed (%v)", err)
	}
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_vacation_responses_handle ON vacation_responses(mailbox_id, sender, handle)`).Error; err != nil {
		t.Fatal(err)
	}
	domain := models.Domain{Name: fmt.Sprintf("sieve-tracking-%d.test", time.Now().UnixNano()), Active: true}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	box := models.Mailbox{DomainID: domain.ID, LocalPart: "dana", Address: "dana@" + domain.Name, Active: true}
	if err := tx.Create(&box).Error; err != nil {
		t.Fatal(err)
	}
	vacation, err := NewVacation(tx)(nil)
	if err != nil {
		t.Fatal(err)
	}

	const (
		expanded = "require [\"vacation\", \"variables\"];\n" +
			"set \"s\" \"Away\";\n" +
			"vacation :subject \"${s}\" \"Back on Monday.\";\n"
		literal = "require \"vacation\";\n" +
			"vacation :subject \"Away\" \"Back on Monday.\";\n"
		fromTheMessage = "require [\"vacation\", \"variables\"];\n" +
			"if header :matches \"Subject\" \"*\" { set \"s\" \"Away: ${1}\"; }\n" +
			"vacation :subject \"${s}\" \"Back on Monday.\";\n"
	)
	// arrive runs script on a message from sender with subject, then the vacation filter,
	// and returns the subject the sieve filter asked the reply to carry and whether a
	// reply was queued.
	arrive := func(t *testing.T, script, sender, subject string) (string, bool) {
		t.Helper()
		raw, _ := json.Marshal(sieveConfig{Script: script})
		filter, err := NewSieve(raw)
		if err != nil {
			t.Fatal(err)
		}
		email := &pipeline.EmailJSON{
			Envelope: pipeline.Envelope{MailFrom: sender, RcptTo: []string{box.Address}},
			Headers: pipeline.Headers{
				From:      pipeline.Addresses(pipeline.Address{Address: sender}),
				To:        pipeline.Addresses(pipeline.Address{Address: box.Address}),
				Subject:   subject,
				MessageID: fmt.Sprintf("<%d@elsewhere.test>", time.Now().UnixNano()),
			},
			Body: pipeline.Body{ContentType: "text/plain", Content: "Hello."},
		}
		sieved, err := filter.Execute(context.Background(), email)
		if err != nil {
			t.Fatal(err)
		}
		replySubject := sieved.Message.Metadata["vacation_reply_subject"]
		res, err := vacation.Execute(context.Background(), sieved.Message)
		if err != nil {
			t.Fatal(err)
		}
		return replySubject, res.Log.Result == "queued"
	}

	t.Run("a variable subject and a literal one that expands alike are two responses", func(t *testing.T) {
		const sender = "one@elsewhere.test"
		subject, queued := arrive(t, expanded, sender, "Lunch?")
		if subject != "Away" || !queued {
			t.Fatalf("the expanded script: subject %q queued %v, want \"Away\" queued", subject, queued)
		}
		subject, queued = arrive(t, literal, sender, "Lunch?")
		if subject != "Away" || !queued {
			t.Fatalf("the literal script was tracked as the expanded one: subject %q queued %v, want \"Away\" queued", subject, queued)
		}
		if again, queued := arrive(t, expanded, sender, "Lunch?"); queued {
			t.Fatalf("the expanded script answered twice within its interval (subject %q)", again)
		}
	})

	t.Run("one variable subject is one response whatever it expands to", func(t *testing.T) {
		const sender = "two@elsewhere.test"
		subject, queued := arrive(t, fromTheMessage, sender, "Lunch?")
		if subject != "Away: Lunch?" || !queued {
			t.Fatalf("first message: subject %q queued %v", subject, queued)
		}
		subject, queued = arrive(t, fromTheMessage, sender, "Dinner?")
		if subject != "Away: Dinner?" {
			t.Fatalf("the subject was not expanded for the second message: %q", subject)
		}
		if queued {
			t.Fatal("a second message within the interval was answered again because its subject expanded differently")
		}
	})

	var replies []models.OutboundQueue
	tx.Where("sending_domain = ?", domain.Name).Order("id").Find(&replies)
	if len(replies) != 3 {
		t.Fatalf("%d replies queued, want 3", len(replies))
	}
	for i, want := range []string{"Subject: Away\r\n", "Subject: Away\r\n", "Subject: Away: Lunch?\r\n"} {
		if !strings.Contains(replies[i].RawMessage, want) {
			t.Errorf("reply %d lacks %q:\n%s", i, want, replies[i].RawMessage)
		}
	}
}
