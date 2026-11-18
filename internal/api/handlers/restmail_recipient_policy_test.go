package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"gorm.io/gorm"
)

// policyRun is one run of a domain's inbound pipeline, and the recipients it was told about.
type policyRun struct {
	Domain string
	RcptTo []string
}

// policyRuns records the pipeline runs of one delivery, in order.
type policyRuns struct {
	mu   sync.Mutex
	runs []policyRun
}

func (p *policyRuns) add(run policyRun) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.runs = append(p.runs, run)
}

func (p *policyRuns) take() []policyRun {
	p.mu.Lock()
	defer p.mu.Unlock()
	taken := p.runs
	p.runs = nil
	return taken
}

// policyStep stands in for a domain's whole inbound policy: it records that this domain's
// pipeline ran and answers the verdict stored in that pipeline's own configuration.
type policyStep struct {
	Domain string          `json:"domain"`
	Action pipeline.Action `json:"action"`
	runs   *policyRuns
}

func (s *policyStep) Name() string              { return "recipient_policy" }
func (s *policyStep) Type() pipeline.FilterType { return pipeline.FilterTypeAction }
func (s *policyStep) Execute(_ context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	s.runs.add(policyRun{Domain: s.Domain, RcptTo: slices.Clone(email.Envelope.RcptTo)})
	return &pipeline.FilterResult{Type: pipeline.FilterTypeAction, Action: s.Action, RejectMsg: "refused by " + s.Domain,
		Log: pipeline.FilterLog{Filter: s.Name(), Result: string(s.Action)}}, nil
}

// recipientPolicyFixture is two hosted domains, each with one mailbox and its own pipeline.
type recipientPolicyFixture struct {
	db            *gorm.DB
	handler       *RestmailHandler
	runs          *policyRuns
	first, second models.Domain
	ann, bob      models.Mailbox
}

func newRecipientPolicyFixture(t *testing.T, firstAction, secondAction pipeline.Action) *recipientPolicyFixture {
	t.Helper()
	db := openRedirectTestDB(t)
	if err := migrateModels(db, &models.Quarantine{}, &models.TLSReport{}, &models.Attachment{},
		&models.VacationConfig{}, &models.VacationResponse{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	fixture := &recipientPolicyFixture{db: db, runs: &policyRuns{}}
	registry := withQuietVacation(pipeline.NewRegistry())
	registry.Register("recipient_policy", func(config []byte) (pipeline.Filter, error) {
		step := &policyStep{runs: fixture.runs}
		return step, json.Unmarshal(config, step)
	})
	engine := pipeline.NewEngine(registry, slog.Default())
	fixture.handler = NewRestmailHandler(db, engine, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{})
	fixture.handler.SetLocalDelivery(NewMessageHandler(db, nil, engine, "", nil))

	suffix := time.Now().UnixNano()
	host := func(label, local string, action pipeline.Action) (models.Domain, models.Mailbox) {
		domain := models.Domain{Name: fmt.Sprintf("%s-%d.test", label, suffix), Active: true}
		if err := db.Create(&domain).Error; err != nil {
			t.Fatalf("create domain: %v", err)
		}
		config, _ := json.Marshal(map[string]any{"domain": label, "action": action})
		steps, _ := json.Marshal([]pipeline.FilterConfig{{Name: "recipient_policy", Type: pipeline.FilterTypeAction, Enabled: true, Config: config}})
		if err := db.Create(&models.Pipeline{DomainID: domain.ID, Direction: "inbound", Filters: steps, Active: true}).Error; err != nil {
			t.Fatalf("create pipeline: %v", err)
		}
		mailbox := models.Mailbox{DomainID: domain.ID, LocalPart: local, Address: local + "@" + domain.Name, Active: true, QuotaBytes: 1 << 30}
		if err := db.Create(&mailbox).Error; err != nil {
			t.Fatalf("create mailbox: %v", err)
		}
		seedReceiveFixture(t, db, mailbox)
		return domain, mailbox
	}
	fixture.first, fixture.ann = host("first", "ann", firstAction)
	fixture.second, fixture.bob = host("second", "bob", secondAction)
	t.Cleanup(func() {
		domains := []uint{fixture.first.ID, fixture.second.ID}
		mailboxes := []uint{fixture.ann.ID, fixture.bob.ID}
		db.Where("mailbox_id IN ?", mailboxes).Delete(&models.Message{})
		db.Where("mailbox_id IN ?", mailboxes).Delete(&models.Quarantine{})
		db.Where("domain_id IN ?", domains).Delete(&models.TLSReport{})
		db.Where("domain_id IN ?", domains).Delete(&models.Alias{})
		db.Where("domain_id IN ?", domains).Delete(&models.Pipeline{})
		db.Where("id IN ?", mailboxes).Delete(&models.Mailbox{})
		db.Where("id IN ?", domains).Delete(&models.Domain{})
	})
	return fixture
}

// policyAnswer is what the route answered one delivery.
type policyAnswer struct {
	status    int
	code      string
	delivered []string
	failed    []string
}

func (f *recipientPolicyFixture) deliver(t *testing.T, raw string, to ...string) policyAnswer {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"from": "sender@remote.test", "to": to, "raw_message": []byte(raw)})
	request := httptest.NewRequest(http.MethodPost, restmailMessagesPath, bytes.NewReader(body))
	request.RemoteAddr = "203.0.113.9:5000"
	recorder := httptest.NewRecorder()
	restmailRoutes(f.handler).ServeHTTP(recorder, request)
	var decoded struct {
		Data struct {
			Delivered []string `json:"delivered"`
			Failed    []string `json:"failed"`
		} `json:"data"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode %d %q: %v", recorder.Code, recorder.Body, err)
	}
	return policyAnswer{status: recorder.Code, code: decoded.Error.Code, delivered: decoded.Data.Delivered, failed: decoded.Data.Failed}
}

func (f *recipientPolicyFixture) count(t *testing.T, model any, column string, id uint) int64 {
	t.Helper()
	var count int64
	if err := f.db.Model(model).Where(column+" = ?", id).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	return count
}

// wantStored checks what each mailbox holds, read from the database.
func (f *recipientPolicyFixture) wantStored(t *testing.T, annMessages, bobMessages, annHolds, bobHolds int64) {
	t.Helper()
	for _, check := range []struct {
		what  string
		model any
		id    uint
		want  int64
	}{
		{"ann's messages", &models.Message{}, f.ann.ID, annMessages},
		{"bob's messages", &models.Message{}, f.bob.ID, bobMessages},
		{"ann's held messages", &models.Quarantine{}, f.ann.ID, annHolds},
		{"bob's held messages", &models.Quarantine{}, f.bob.ID, bobHolds},
	} {
		if got := f.count(t, check.model, "mailbox_id", check.id); got != check.want {
			t.Errorf("%s = %d, want %d", check.what, got, check.want)
		}
	}
}

func wantPolicyRuns(t *testing.T, got []policyRun, want ...policyRun) {
	t.Helper()
	if !slices.EqualFunc(got, want, func(a, b policyRun) bool { return a.Domain == b.Domain && slices.Equal(a.RcptTo, b.RcptTo) }) {
		t.Errorf("pipeline runs = %+v, want %+v", got, want)
	}
}

const policyMessage = "From: sender@remote.test\r\nSubject: Hi\r\nMessage-ID: <policy@remote.test>\r\n\r\nhello\r\n"

// A RESTMAIL delivery to several domains runs one inbound pipeline, the one of the first
// recipient's domain, and that pipeline's verdict is every recipient's. These scenarios pin
// the behaviour #1161 asked to have checked explicitly, without changing it. Each asserts the
// pipelines that really ran and the rows that were really stored.
func TestRESTMAILDeliver_FirstRecipientDomainChoosesThePipeline(t *testing.T) {
	const (
		cont       = pipeline.ActionContinue
		reject     = pipeline.ActionReject
		deferred   = pipeline.ActionDefer
		quarantine = pipeline.ActionQuarantine
		discard    = pipeline.ActionDiscard
	)

	t.Run("the first domain's pipeline accepts for the second domain, whose own would refuse", func(t *testing.T) {
		f := newRecipientPolicyFixture(t, cont, reject)
		got := f.deliver(t, policyMessage, f.ann.Address, f.bob.Address)
		if got.status != http.StatusCreated || !slices.Equal(got.delivered, []string{f.ann.Address, f.bob.Address}) || len(got.failed) != 0 {
			t.Fatalf("answer = %+v, want 201 with both delivered", got)
		}
		wantPolicyRuns(t, f.runs.take(), policyRun{"first", []string{f.ann.Address, f.bob.Address}})
		f.wantStored(t, 1, 1, 0, 0)
	})

	t.Run("the same recipients in the other order are refused by the other domain", func(t *testing.T) {
		f := newRecipientPolicyFixture(t, cont, reject)
		got := f.deliver(t, policyMessage, f.bob.Address, f.ann.Address)
		if got.status != http.StatusForbidden || got.code != "rejected" {
			t.Fatalf("answer = %+v, want 403 rejected", got)
		}
		wantPolicyRuns(t, f.runs.take(), policyRun{"second", []string{f.bob.Address, f.ann.Address}})
		f.wantStored(t, 0, 0, 0, 0)
	})

	t.Run("the first domain's deferral holds back every recipient", func(t *testing.T) {
		f := newRecipientPolicyFixture(t, deferred, cont)
		got := f.deliver(t, policyMessage, f.ann.Address, f.bob.Address)
		if got.status != http.StatusServiceUnavailable || got.code != "deferred" {
			t.Fatalf("answer = %+v, want 503 deferred", got)
		}
		wantPolicyRuns(t, f.runs.take(), policyRun{"first", []string{f.ann.Address, f.bob.Address}})
		f.wantStored(t, 0, 0, 0, 0)
	})

	t.Run("a later domain's deferral is never asked", func(t *testing.T) {
		f := newRecipientPolicyFixture(t, cont, deferred)
		got := f.deliver(t, policyMessage, f.ann.Address, f.bob.Address)
		if got.status != http.StatusCreated || len(got.delivered) != 2 {
			t.Fatalf("answer = %+v, want 201 with both delivered", got)
		}
		wantPolicyRuns(t, f.runs.take(), policyRun{"first", []string{f.ann.Address, f.bob.Address}})
		f.wantStored(t, 1, 1, 0, 0)
	})

	t.Run("the first domain's quarantine holds every recipient's copy", func(t *testing.T) {
		f := newRecipientPolicyFixture(t, quarantine, cont)
		got := f.deliver(t, policyMessage, f.ann.Address, f.bob.Address)
		if got.status != http.StatusCreated || len(got.delivered) != 2 {
			t.Fatalf("answer = %+v, want 201 with both delivered", got)
		}
		f.wantStored(t, 0, 0, 1, 1)
	})

	t.Run("the first domain's discard drops every recipient's copy", func(t *testing.T) {
		f := newRecipientPolicyFixture(t, discard, cont)
		got := f.deliver(t, policyMessage, f.ann.Address, f.bob.Address)
		if got.status != http.StatusCreated || len(got.delivered) != 2 {
			t.Fatalf("answer = %+v, want 201 with both delivered", got)
		}
		f.wantStored(t, 0, 0, 0, 0)
	})

	t.Run("an unknown address does not choose the pipeline", func(t *testing.T) {
		f := newRecipientPolicyFixture(t, cont, reject)
		unknown := "nobody@" + f.second.Name
		got := f.deliver(t, policyMessage, unknown, f.ann.Address)
		if got.status != http.StatusCreated || !slices.Equal(got.delivered, []string{f.ann.Address}) || !slices.Equal(got.failed, []string{unknown}) {
			t.Fatalf("answer = %+v, want 201 with ann delivered and the unknown address failed", got)
		}
		wantPolicyRuns(t, f.runs.take(), policyRun{"first", []string{f.ann.Address}})
		f.wantStored(t, 1, 0, 0, 0)
	})

	t.Run("an alias among the recipients gives each its own domain's pipeline", func(t *testing.T) {
		f := newRecipientPolicyFixture(t, cont, reject)
		alias := "alias@" + f.first.Name
		if err := f.db.Create(&models.Alias{DomainID: f.first.ID, SourceAddress: alias, DestinationAddress: f.ann.Address, Active: true}).Error; err != nil {
			t.Fatal(err)
		}
		got := f.deliver(t, policyMessage, alias, f.bob.Address)
		if got.status != http.StatusCreated || !slices.Equal(got.delivered, []string{alias}) || !slices.Equal(got.failed, []string{f.bob.Address}) {
			t.Fatalf("answer = %+v, want 201 with the alias delivered and bob failed", got)
		}
		wantPolicyRuns(t, f.runs.take(), policyRun{"first", []string{alias}}, policyRun{"second", []string{f.bob.Address}})
		f.wantStored(t, 1, 0, 0, 0)
	})
}

// A delivery that names the TLS report mailbox and ordinary recipients is two things at once.
// The report is consumed first, and the mail goes on without that recipient.
func TestRESTMAILDeliver_MixedReportAndMail(t *testing.T) {
	// reportFixture makes bob the report mailbox and returns a signed report about the first domain.
	reportFixture := func(t *testing.T, first, second pipeline.Action) (*recipientPolicyFixture, string) {
		t.Helper()
		f := newRecipientPolicyFixture(t, first, second)
		receiver := NewTLSReportEmailReceiver(f.db, f.bob.Address)
		raw, resolver := signedTLSReport(t, tlsReportEmail(t, tlsReportEmailBody(t, f.first.Name), true), "tlsrpt")
		receiver.resolver = resolver
		f.handler.SetTLSReportEmailReceiver(receiver)
		return f, raw
	}
	reports := func(t *testing.T, f *recipientPolicyFixture) int64 {
		t.Helper()
		return f.count(t, &models.TLSReport{}, "domain_id", f.first.ID)
	}

	t.Run("the report mailbox does not choose the pipeline", func(t *testing.T) {
		f, raw := reportFixture(t, pipeline.ActionContinue, pipeline.ActionReject)
		got := f.deliver(t, raw, f.bob.Address, f.ann.Address)
		if got.status != http.StatusCreated || !slices.Equal(got.delivered, []string{f.bob.Address, f.ann.Address}) || len(got.failed) != 0 {
			t.Fatalf("answer = %+v, want 201 with both delivered", got)
		}
		wantPolicyRuns(t, f.runs.take(), policyRun{"first", []string{f.ann.Address}})
		if got := reports(t, f); got != 1 {
			t.Errorf("stored reports = %d, want 1", got)
		}
		f.wantStored(t, 1, 0, 0, 0)
	})

	t.Run("a delivery only to the report mailbox runs no pipeline", func(t *testing.T) {
		f, raw := reportFixture(t, pipeline.ActionReject, pipeline.ActionReject)
		got := f.deliver(t, raw, f.bob.Address)
		if got.status != http.StatusCreated || !slices.Equal(got.delivered, []string{f.bob.Address}) {
			t.Fatalf("answer = %+v, want 201 with the report mailbox delivered", got)
		}
		wantPolicyRuns(t, f.runs.take())
		if got := reports(t, f); got != 1 {
			t.Errorf("stored reports = %d, want 1", got)
		}
		f.wantStored(t, 0, 0, 0, 0)
	})

	t.Run("a report stays stored when the mail beside it is refused", func(t *testing.T) {
		f, raw := reportFixture(t, pipeline.ActionReject, pipeline.ActionContinue)
		got := f.deliver(t, raw, f.bob.Address, f.ann.Address)
		if got.status != http.StatusForbidden || got.code != "rejected" {
			t.Fatalf("answer = %+v, want 403 rejected", got)
		}
		wantPolicyRuns(t, f.runs.take(), policyRun{"first", []string{f.ann.Address}})
		if got := reports(t, f); got != 1 {
			t.Errorf("stored reports = %d, want 1", got)
		}
		f.wantStored(t, 0, 0, 0, 0)
	})

	// The report is stored before the pipeline defers the mail, and nothing recognises it when
	// the peer sends the same message again, so each attempt adds a row. This pins what happens
	// today; it is not a statement that a second row is right.
	t.Run("each attempt after a deferral stores the report again", func(t *testing.T) {
		f, raw := reportFixture(t, pipeline.ActionDefer, pipeline.ActionContinue)
		for attempt := int64(1); attempt <= 2; attempt++ {
			got := f.deliver(t, raw, f.bob.Address, f.ann.Address)
			if got.status != http.StatusServiceUnavailable || got.code != "deferred" {
				t.Fatalf("attempt %d: answer = %+v, want 503 deferred", attempt, got)
			}
			if stored := reports(t, f); stored != attempt {
				t.Errorf("after attempt %d: stored reports = %d, want %d", attempt, stored, attempt)
			}
		}
		f.wantStored(t, 0, 0, 0, 0)
	})
}
