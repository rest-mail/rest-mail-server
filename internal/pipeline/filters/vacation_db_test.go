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
