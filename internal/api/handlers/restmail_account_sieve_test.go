package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
	"github.com/restmail/restmail/internal/pipeline/filters"
)

// A message from another RESTMAIL server runs the domain's pipeline once for all its
// recipients, then each recipient's own Sieve script on that recipient alone (#787): one
// files and flags it, one refuses it for itself only, one without a script gets it in INBOX.
// A script that cannot run, changed behind the API, defers the whole message before any
// recipient has it; a domain whose pipeline has no sieve step runs no account's script.
func TestRESTMAILDeliver_RunsEachRecipientsSieveScript(t *testing.T) {
	db := openRedirectTestDB(t)
	if err := db.AutoMigrate(&models.SieveScript{}, &models.Quarantine{}, &models.VacationConfig{}, &models.VacationResponse{}); err != nil {
		t.Skipf("restmail account sieve DB test skipped: migrate failed (%v)", err)
	}
	engine := pipeline.NewEngine(pipeline.DefaultRegistry, slog.Default())
	const files = `require ["fileinto", "imap4flags"]; fileinto "Work"; addflag "\\Flagged";`
	const refuses = `require ["reject"]; reject "not for me";`

	// setup makes a domain with the given inbound pipeline and one mailbox per local part,
	// each with the script given for it (none for ""), stored under hash (its own when "").
	setup := func(t *testing.T, pipelineFilters []pipeline.FilterConfig, scripts map[string]string, hash string, engines ...*pipeline.Engine) (*RestmailHandler, map[string]models.Mailbox) {
		t.Helper()
		suffix := time.Now().UnixNano()
		dom := models.Domain{Name: fmt.Sprintf("rm-sieve-%d.test", suffix), Active: true}
		if err := db.Create(&dom).Error; err != nil {
			t.Fatalf("create domain: %v", err)
		}
		encoded, _ := json.Marshal(pipelineFilters)
		pl := models.Pipeline{DomainID: dom.ID, Direction: "inbound", Filters: encoded, Active: true}
		if err := db.Create(&pl).Error; err != nil {
			t.Fatalf("create pipeline: %v", err)
		}
		boxes := map[string]models.Mailbox{}
		for local, script := range scripts {
			mb := models.Mailbox{DomainID: dom.ID, LocalPart: local, Address: local + "@" + dom.Name, Active: true, QuotaBytes: 1 << 30}
			if err := db.Create(&mb).Error; err != nil {
				t.Fatalf("create mailbox: %v", err)
			}
			boxes[local] = mb
			if script != "" {
				h := hash
				if h == "" {
					h = filters.HashSieveScript(script)
				}
				if err := db.Create(&models.SieveScript{MailboxID: mb.ID, Script: script, ScriptHash: h, Active: true}).Error; err != nil {
					t.Fatalf("store the script: %v", err)
				}
			}
		}
		t.Cleanup(func() {
			for _, mb := range boxes {
				db.Where("mailbox_id = ?", mb.ID).Delete(&models.SieveScript{})
				db.Where("mailbox_id = ?", mb.ID).Delete(&models.Message{})
				db.Where("mailbox_id = ?", mb.ID).Delete(&models.Quarantine{})
				db.Where("mailbox_id = ?", mb.ID).Delete(&models.VacationResponse{})
				db.Where("sending_domain = ?", dom.Name).Delete(&models.OutboundQueue{})
				db.Where("id = ?", mb.ID).Delete(&models.Mailbox{})
			}
			db.Where("id = ?", pl.ID).Delete(&models.Pipeline{})
			db.Where("id = ?", dom.ID).Delete(&models.Domain{})
		})
		use := engine
		if len(engines) > 0 {
			use = engines[0]
		}
		return NewRestmailHandler(db, use, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{}), boxes
	}
	stored := func(t *testing.T, mailboxID uint) []models.Message {
		t.Helper()
		var messages []models.Message
		db.Where("mailbox_id = ?", mailboxID).Find(&messages)
		return messages
	}
	raw := "From: alice@remote.test\r\nSubject: Hi\r\n\r\nhello body\r\n"

	t.Run("each recipient's own script", func(t *testing.T) {
		h, boxes := setup(t, []pipeline.FilterConfig{sieveStep("")}, map[string]string{"files": files, "refuses": refuses, "plain": ""}, "")
		delivered, failed := deliverRESTMAIL(t, h, "alice@remote.test", raw, boxes["files"].Address, boxes["refuses"].Address, boxes["plain"].Address)
		if !slices.Equal(delivered, []string{boxes["files"].Address, boxes["plain"].Address}) || !slices.Equal(failed, []string{boxes["refuses"].Address}) {
			t.Fatalf("delivered %v failed %v, want files and plain delivered, refuses failed", delivered, failed)
		}
		if got := stored(t, boxes["files"].ID); len(got) != 1 || got[0].Folder != "Work" || !got[0].IsFlagged {
			t.Errorf("files stored %+v, want one message in Work, flagged", got)
		}
		if got := stored(t, boxes["plain"].ID); len(got) != 1 || got[0].Folder != "INBOX" || got[0].IsFlagged {
			t.Errorf("plain stored %+v, want one message in INBOX, unflagged", got)
		}
		if got := stored(t, boxes["refuses"].ID); len(got) != 0 {
			t.Errorf("refuses stored %+v, want nothing", got)
		}
	})

	t.Run("a drifted script defers the whole message", func(t *testing.T) {
		h, boxes := setup(t, []pipeline.FilterConfig{sieveStep("")}, map[string]string{"drifted": files, "plain": ""}, filters.HashSieveScript(`keep;`))
		body, _ := json.Marshal(map[string]any{"from": "alice@remote.test", "to": []string{boxes["plain"].Address, boxes["drifted"].Address},
			"subject": "Hi", "body_text": "hello body", "raw_message": raw})
		req := httptest.NewRequest(http.MethodPost, "/restmail/messages", bytes.NewReader(body))
		req.RemoteAddr = "203.0.113.9:5000"
		rr := httptest.NewRecorder()
		restmailRoutes(h).ServeHTTP(rr, req)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("deliver = %d %s, want 503: a script changed behind the API fails closed", rr.Code, rr.Body)
		}
		for local, mb := range boxes {
			if got := stored(t, mb.ID); len(got) != 0 {
				t.Errorf("%s stored %+v, want nothing before the peer retries", local, got)
			}
		}
	})

	t.Run("a recipient's own vacation answers the sender", func(t *testing.T) {
		// The API registers the vacation filter with its database at startup (routes.go); this
		// engine has its own registry, so the global one is left alone.
		registry := pipeline.NewRegistry()
		registry.Register("sieve", filters.NewSieve)
		registry.Register("vacation", filters.NewVacation(db))
		vacation := pipeline.FilterConfig{Name: "vacation", Type: pipeline.FilterTypeAction, Enabled: true, Config: json.RawMessage(`{}`)}
		h, boxes := setup(t, []pipeline.FilterConfig{sieveStep(""), vacation},
			map[string]string{"away": `require "vacation"; vacation :days 1 "I am away.";`, "plain": ""}, "",
			pipeline.NewEngine(registry, slog.Default()))
		addressed := "From: alice@remote.test\r\nTo: " + boxes["away"].Address + ", " + boxes["plain"].Address + "\r\nSubject: Lunch?\r\n\r\nhello body\r\n"
		delivered, _ := deliverRESTMAIL(t, h, "alice@remote.test", addressed, boxes["away"].Address, boxes["plain"].Address)
		if len(delivered) != 2 {
			t.Fatalf("delivered %v, want both recipients", delivered)
		}
		var replies []models.OutboundQueue
		db.Where("recipient = ?", "alice@remote.test").Find(&replies)
		if len(replies) != 1 || !strings.Contains(replies[0].RawMessage, "From: "+boxes["away"].Address+"\r\n") ||
			!strings.Contains(replies[0].RawMessage, "I am away.") {
			t.Fatalf("replies %+v, want one from away with its reason", replies)
		}
		for _, r := range replies {
			db.Delete(&r)
		}
	})

	t.Run("Sieve off for the domain", func(t *testing.T) {
		h, boxes := setup(t, []pipeline.FilterConfig{}, map[string]string{"files": files}, "")
		delivered, _ := deliverRESTMAIL(t, h, "alice@remote.test", raw, boxes["files"].Address)
		if len(delivered) != 1 {
			t.Fatalf("delivered %v, want the recipient", delivered)
		}
		if got := stored(t, boxes["files"].ID); len(got) != 1 || got[0].Folder != "INBOX" || got[0].IsFlagged {
			t.Errorf("stored %+v, want one message in INBOX, unflagged: the domain runs no account scripts", got)
		}
	})
}

// A recipient's copy names that recipient alone in its envelope and leaves out the actions
// the domain's run already carried out, keeping the folder and flags it chose (#787).
func TestRecipientCopy(t *testing.T) {
	shared := &pipeline.EmailJSON{
		Envelope: pipeline.Envelope{RcptTo: []string{"a@x.test", "b@x.test"}},
		Metadata: map[string]string{"deliver_to_folder": "Lists", "imap_flags": `\Seen`, "redirect_to": `["c@y.test"]`, "vacation_days": "7"},
	}
	got, err := recipientCopy(shared, "b@x.test")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Envelope.RcptTo, []string{"b@x.test"}) {
		t.Errorf("envelope %v, want b@x.test alone", got.Envelope.RcptTo)
	}
	want := map[string]string{"deliver_to_folder": "Lists", "imap_flags": `\Seen`}
	if fmt.Sprint(got.Metadata) != fmt.Sprint(want) {
		t.Errorf("metadata %v, want %v", got.Metadata, want)
	}
	if len(shared.Metadata) != 4 || len(shared.Envelope.RcptTo) != 2 {
		t.Errorf("the shared email changed: %+v", shared)
	}
}
