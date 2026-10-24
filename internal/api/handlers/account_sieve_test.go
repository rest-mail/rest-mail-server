package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
	"github.com/restmail/restmail/internal/pipeline/filters"
)

func sieveStep(script string) pipeline.FilterConfig {
	config := json.RawMessage(`{}`)
	if script != "" {
		config, _ = json.Marshal(map[string]string{"script": script})
	}
	return pipeline.FilterConfig{Name: "sieve", Type: pipeline.FilterTypeTransform, Enabled: true, Config: config}
}

func stepScripts(t *testing.T, cfg *pipeline.PipelineConfig) []string {
	t.Helper()
	var out []string
	for _, step := range cfg.Filters {
		if step.Name != "sieve" {
			out = append(out, step.Name)
			continue
		}
		var c struct {
			Script    string `json:"script"`
			MailboxID uint   `json:"mailbox_id"`
		}
		_ = json.Unmarshal(step.Config, &c)
		out = append(out, fmt.Sprintf("sieve(%q,%d)", c.Script, c.MailboxID))
	}
	return out
}

// The mailbox's script goes in the pipeline's empty sieve step; after a domain-level script,
// in a step of its own; nowhere when the pipeline has no sieve step. The shared pipeline is
// never changed (#787).
func TestWithSieveScript(t *testing.T) {
	const mine = `keep;`
	other := pipeline.FilterConfig{Name: "vacation", Type: pipeline.FilterTypeAction, Enabled: true}
	for _, tc := range []struct {
		name    string
		filters []pipeline.FilterConfig
		want    []string
	}{
		{"an empty step takes it", []pipeline.FilterConfig{sieveStep(""), other},
			[]string{`sieve("keep;",7)`, "vacation"}},
		{"after a domain's script", []pipeline.FilterConfig{sieveStep(`discard;`), other},
			[]string{`sieve("discard;",0)`, `sieve("keep;",7)`, "vacation"}},
		{"a disabled step is not Sieve on", []pipeline.FilterConfig{{Name: "sieve", Enabled: false}, other},
			[]string{`sieve("",0)`, "vacation"}},
		{"no sieve step, no script", []pipeline.FilterConfig{other}, []string{"vacation"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shared := &pipeline.PipelineConfig{Filters: tc.filters}
			before := stepScripts(t, shared)
			got, err := withSieveScript(shared, 7, mine, "hash")
			if err != nil {
				t.Fatal(err)
			}
			if steps := stepScripts(t, got); !reflect.DeepEqual(steps, tc.want) {
				t.Errorf("steps %v, want %v", steps, tc.want)
			}
			if after := stepScripts(t, shared); !reflect.DeepEqual(after, before) {
				t.Errorf("the shared pipeline changed: %v, was %v", after, before)
			}
		})
	}
}

// fileinto's folder is where the message goes, INBOX in any case is INBOX, and one the folder
// rules refuse is INBOX; imap4flags' \Seen, \Flagged and \Draft are kept (#787).
func TestSieveFolderAndFlags(t *testing.T) {
	email := func(meta map[string]string) *pipeline.EmailJSON { return &pipeline.EmailJSON{Metadata: meta} }
	for folder, want := range map[string]string{"": "INBOX", "Work": "Work", "inbox": "INBOX", "a/../b": "INBOX", " Work": "INBOX"} {
		if got := sieveFolder(email(map[string]string{"deliver_to_folder": folder}), "u@example.test"); got != want {
			t.Errorf("fileinto %q delivers to %q, want %q", folder, got, want)
		}
	}
	if got := sieveFolder(email(nil), "u@example.test"); got != "INBOX" {
		t.Errorf("no metadata delivers to %q", got)
	}
	got := sieveFlagsOf(email(map[string]string{"imap_flags": `\Seen \flagged $Label \Deleted \Draft`}))
	if want := (sieveFlags{seen: true, flagged: true, draft: true}); got != want {
		t.Errorf("flags %+v, want %+v", got, want)
	}
}

// An account's stored script runs on its mail, delivered as the SMTP gateway delivers it
// (POST /api/v1/messages/deliver, deliverToLocal): fileinto and addflag shape the stored
// message; a script changed behind the API fails closed; a domain whose pipeline has no sieve
// step runs none (#787).
func TestDelivery_RunsTheAccountsSieveScript(t *testing.T) {
	db := openRedirectTestDB(t)
	if err := db.AutoMigrate(&models.SieveScript{}); err != nil {
		t.Skipf("account sieve DB test skipped: migrate failed (%v)", err)
	}
	engine := pipeline.NewEngine(pipeline.DefaultRegistry, slog.Default())
	const script = `require ["fileinto", "imap4flags"]; fileinto "Work"; addflag "\\Flagged";`

	setup := func(t *testing.T, local string, filters []pipeline.FilterConfig, hash string) (*MessageHandler, models.Mailbox) {
		t.Helper()
		suffix := time.Now().UnixNano()
		dom := models.Domain{Name: fmt.Sprintf("acct-sieve-%d.test", suffix), Active: true}
		if err := db.Create(&dom).Error; err != nil {
			t.Fatalf("create domain: %v", err)
		}
		mb := models.Mailbox{DomainID: dom.ID, LocalPart: local, Address: local + "@" + dom.Name, Active: true, QuotaBytes: 1 << 30}
		if err := db.Create(&mb).Error; err != nil {
			t.Fatalf("create mailbox: %v", err)
		}
		encoded, _ := json.Marshal(filters)
		pl := models.Pipeline{DomainID: dom.ID, Direction: "inbound", Filters: encoded, Active: true}
		if err := db.Create(&pl).Error; err != nil {
			t.Fatalf("create pipeline: %v", err)
		}
		stored := models.SieveScript{MailboxID: mb.ID, Script: script, ScriptHash: hash, Active: true}
		if err := db.Create(&stored).Error; err != nil {
			t.Fatalf("store the script: %v", err)
		}
		t.Cleanup(func() {
			db.Where("mailbox_id = ?", mb.ID).Delete(&models.SieveScript{})
			db.Where("mailbox_id = ?", mb.ID).Delete(&models.Message{})
			db.Where("id = ?", pl.ID).Delete(&models.Pipeline{})
			db.Where("id = ?", mb.ID).Delete(&models.Mailbox{})
			db.Where("id = ?", dom.ID).Delete(&models.Domain{})
		})
		return NewMessageHandler(db, nil, engine, "", nil), mb
	}
	stored := func(t *testing.T, mailboxID uint) []models.Message {
		t.Helper()
		var messages []models.Message
		db.Where("mailbox_id = ?", mailboxID).Find(&messages)
		return messages
	}
	deliver := func(t *testing.T, h *MessageHandler, to string) int {
		t.Helper()
		body, _ := json.Marshal(map[string]any{
			"address": to, "sender": "alice@remote.test", "subject": "Hi", "body_text": "hello body",
			// The raw message is base64 on the wire: the route reads it as []byte.
			"raw_message": []byte("From: alice@remote.test\r\nTo: " + to + "\r\nSubject: Hi\r\n\r\nhello body\r\n"),
		})
		rr := httptest.NewRecorder()
		deliveryRoute(h)(rr, httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", bytes.NewReader(body)))
		return rr.Code
	}

	t.Run("fileinto and addflag", func(t *testing.T) {
		h, mb := setup(t, "files", []pipeline.FilterConfig{sieveStep("")}, filters.HashSieveScript(script))
		if code := deliver(t, h, mb.Address); code != http.StatusCreated {
			t.Fatalf("deliver = %d, want 201", code)
		}
		got := stored(t, mb.ID)
		if len(got) != 1 || got[0].Folder != "Work" || !got[0].IsFlagged {
			t.Fatalf("stored %+v, want one message in Work, flagged", got)
		}
	})

	t.Run("a drifted script fails closed", func(t *testing.T) {
		h, mb := setup(t, "drifted", []pipeline.FilterConfig{sieveStep("")}, filters.HashSieveScript(`keep;`))
		if code := deliver(t, h, mb.Address); code == http.StatusCreated {
			t.Fatalf("deliver = %d, want it refused: the stored script no longer matches its hash", code)
		}
		if got := stored(t, mb.ID); len(got) != 0 {
			t.Fatalf("stored %+v, want nothing", got)
		}
	})

	t.Run("Sieve off for the domain", func(t *testing.T) {
		h, mb := setup(t, "nosieve", []pipeline.FilterConfig{}, filters.HashSieveScript(script))
		if code := deliver(t, h, mb.Address); code != http.StatusCreated {
			t.Fatalf("deliver = %d, want 201", code)
		}
		if got := stored(t, mb.ID); len(got) != 1 || got[0].Folder != "INBOX" || got[0].IsFlagged {
			t.Fatalf("stored %+v, want one message in INBOX, unflagged", got)
		}
	})
}
