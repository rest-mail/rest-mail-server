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

// A vacation reply is queued with a null reverse path (RFC 5230 §5.1), From:
// the mailbox, and the mailbox's domain to sign and route it as (#716).
func TestVacation_ReplyLeavesWithNullSender(t *testing.T) {
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

	domain := models.Domain{Name: fmt.Sprintf("vacation-%d.test", time.Now().UnixNano()), Active: true}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	box := models.Mailbox{DomainID: domain.ID, LocalPart: "dana", Address: "dana@" + domain.Name, Active: true}
	if err := tx.Create(&box).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Create(&models.VacationConfig{MailboxID: box.ID, Enabled: true, Subject: "Away", Body: "Back on Monday."}).Error; err != nil {
		t.Fatal(err)
	}

	f, err := NewVacation(tx)(nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.Execute(context.Background(), &pipeline.EmailJSON{
		Envelope: pipeline.Envelope{MailFrom: "Friend@Elsewhere.test", RcptTo: []string{box.Address}},
		Headers:  pipeline.Headers{From: []pipeline.Address{{Address: "friend@elsewhere.test"}}, MessageID: "<m1@elsewhere.test>"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Log.Result != "queued" {
		t.Fatalf("no reply queued: %+v", res.Log)
	}

	var queued []models.OutboundQueue
	if err := tx.Where("recipient = ?", "friend@elsewhere.test").Find(&queued).Error; err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 {
		t.Fatalf("queued %d replies, want 1", len(queued))
	}
	reply := queued[0]
	if reply.Sender != "" || reply.SendingDomain != domain.Name {
		t.Errorf("queued as sender %q for %q, want the null sender for %s", reply.Sender, reply.SendingDomain, domain.Name)
	}
	if !strings.Contains(reply.RawMessage, "From: "+box.Address+"\r\n") {
		t.Errorf("the reply is not From: the mailbox:\n%s", reply.RawMessage)
	}
}

// defaultVacationStep is the vacation step of the default inbound pipeline, built from the
// config the pipeline gives it.
func defaultVacationStep(t *testing.T) pipeline.FilterConfig {
	t.Helper()
	for _, step := range pipeline.DefaultInboundPipeline(1).Filters {
		if step.Name == "vacation" {
			return step
		}
	}
	t.Fatal("the default inbound pipeline has no vacation step (#772)")
	return pipeline.FilterConfig{}
}

// The default inbound pipeline's vacation step builds from its own config, with the RFC 5230
// default interval, and runs after Sieve, so a message Sieve refuses gets no reply (#772).
func TestDefaultInboundPipeline_RunsVacationAfterSieve(t *testing.T) {
	step := defaultVacationStep(t)
	f, err := NewVacation(nil)(step.Config)
	if err != nil {
		t.Fatalf("the default vacation config does not build: %v", err)
	}
	if days := f.(*vacationFilter).cfg.Days; days != 7 {
		t.Errorf("default interval = %d days, want 7", days)
	}
	names := []string{}
	for _, s := range pipeline.DefaultInboundPipeline(1).Filters {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") == "" || names[len(names)-1] != "vacation" || names[len(names)-2] != "sieve" {
		t.Fatalf("vacation does not follow sieve last: %v", names)
	}
}

// A sender is answered at most once per interval, the interval running from the latest
// reply: once it has passed, the next message is answered and the one after it is not
// (#809). The reply row used to keep the first reply's time, the insert of the second
// failing on the (mailbox_id, sender) unique index, so every message after the first
// interval was answered.
func TestVacation_IntervalRunsFromTheLatestReply(t *testing.T) {
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
	// The index the migration makes (internal/db), which a plain AutoMigrate does not.
	if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_vacation_responses_unique ON vacation_responses(mailbox_id, sender)`).Error; err != nil {
		t.Fatal(err)
	}

	domain := models.Domain{Name: fmt.Sprintf("vacation-interval-%d.test", time.Now().UnixNano()), Active: true}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	box := models.Mailbox{DomainID: domain.ID, LocalPart: "dana", Address: "dana@" + domain.Name, Active: true}
	if err := tx.Create(&box).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Create(&models.VacationConfig{MailboxID: box.ID, Enabled: true, Subject: "Away", Body: "Back on Monday."}).Error; err != nil {
		t.Fatal(err)
	}
	f, err := NewVacation(tx)(nil)
	if err != nil {
		t.Fatal(err)
	}
	arrive := func(n int) string {
		t.Helper()
		res, err := f.Execute(context.Background(), &pipeline.EmailJSON{
			Envelope: pipeline.Envelope{MailFrom: "friend@elsewhere.test", RcptTo: []string{box.Address}},
			Headers:  pipeline.Headers{From: []pipeline.Address{{Address: "friend@elsewhere.test"}}, MessageID: fmt.Sprintf("<m%d@elsewhere.test>", n)},
		})
		if err != nil {
			t.Fatal(err)
		}
		return res.Log.Result
	}
	// age moves the recorded reply to before the default seven-day interval.
	age := func() {
		t.Helper()
		if err := tx.Model(&models.VacationResponse{}).Where("mailbox_id = ?", box.ID).
			Update("responded_at", time.Now().Add(-8*24*time.Hour)).Error; err != nil {
			t.Fatal(err)
		}
	}

	if got := arrive(1); got != "queued" {
		t.Fatalf("the first message: %s, want queued", got)
	}
	if got := arrive(2); got == "queued" {
		t.Fatal("a second message within the interval was answered")
	}
	age()
	if got := arrive(3); got != "queued" {
		t.Fatalf("the first message after the interval: %s, want queued", got)
	}
	if got := arrive(4); got == "queued" {
		t.Fatal("the message after that was answered too: the interval did not restart at the latest reply")
	}
	var queued int64
	tx.Model(&models.OutboundQueue{}).Where("recipient = ?", "friend@elsewhere.test").Count(&queued)
	if queued != 2 {
		t.Errorf("queued %d replies, want 2", queued)
	}
}
