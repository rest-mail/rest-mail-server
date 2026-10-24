package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/restmail/restmail/internal/db/models"
	rmail "github.com/restmail/restmail/internal/mail"
	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

// withAccountSieve is the inbound pipeline a local delivery to mailboxID runs: cfg, with the
// mailbox's own Sieve script in its sieve step (#787). The script is stored by PUT
// /accounts/{id}/sieve with the hash it was validated with, which the filter checks again, so
// a script changed behind the API fails closed (#250).
//
// A sieve step with no script of its own takes the mailbox's. One that carries a domain-level
// script keeps it, and the mailbox's runs in a second sieve step right after it. A pipeline
// with no sieve step runs no account's script: Sieve is off for the domain. cfg itself is
// never changed; it may be the domain's shared default.
func withAccountSieve(db *gorm.DB, cfg *pipeline.PipelineConfig, mailboxID uint) (*pipeline.PipelineConfig, error) {
	stored, ok, err := activeSieveScript(db, mailboxID)
	if err != nil || !ok {
		return cfg, err
	}
	return withSieveScript(cfg, mailboxID, stored.Script, stored.ScriptHash)
}

// activeSieveScript is mailboxID's stored script, when it has an active one that is not empty.
func activeSieveScript(db *gorm.DB, mailboxID uint) (models.SieveScript, bool, error) {
	var stored models.SieveScript
	err := db.Where("mailbox_id = ? AND active = ?", mailboxID, true).First(&stored).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && strings.TrimSpace(stored.Script) == "") {
		return models.SieveScript{}, false, nil
	}
	if err != nil {
		return models.SieveScript{}, false, fmt.Errorf("read the account's Sieve script: %w", err)
	}
	return stored, true, nil
}

// accountSieveOnly is the pipeline that runs mailboxID's own Sieve script by itself, for a
// delivery whose domain pipeline cfg has already run once for all of its recipients
// (RESTMAIL, #787). It is nil when the mailbox has no active script, or cfg has no enabled
// sieve step, which is how an administrator turns Sieve off for the domain.
func accountSieveOnly(db *gorm.DB, cfg *pipeline.PipelineConfig, mailboxID uint) (*pipeline.PipelineConfig, error) {
	if cfg == nil || !hasSieveStep(cfg) {
		return nil, nil
	}
	stored, ok, err := activeSieveScript(db, mailboxID)
	if err != nil || !ok {
		return nil, err
	}
	only := &pipeline.PipelineConfig{
		ID: cfg.ID, DomainID: cfg.DomainID, Direction: cfg.Direction, Active: true,
		Filters: []pipeline.FilterConfig{{Name: "sieve", Type: pipeline.FilterTypeTransform, Enabled: true}},
	}
	return withSieveScript(only, mailboxID, stored.Script, stored.ScriptHash)
}

func hasSieveStep(cfg *pipeline.PipelineConfig) bool {
	for _, step := range cfg.Filters {
		if step.Name == "sieve" && step.Enabled {
			return true
		}
	}
	return false
}

// onceOnlyActions are the metadata a Sieve run leaves for actions a delivery carries out
// once: its redirects, its notification and its vacation reply. A recipient's copy of a
// message the domain's pipeline already ran on drops them, so the recipient's own script
// does not repeat them; the folder and the flags stay, for its script to build on.
var onceOnlyActions = []string{
	"redirect_to", "redirect_suppress_keep", "notify_method", "notify_message",
	"vacation_days", "vacation_reply_body", "vacation_reply_from", "vacation_reply_mime",
	"vacation_reply_subject", "vacation_reply_to",
}

// recipientCopy is email as one of its recipients' own scripts sees it: a copy whose
// envelope names that recipient alone, without the once-only actions of the run before.
func recipientCopy(email *pipeline.EmailJSON, rcpt string) (*pipeline.EmailJSON, error) {
	raw, err := json.Marshal(email)
	if err != nil {
		return nil, err
	}
	var out pipeline.EmailJSON
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	out.Envelope.RcptTo = []string{rcpt}
	for _, key := range onceOnlyActions {
		delete(out.Metadata, key)
	}
	return &out, nil
}

// withSieveScript is cfg with script, the mailbox's, in its sieve step, as withAccountSieve
// describes.
func withSieveScript(cfg *pipeline.PipelineConfig, mailboxID uint, script, hash string) (*pipeline.PipelineConfig, error) {
	config, err := json.Marshal(map[string]any{"script": script, "script_hash": hash, "mailbox_id": mailboxID})
	if err != nil {
		return nil, err
	}

	last := -1
	for i, step := range cfg.Filters {
		if step.Name != "sieve" || !step.Enabled {
			continue
		}
		var own struct {
			Script string `json:"script"`
		}
		if len(step.Config) > 0 {
			if err := json.Unmarshal(step.Config, &own); err != nil {
				return nil, fmt.Errorf("the pipeline's sieve step %d: %w", i, err)
			}
		}
		if own.Script == "" {
			out := *cfg
			out.Filters = append([]pipeline.FilterConfig(nil), cfg.Filters...)
			out.Filters[i].Config = config
			return &out, nil
		}
		last = i
	}
	if last < 0 {
		return cfg, nil
	}
	out := *cfg
	out.Filters = make([]pipeline.FilterConfig, 0, len(cfg.Filters)+1)
	out.Filters = append(out.Filters, cfg.Filters[:last+1]...)
	out.Filters = append(out.Filters, pipeline.FilterConfig{
		Name: "sieve", Type: pipeline.FilterTypeTransform, Enabled: true, Config: config,
	})
	out.Filters = append(out.Filters, cfg.Filters[last+1:]...)
	return &out, nil
}

// sieveFolder is the folder a Sieve fileinto chose for the message (RFC 5228 §4.1), INBOX
// when none did. A folder that is not there is made by delivering into it, which is what
// fileinto :create asks for (RFC 5490 §3.2) and what §4.1 allows without it. A name the
// folder rules refuse, or that names INBOX in another case, is delivered to INBOX, logged.
func sieveFolder(email *pipeline.EmailJSON, mailbox string) string {
	if email == nil || email.Metadata == nil {
		return "INBOX"
	}
	folder := email.Metadata["deliver_to_folder"]
	if folder == "" {
		return "INBOX"
	}
	if problem := rmail.FolderNameProblem(folder); problem != "" {
		slog.Warn("deliverToLocal: sieve fileinto names a folder that cannot be made; delivering to INBOX",
			"mailbox", mailbox, "folder", folder, "problem", problem)
		return "INBOX"
	}
	return rmail.CanonicalFolder(folder)
}

// sieveFlags are the system flags a Sieve script's imap4flags actions left on the message
// (RFC 5232), as the message's own fields: \Seen, \Flagged and \Draft. Keywords and
// \Answered have no field to go in. \Deleted is left too: a deleted message is gone from its
// folder here, where IMAP's \Deleted only marks one for a later EXPUNGE, so a script that
// marks one would otherwise hide it.
type sieveFlags struct {
	seen, flagged, draft bool
}

func sieveFlagsOf(email *pipeline.EmailJSON) sieveFlags {
	var flags sieveFlags
	if email == nil || email.Metadata == nil {
		return flags
	}
	for _, flag := range strings.Fields(email.Metadata["imap_flags"]) {
		switch strings.ToLower(flag) {
		case `\seen`:
			flags.seen = true
		case `\flagged`:
			flags.flagged = true
		case `\draft`:
			flags.draft = true
		}
	}
	return flags
}
