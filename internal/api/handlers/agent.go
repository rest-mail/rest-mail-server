package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	oapifly "github.com/antimatter-studios/go-oapifly"

	"github.com/rest-mail/rest-mail-server/internal/agentledger"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/api/respond"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Audit actions of the assistant's routes. A written entry is in the ledger; a
// refused one is only in the audit log.
const (
	ActionAgentLedgerAppend = "agent.ledger.append"
	ActionAgentTrustUpdate  = "agent.trust.update"
)

// How much one entry may carry in target or undo.
const (
	agentMaxPairs     = 16
	agentMaxPairRunes = 256
)

const agentLedgerDefaultLimit = 50

// agentCapability is one thing an assistant can be trusted with. Requires is the
// grant the action needs when it is taken: a level never widens a grant.
// Undoable says the action can be reversed, without which it cannot be automatic.
type agentCapability struct {
	Name     string
	Requires string
	Undoable bool
}

// agentCapabilities is every capability, in the order they are listed. Mail that
// has left the server cannot be called back, so sending cannot be undone.
var agentCapabilities = []agentCapability{
	{Name: models.AgentCapabilitySendMail, Requires: models.CapMailSend},
	{Name: models.AgentCapabilityFileNewsletters, Requires: models.CapMailWrite, Undoable: true},
}

func agentCapabilityNamed(name string) (agentCapability, bool) {
	for _, capability := range agentCapabilities {
		if capability.Name == name {
			return capability, true
		}
	}
	return agentCapability{}, false
}

// agentLevelRank orders the levels: each allows what the ones before it do.
var agentLevelRank = map[string]int{models.AgentLevelSuggest: 0, models.AgentLevelApprove: 1, models.AgentLevelAuto: 2}

// AgentHandler serves an account's assistant ledger and its per-capability trust.
// The assistant is a client of the API: an entry records an action, it never
// performs one, and the action itself goes through the routes that already exist,
// with their own checks.
type AgentHandler struct {
	db         *gorm.DB
	undoWindow time.Duration
	now        func() time.Time
}

// NewAgentHandler makes the handler. An undoWindow of zero gives no action a
// window, so no capability can be automatic.
func NewAgentHandler(db *gorm.DB, undoWindow time.Duration) *AgentHandler {
	return &AgentHandler{db: db, undoWindow: undoWindow, now: time.Now}
}

// RegisterRead mounts reading the ledger, for the principals that may read the
// account's mail.
func (h *AgentHandler) RegisterRead(rs endpoint.Routes) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/accounts/{id}/agent/ledger",
		OperationID: "listAgentLedger", Summary: "List the assistant's actions on an account", Tags: []string{"Assistant"},
		Description: "Newest first. A page is followed by the next with before_seq set to the cursor. Each entry carries the hash of the account's entry before it. Needs mail:read on the account.",
		Errors:      []int{http.StatusForbidden, http.StatusNotFound}, Security: handlerChecked(models.CapMailRead, mailRulesSchemes...)}, h.list)
}

// Register mounts writing to the ledger and changing the trust, for a person's
// session only.
func (h *AgentHandler) Register(rs endpoint.Routes, checks SessionRouteChecks) {
	tags := []string{"Assistant"}
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/accounts/{id}/agent/ledger",
		OperationID: "appendAgentLedgerEntry", Summary: "Record an assistant's action on an account", Tags: tags, Status: http.StatusCreated,
		Description: "Only the account's own signed-in session may write an entry; who else may is not decided yet (#1423). The actor comes from the session, never the body. An action may run only at the level the account's trust allows on its capability. An automatic action must say what reverses it, on a capability that can be undone, with an undo window configured. Set undoes to the seq of an entry to record that it was undone, within its window and once.",
		Errors:      []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict}, Security: handlerChecked(models.CapMailRead, "CookieAuth", "BearerAuth"),
		Middleware: []endpoint.Middleware{checks.Audit(ActionAgentLedgerAppend, middleware.AuditFailuresOnly())}}, h.append)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPatch, Path: "/api/v1/accounts/{id}/agent/trust",
		OperationID: "updateAgentTrust", Summary: "Change how far an assistant may go on each capability", Tags: tags,
		Description: "Every capability starts at suggest. Sets the levels the body names, all or none, and answers every capability's level; an empty body changes nothing. A capability that cannot be undone, or any capability while no undo window is configured, cannot be set to auto. A level never widens the account's grants. Needs mailbox:manage on the account and a person's session with a recent second factor.",
		Errors:      []int{http.StatusForbidden, http.StatusNotFound}, Security: handlerChecked(models.CapMailboxManage, "CookieAuth", "BearerAuth"),
		Middleware: []endpoint.Middleware{checks.Audit(ActionAgentTrustUpdate, middleware.AuditBodyFields(models.AgentCapabilitySendMail, models.AgentCapabilityFileNewsletters))}}, h.updateTrust)
}

// AgentLedgerEntry is one action, in the audit log's shape. Where the request
// came from stays in the audit log, which request_id leads to.
type AgentLedgerEntry struct {
	Seq          int64             `json:"seq" description:"This entry's place in the account's ledger, counting from 1"`
	OccurredAt   time.Time         `json:"occurred_at"`
	ActorKind    string            `json:"actor_kind" enums:"account,system,anonymous"`
	ActorID      *uint             `json:"actor_id"`
	ActorName    string            `json:"actor_name"`
	CredentialID *uint             `json:"credential_id" description:"The credential the entry was written with; null for a session"`
	Capability   string            `json:"capability" enums:"send_mail,file_newsletters"`
	Level        string            `json:"level" enums:"suggest,approve,auto" description:"suggest: only proposed; approve: a person approved it; auto: taken automatically"`
	Action       string            `json:"action"`
	Target       map[string]string `json:"target"`
	Undo         map[string]string `json:"undo" description:"What reverses the action; empty when nothing does"`
	UndoUntil    *time.Time        `json:"undo_until" description:"When the undo window closes; null when the entry has none"`
	Undoes       *int64            `json:"undoes" description:"The seq of the entry this one undoes"`
	UndoneBy     *int64            `json:"undone_by" description:"The seq of the entry that undid this one"`
	RequestID    string            `json:"request_id"`
	PrevHash     string            `json:"prev_hash"`
	Hash         string            `json:"hash"`
}

func agentLedgerEntryOf(row models.AgentLedgerEntry, undoneBy *int64) AgentLedgerEntry {
	var until *time.Time
	if row.UndoUntil != nil {
		t := row.UndoUntil.UTC()
		until = &t
	}
	return AgentLedgerEntry{
		Seq: row.Seq, OccurredAt: row.OccurredAt.UTC(),
		ActorKind: row.ActorKind, ActorID: row.ActorID, ActorName: row.ActorName, CredentialID: row.CredentialID,
		Capability: row.Capability, Level: row.Level, Action: row.Action, Target: row.Target, Undo: row.Undo,
		UndoUntil: until, Undoes: row.Undoes, UndoneBy: undoneBy,
		RequestID: row.RequestID, PrevHash: row.PrevHash, Hash: row.Hash,
	}
}

// listAgentLedgerRequest filters an account's ledger, newest first.
type listAgentLedgerRequest struct {
	ID         uint32 `path:"id" minimum:"1" example:"1"`
	Capability string `query:"capability" enums:",send_mail,file_newsletters" message:"must be send_mail or file_newsletters"`
	BeforeSeq  *int64 `query:"before_seq" minimum:"1" message:"must be a positive whole number"`
	Limit      *int   `query:"limit" minimum:"1" maximum:"200" message:"must be between 1 and 200"`
}

func (h *AgentHandler) list(r *http.Request, in listAgentLedgerRequest) (endpoint.List[AgentLedgerEntry], error) {
	var zero endpoint.List[AgentLedgerEntry]
	db := h.db.WithContext(r.Context())
	claims := middleware.GetClaims(r)
	if claims == nil {
		return zero, errAuthenticationRequired
	}
	mailbox, err := mayReach(db, claims, uint(in.ID), models.CapMailRead)
	if err != nil {
		return zero, agentAccountRefusal(err)
	}
	query := db.Where("mailbox_id = ?", mailbox.ID)
	if in.Capability != "" {
		query = query.Where("capability = ?", in.Capability)
	}
	if in.BeforeSeq != nil {
		query = query.Where("seq < ?", *in.BeforeSeq)
	}
	limit := agentLedgerDefaultLimit
	if in.Limit != nil {
		limit = *in.Limit
	}
	// One more than a page says whether there is another.
	rows := []models.AgentLedgerEntry{}
	if err := query.Order("seq DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return zero, fmt.Errorf("read agent ledger: %w", err)
	}
	page := &respond.Pagination{}
	if len(rows) > limit {
		rows = rows[:limit]
		page.HasMore = true
		page.Cursor = strconv.FormatInt(rows[limit-1].Seq, 10)
	}
	undoneBy, err := h.undoneBy(db, mailbox.ID, rows)
	if err != nil {
		return zero, err
	}
	out := make([]AgentLedgerEntry, len(rows))
	for i, row := range rows {
		var by *int64
		if seq, ok := undoneBy[row.Seq]; ok {
			by = &seq
		}
		out[i] = agentLedgerEntryOf(row, by)
	}
	return endpoint.List[AgentLedgerEntry]{Data: out, Pagination: page}, nil
}

// undoneBy finds which of rows have been undone, and by which entry.
func (h *AgentHandler) undoneBy(db *gorm.DB, mailboxID uint, rows []models.AgentLedgerEntry) (map[int64]int64, error) {
	seqs := make([]int64, len(rows))
	for i, row := range rows {
		seqs[i] = row.Seq
	}
	var undos []models.AgentLedgerEntry
	if err := db.Select("seq", "undoes").Where("mailbox_id = ? AND undoes IN ?", mailboxID, seqs).Find(&undos).Error; err != nil {
		return nil, fmt.Errorf("read agent ledger undos: %w", err)
	}
	out := make(map[int64]int64, len(undos))
	for _, undo := range undos {
		out[*undo.Undoes] = undo.Seq
	}
	return out, nil
}

// appendAgentLedgerRequest adds one entry to the account's ledger.
type appendAgentLedgerRequest struct {
	ID   uint32                `path:"id" minimum:"1" example:"1"`
	Body appendAgentLedgerBody `body:""`
}

// appendAgentLedgerBody is an action (capability, level and action), or that an
// earlier one was undone (undoes). Who acted is not here: it comes from the session.
type appendAgentLedgerBody struct {
	Capability string            `json:"capability,omitempty" enums:"send_mail,file_newsletters" message:"must be send_mail or file_newsletters"`
	Level      string            `json:"level,omitempty" enums:"suggest,approve,auto" message:"must be suggest, approve or auto"`
	Action     string            `json:"action,omitempty" pattern:"^[a-z][a-z0-9_.]*$" minLength:"1" maxLength:"64" message:"must be lower-case letters, digits, dots and underscores"`
	Target     map[string]string `json:"target,omitempty" description:"What the action acted on, as string pairs"`
	Undo       map[string]string `json:"undo,omitempty" description:"What reverses the action, as string pairs. An automatic action must have it."`
	Undoes     int64             `json:"undoes,omitempty" minimum:"1" description:"The seq of the entry this one undoes; nothing else may be sent with it"`
}

// AnyOf states the kinds of entry: an action a person suggested or approved, an
// automatic action, which must be on a capability that can be undone and say what
// reverses it, and an undo.
func (appendAgentLedgerBody) AnyOf() oapifly.AnyOfRule {
	return oapifly.AnyOfRule{Alternatives: []oapifly.Alternative{
		{Required: []string{"capability", "level", "action"}, Fields: map[string]string{"level": `enums:"suggest,approve"`}},
		{Required: []string{"capability", "level", "action", "undo"}, Fields: map[string]string{
			"level": `enums:"auto"`, "capability": `enums:"file_newsletters"`, "undo": `minProperties:"1"`,
		}, Message: "an automatic action must be on a capability that can be undone, and say what reverses it"},
		{Required: []string{"undoes"}},
	}}
}

func (h *AgentHandler) append(r *http.Request, in appendAgentLedgerRequest) (AgentLedgerEntry, error) {
	db := h.db.WithContext(r.Context())
	claims := middleware.GetClaims(r)
	if claims == nil {
		return AgentLedgerEntry{}, errAuthenticationRequired
	}
	mailbox, err := mayReach(db, claims, uint(in.ID), models.CapMailRead)
	if err != nil {
		return AgentLedgerEntry{}, agentAccountRefusal(err)
	}
	if !ownPersonSession(claims, mailbox.ID) {
		return AgentLedgerEntry{}, endpoint.Refuse(http.StatusForbidden, "own_session_required", "Only the account's own signed-in session may write to its ledger")
	}
	now := h.now()
	var entry models.AgentLedgerEntry
	err = db.Transaction(func(tx *gorm.DB) error {
		var err error
		if in.Body.Undoes != 0 {
			entry, err = h.undoEntry(tx, claims, mailbox, in.Body, now)
		} else {
			entry, err = h.actionEntry(tx, claims, mailbox, in.Body, now)
		}
		if err != nil {
			return err
		}
		entry.MailboxID = mailbox.ID
		entry.OccurredAt = now
		entry.ActorKind, entry.ActorName = models.AuditActorAccount, claims.Address
		actor := claims.AccountID
		entry.ActorID = &actor
		entry.RequestID = middleware.RequestEntry(r, ActionAgentLedgerAppend, http.StatusCreated).RequestID
		return agentledger.Append(tx, &entry)
	})
	if err != nil {
		return AgentLedgerEntry{}, agentAccountRefusal(err)
	}
	return agentLedgerEntryOf(entry, nil), nil
}

// ownPersonSession reports whether the request is a person's session on the
// account itself: not a credential, and not another account holding a grant.
func ownPersonSession(claims *auth.Claims, mailboxID uint) bool {
	return claims.AccountID == mailboxID && claims.CredentialID == 0 && claims.CredentialKind == "" && claims.MachineCredentialID == 0
}

// actionEntry is the entry for an action, once the rules allow it.
func (h *AgentHandler) actionEntry(tx *gorm.DB, claims *auth.Claims, mailbox *models.Mailbox, body appendAgentLedgerBody, now time.Time) (models.AgentLedgerEntry, error) {
	var zero models.AgentLedgerEntry
	capability, ok := agentCapabilityNamed(body.Capability)
	if !ok { // binding allows only the listed names
		return zero, endpoint.Invalid(map[string]string{"capability": "must be send_mail or file_newsletters"})
	}
	fields := map[string]string{}
	agentCheckPairs(fields, "target", body.Target)
	agentCheckPairs(fields, "undo", body.Undo)
	if body.Level == models.AgentLevelAuto {
		if why := h.whyNotAutomatic(capability); why != "" {
			fields["level"] = why
		} else if len(body.Undo) == 0 {
			fields["undo"] = "an automatic action must say what reverses it"
		}
	}
	if len(fields) > 0 {
		return zero, endpoint.Invalid(fields)
	}
	level, err := agentLevelOf(tx, mailbox.ID, capability.Name)
	if err != nil {
		return zero, err
	}
	if agentLevelRank[body.Level] > agentLevelRank[level] {
		return zero, endpoint.Refuse(http.StatusForbidden, "trust_level_too_low", "This capability's level on this account does not allow that")
	}
	entry := models.AgentLedgerEntry{Capability: capability.Name, Level: body.Level, Action: body.Action, Target: body.Target, Undo: body.Undo}
	if body.Level != models.AgentLevelSuggest {
		// A suggestion changes nothing; anything else is an action the grants must allow.
		if _, err := mayReach(tx, claims, mailbox.ID, capability.Requires); err != nil {
			return zero, err
		}
		if len(body.Undo) > 0 && capability.Undoable && h.undoWindow > 0 {
			until := now.Add(h.undoWindow)
			entry.UndoUntil = &until
		}
	}
	return entry, nil
}

// undoEntry is the entry saying an earlier one was undone, once the rules allow
// it. It takes the ledger's lock, so that two undos of one entry cannot both pass.
func (h *AgentHandler) undoEntry(tx *gorm.DB, claims *auth.Claims, mailbox *models.Mailbox, body appendAgentLedgerBody, now time.Time) (models.AgentLedgerEntry, error) {
	var zero models.AgentLedgerEntry
	if body.Capability != "" || body.Level != "" || body.Action != "" || len(body.Target) > 0 || len(body.Undo) > 0 {
		return zero, endpoint.Invalid(map[string]string{"undoes": "cannot be sent with anything else"})
	}
	if err := agentledger.Lock(tx, mailbox.ID); err != nil {
		return zero, err
	}
	var original models.AgentLedgerEntry
	err := tx.Where("mailbox_id = ? AND seq = ?", mailbox.ID, body.Undoes).Take(&original).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return zero, endpoint.Invalid(map[string]string{"undoes": "no such entry"})
	}
	if err != nil {
		return zero, err
	}
	switch {
	case original.Undoes != nil:
		return zero, endpoint.Invalid(map[string]string{"undoes": "an undo cannot be undone"})
	case original.Level == models.AgentLevelSuggest:
		return zero, endpoint.Invalid(map[string]string{"undoes": "a suggestion changed nothing"})
	}
	if capability, ok := agentCapabilityNamed(original.Capability); ok {
		if _, err := mayReach(tx, claims, mailbox.ID, capability.Requires); err != nil {
			return zero, err
		}
	}
	var undone int64
	if err := tx.Model(&models.AgentLedgerEntry{}).Where("mailbox_id = ? AND undoes = ?", mailbox.ID, original.Seq).Count(&undone).Error; err != nil {
		return zero, err
	}
	if undone > 0 {
		return zero, endpoint.Refuse(http.StatusConflict, "already_undone", "This entry has already been undone")
	}
	if original.UndoUntil == nil || now.After(*original.UndoUntil) {
		return zero, endpoint.Refuse(http.StatusConflict, "undo_window_closed", "This entry has no open undo window")
	}
	seq := original.Seq
	// Only a person's session writes an undo, so it is an approved action.
	return models.AgentLedgerEntry{Capability: original.Capability, Level: models.AgentLevelApprove, Action: "undo", Target: original.Target, Undoes: &seq}, nil
}

// whyNotAutomatic says why capability cannot be automatic, or nothing if it can:
// what cannot be undone cannot be automatic.
func (h *AgentHandler) whyNotAutomatic(capability agentCapability) string {
	if !capability.Undoable {
		return "this capability cannot be undone, so it cannot be automatic"
	}
	if h.undoWindow <= 0 {
		return "this server has no undo window, so nothing can be automatic"
	}
	return ""
}

// agentCheckPairs reports a target or undo that is too big or not storable.
func agentCheckPairs(fields map[string]string, field string, pairs map[string]string) {
	if len(pairs) > agentMaxPairs {
		fields[field] = fmt.Sprintf("must have at most %d entries", agentMaxPairs)
		return
	}
	for key, value := range pairs {
		for _, text := range []string{key, value} {
			if utf8.RuneCountInString(text) > agentMaxPairRunes || strings.ContainsRune(text, 0) {
				fields[field] = fmt.Sprintf("each key and value must be at most %d characters, with no NUL", agentMaxPairRunes)
				return
			}
		}
	}
}

// agentLevelOf is the account's level on capability: suggest until it is changed.
func agentLevelOf(db *gorm.DB, mailboxID uint, capability string) (string, error) {
	var rows []models.AgentTrust
	if err := db.Where("mailbox_id = ? AND capability = ?", mailboxID, capability).Limit(1).Find(&rows).Error; err != nil {
		return "", fmt.Errorf("read agent trust: %w", err)
	}
	if len(rows) == 0 {
		return models.AgentLevelSuggest, nil
	}
	return rows[0].Level, nil
}

// AgentTrust is how far an assistant may go on each capability of an account.
type AgentTrust struct {
	UndoWindowSeconds int64                  `json:"undo_window_seconds" description:"How long an action can be undone; zero means no action has a window, so nothing can be automatic"`
	Capabilities      []AgentCapabilityLevel `json:"capabilities"`
}

// AgentCapabilityLevel is one capability's level.
type AgentCapabilityLevel struct {
	Capability     string `json:"capability" enums:"send_mail,file_newsletters"`
	Level          string `json:"level" enums:"suggest,approve,auto"`
	CanBeAutomatic bool   `json:"can_be_automatic" description:"Whether this capability can be set to auto: it can be undone, and an undo window is configured"`
}

// updateAgentTrustRequest changes the levels its body names.
type updateAgentTrustRequest struct {
	ID   uint32               `path:"id" minimum:"1" example:"1"`
	Body updateAgentTrustBody `body:""`
}

// updateAgentTrustBody has one field per capability, so that an unknown one is
// refused like any unknown field.
type updateAgentTrustBody struct {
	SendMail        string `json:"send_mail,omitempty" enums:"suggest,approve" message:"must be suggest or approve: this capability cannot be undone, so it cannot be automatic"`
	FileNewsletters string `json:"file_newsletters,omitempty" enums:"suggest,approve,auto" message:"must be suggest, approve or auto"`
}

func (b updateAgentTrustBody) levels() map[string]string {
	return map[string]string{
		models.AgentCapabilitySendMail:        b.SendMail,
		models.AgentCapabilityFileNewsletters: b.FileNewsletters,
	}
}

func (h *AgentHandler) updateTrust(r *http.Request, in updateAgentTrustRequest) (AgentTrust, error) {
	db := h.db.WithContext(r.Context())
	claims := middleware.GetClaims(r)
	if claims == nil {
		return AgentTrust{}, errAuthenticationRequired
	}
	mailbox, err := mayReach(db, claims, uint(in.ID), models.CapMailboxManage)
	if err != nil {
		return AgentTrust{}, agentAccountRefusal(err)
	}
	if err := RequireFreshTwoFactor(r); err != nil {
		return AgentTrust{}, err
	}
	levels := in.Body.levels()
	fields := map[string]string{}
	for _, capability := range agentCapabilities {
		if levels[capability.Name] == models.AgentLevelAuto {
			if why := h.whyNotAutomatic(capability); why != "" {
				fields[capability.Name] = why
			}
		}
	}
	if len(fields) > 0 {
		return AgentTrust{}, endpoint.Invalid(fields)
	}
	var trust AgentTrust
	err = db.Transaction(func(tx *gorm.DB) error {
		for _, capability := range agentCapabilities {
			level := levels[capability.Name]
			if level == "" {
				continue
			}
			row := models.AgentTrust{MailboxID: mailbox.ID, Capability: capability.Name, Level: level, UpdatedAt: h.now()}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "mailbox_id"}, {Name: "capability"}},
				DoUpdates: clause.AssignmentColumns([]string{"level", "updated_at"}),
			}).Omit("Mailbox").Create(&row).Error; err != nil {
				return fmt.Errorf("save agent trust: %w", err)
			}
		}
		var err error
		trust, err = h.trustOf(tx, mailbox.ID)
		return err
	})
	if err != nil {
		return AgentTrust{}, err
	}
	return trust, nil
}

func (h *AgentHandler) trustOf(db *gorm.DB, mailboxID uint) (AgentTrust, error) {
	trust := AgentTrust{UndoWindowSeconds: int64(h.undoWindow / time.Second), Capabilities: make([]AgentCapabilityLevel, len(agentCapabilities))}
	for i, capability := range agentCapabilities {
		level, err := agentLevelOf(db, mailboxID, capability.Name)
		if err != nil {
			return AgentTrust{}, err
		}
		trust.Capabilities[i] = AgentCapabilityLevel{Capability: capability.Name, Level: level, CanBeAutomatic: h.whyNotAutomatic(capability) == ""}
	}
	return trust, nil
}

// agentAccountRefusal answers an account the caller may not know of as the
// routes on an account do.
func agentAccountRefusal(err error) error {
	if errors.Is(err, errNoAccess) {
		return errAccountNotFound
	}
	return err
}
