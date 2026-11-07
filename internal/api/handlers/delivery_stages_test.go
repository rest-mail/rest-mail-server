package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"gorm.io/gorm"
)

// stagedFilter notes that it ran, adds a header if it is the required one, and answers as told.
type stagedFilter struct {
	name   string
	action pipeline.Action
	ran    *[]string
	// sawCheck reports whether the content check could see what the required one added.
	sawCheck *bool
}

func (f *stagedFilter) Name() string              { return f.name }
func (f *stagedFilter) Type() pipeline.FilterType { return pipeline.FilterTypeTransform }
func (f *stagedFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	*f.ran = append(*f.ran, f.name)
	if f.name == "spf_check" {
		email.Headers.Raw["X-Stage-Check"] = []string{"spf pass"}
		pipeline.AuthResultsFromContext(ctx).Add(pipeline.AuthResult{Method: pipeline.AuthSPF, Result: "pass", Property: pipeline.PropMailFrom, Domain: "remote.test"})
	}
	if f.sawCheck != nil {
		*f.sawCheck = email.Headers.Raw["X-Stage-Check"] != nil
	}
	return &pipeline.FilterResult{Type: pipeline.FilterTypeTransform, Action: f.action, Message: email}, nil
}

var stagedSawCheck bool

func stagedHandler(t *testing.T, content pipeline.Action) (*MessageHandler, localDeliveryParams, *gorm.DB, *[]string) {
	t.Helper()
	db := openAccountsTestDB(t)
	if err := migrateModels(db, &models.Attachment{}, &models.Pipeline{}, &models.Quarantine{}); err != nil {
		t.Fatal(err)
	}
	fx := seedAccountsFixture(t, db)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	// Mail is delivered only to a live domain's mailboxes (#1845).
	if err := tx.Model(&models.Domain{}).Where("id = ?", fx.domain.ID).Update("active", true).Error; err != nil {
		t.Fatal(err)
	}
	stored, err := json.Marshal([]pipeline.FilterConfig{
		{Name: "spf_check", Type: pipeline.FilterTypeAction, Enabled: true},
		{Name: "rspamd", Type: pipeline.FilterTypeAction, Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Create(&models.Pipeline{DomainID: fx.domain.ID, Direction: "inbound", Active: true, Filters: stored}).Error; err != nil {
		t.Fatal(err)
	}
	ran := &[]string{}
	registry := withQuietVacation(pipeline.NewRegistry())
	registry.Register("spf_check", func([]byte) (pipeline.Filter, error) {
		return &stagedFilter{name: "spf_check", action: pipeline.ActionContinue, ran: ran}, nil
	})
	registry.Register("rspamd", func([]byte) (pipeline.Filter, error) {
		return &stagedFilter{name: "rspamd", action: content, ran: ran, sawCheck: &stagedSawCheck}, nil
	})
	h := NewMessageHandler(tx, nil, pipeline.NewEngine(registry, nil), "", nil)
	raw := "From: sender@remote.test\r\nSubject: staged\r\n\r\nbody\r\n"
	child := quotaChild(t, tx, fx.admin, "staged", 0, 0)
	return h, localDeliveryParams{Mailbox: child, Sender: "sender@remote.test", Subject: "staged", BodyText: "body", RawMessage: raw}, tx, ran
}

func TestRequiredStageStoresNothing(t *testing.T) {
	h, params, tx, ran := stagedHandler(t, pipeline.ActionContinue)
	params.Stage, params.State = deliverRequired, newDeliveryState()
	before := countMessages(t, tx, params.Mailbox.ID)
	if _, err := h.deliverToLocal(context.Background(), params); !errors.Is(err, errContentStageNext) {
		t.Fatalf("got %v", err)
	}
	if countMessages(t, tx, params.Mailbox.ID) != before {
		t.Fatal("the required stage stored a message")
	}
	if len(*ran) != 1 || (*ran)[0] != "spf_check" {
		t.Fatalf("ran %v", *ran)
	}
	if params.State.RawHeaders["X-Stage-Check"] == nil {
		t.Fatal("what the required check added was not kept")
	}
}

func TestContentStageStoresWhatTheRequiredStageSaw(t *testing.T) {
	h, params, _, ran := stagedHandler(t, pipeline.ActionContinue)
	params.Stage, params.State = deliverRequired, newDeliveryState()
	if _, err := h.deliverToLocal(context.Background(), params); !errors.Is(err, errContentStageNext) {
		t.Fatalf("got %v", err)
	}
	// As the queue does: the state is stored between the stages.
	stored, err := json.Marshal(params.State)
	if err != nil {
		t.Fatal(err)
	}
	restored := newDeliveryState()
	if err := json.Unmarshal(stored, restored); err != nil {
		t.Fatal(err)
	}
	params.Stage, params.State = deliverContent, restored
	committed := false
	params.InCommit = func(*gorm.DB) error { committed = true; return nil }
	msg, err := h.deliverToLocal(context.Background(), params)
	if err != nil || msg == nil {
		t.Fatalf("%+v, %v", msg, err)
	}
	if !committed {
		t.Fatal("InCommit did not run")
	}
	if len(*ran) != 2 || (*ran)[1] != "rspamd" {
		t.Fatalf("ran %v", *ran)
	}
	if !stagedSawCheck {
		t.Fatal("the content check could not see what the required check added")
	}
	// The required check's verdict reaches the stored message.
	if !strings.Contains(msg.RawMessage, "spf=pass") {
		t.Fatalf("the stored message lost the required check's verdict: %q", msg.RawMessage)
	}
}

// Taking a message off the queue and storing it happen together: if the row is gone because
// another worker took it over, this one stores nothing.
func TestAFailedCommitHookStoresNothing(t *testing.T) {
	h, params, tx, _ := stagedHandler(t, pipeline.ActionContinue)
	params.Stage, params.State = deliverContent, newDeliveryState()
	taken := errors.New("another worker finished it")
	params.InCommit = func(*gorm.DB) error { return taken }
	before := countMessages(t, tx, params.Mailbox.ID)
	if _, err := h.deliverToLocal(context.Background(), params); !errors.Is(err, taken) {
		t.Fatalf("got %v", err)
	}
	if countMessages(t, tx, params.Mailbox.ID) != before {
		t.Fatal("a message was stored anyway")
	}
}

// Whole delivery behaves as it did: both checks, in one run.
func TestWholeDeliveryRunsEverything(t *testing.T) {
	h, params, _, ran := stagedHandler(t, pipeline.ActionContinue)
	msg, err := h.deliverToLocal(context.Background(), params)
	if err != nil || msg == nil {
		t.Fatalf("%+v, %v", msg, err)
	}
	if len(*ran) != 2 {
		t.Fatalf("ran %v", *ran)
	}
}
