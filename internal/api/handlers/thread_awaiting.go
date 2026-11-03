package handlers

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/respond"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"gorm.io/gorm"
)

// listThreadsRequest asks for an account's threads in a state; awaiting is the only state.
type listThreadsRequest struct {
	ID     uint32 `path:"id" minimum:"1" example:"1"`
	State  string `query:"state,required" enums:"awaiting" message:"must be awaiting" example:"awaiting"`
	Limit  *int   `query:"limit" description:"1 to 100; 50 otherwise"`
	Cursor string `query:"cursor" description:"The cursor of the page before; one that does not decode starts from the newest"`
}

// AwaitingThread is a thread whose last message came from this account.
type AwaitingThread struct {
	ThreadID      string    `json:"thread_id"`
	AwaitingSince time.Time `json:"awaiting_since" description:"When the thread's last message came from this account"`
	LastMessage   Message   `json:"last_message"`
}

// countedMessageSQL says whether a row is mail that was really sent or received. A draft is
// not sent, wherever it is filed, and a deleted or trashed message no longer counts in either
// direction. A scheduled send has no message row until it is sent.
func countedMessageSQL(table string) string {
	return table + ".is_deleted = false AND " + table + ".imap_deleted = false AND " + table + ".is_draft = false AND " + table + ".folder NOT IN ('Trash', 'Drafts')"
}

// awaitingMessageSQL finds the row that makes a thread wait: a counted message from this
// account with no counted message after it in its thread. The order is the mail list's,
// received_at with the id breaking ties, so exactly one row can answer for a thread.
//
// A reply also ends the wait when it landed under another thread_id. The send path files a
// reply under its parent's Message-ID, not the thread's root, so a conversation's third
// message starts a new thread_id and the answer to it may start another (see the
// conversation test). What a message replies to is the rule contact reply times use:
// In-Reply-To, or the last of References when that is absent.
var awaitingMessageSQL = `messages.thread_id <> ''
	AND ` + countedMessageSQL("messages") + `
	AND normalize(lower(btrim(COALESCE(substring(messages.sender from '<([^<>]*)>\s*$'), messages.sender))), NFC) IN (?)
	AND NOT EXISTS (SELECT 1 FROM messages later
		WHERE later.mailbox_id = messages.mailbox_id AND later.thread_id = messages.thread_id
		AND ` + countedMessageSQL("later") + `
		AND (later.received_at, later.id) > (messages.received_at, messages.id))
	AND (btrim(messages.message_id, '<> ') = '' OR NOT EXISTS (SELECT 1 FROM messages reply
		WHERE reply.mailbox_id = messages.mailbox_id AND reply.id <> messages.id
		AND ` + models.MessageReplyParentSQL("reply.") + ` = left(btrim(messages.message_id, '<> '), 255)
		AND ` + countedMessageSQL("reply") + `))`

// ownSenderSpellings is how a message's sender may spell this mailbox's address. It is what
// FindMailboxByAddress resolves to the mailbox, and so what sending as the account accepts:
// any case, NFC-equivalent local parts, and the domain in either form. An alias is not among
// them: it only routes mail in, and neither send path accepts it as a From address.
func ownSenderSpellings(address string) []string {
	canonical, err := rmail.CanonicalMailboxAddress(address)
	if err != nil {
		return []string{strings.ToLower(strings.TrimSpace(address))}
	}
	spellings := []string{canonical}
	local, domain, _ := strings.Cut(canonical, "@")
	if unicode, err := rmail.DomainUnicode(domain); err == nil && unicode != domain {
		spellings = append(spellings, local+"@"+strings.ToLower(unicode))
	}
	return spellings
}

func mailboxSenderSpellings(db *gorm.DB, mailboxID uint) ([]string, error) {
	var address string
	if err := db.Model(&models.Mailbox{}).Select("address").Where("id = ?", mailboxID).Take(&address).Error; err != nil {
		return nil, fmt.Errorf("load the account's address: %w", err)
	}
	return ownSenderSpellings(address), nil
}

// attachAwaitingSince sets awaiting_since on the rows whose thread is waiting. The rows must
// all be in mailboxID.
func attachAwaitingSince(db *gorm.DB, mailboxID uint, rows []models.Message) error {
	threads := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.ThreadID != "" {
			threads = append(threads, row.ThreadID)
		}
	}
	if len(threads) == 0 {
		return nil
	}
	spellings, err := mailboxSenderSpellings(db, mailboxID)
	if err != nil {
		return err
	}
	var waiting []struct {
		ThreadID   string
		ReceivedAt time.Time
	}
	if err := db.Model(&models.Message{}).Select("messages.thread_id", "messages.received_at").
		Where("messages.mailbox_id = ? AND messages.thread_id IN (?)", mailboxID, threads).
		Where(awaitingMessageSQL, spellings).Scan(&waiting).Error; err != nil {
		return fmt.Errorf("derive waiting threads: %w", err)
	}
	since := make(map[string]time.Time, len(waiting))
	for _, thread := range waiting {
		since[thread.ThreadID] = thread.ReceivedAt
	}
	for i := range rows {
		if at, ok := since[rows[i].ThreadID]; ok {
			rows[i].AwaitingSince = &at
		}
	}
	return nil
}

// listThreads answers the account's threads in a state, newest first on the same cursor as
// the mail lists. It leaves visibleThreads out on purpose: snoozing hides a thread from folder
// listings only, so a snoozed thread stays here with its deadline, as it does in search.
func (h *MessageHandler) listThreads(r *http.Request, in listThreadsRequest) (endpoint.List[AwaitingThread], error) {
	h = h.forRequest(r)
	var zero endpoint.List[AwaitingThread]
	mailboxID, err := h.accountMailbox(r, in.ID, models.CapMailRead)
	if err != nil {
		return zero, err
	}
	spellings, err := mailboxSenderSpellings(h.db, mailboxID)
	if err != nil {
		return zero, err
	}

	limit := 50
	if in.Limit != nil && *in.Limit > 0 && *in.Limit <= 100 {
		limit = *in.Limit
	}
	query := h.db.Where("messages.mailbox_id = ?", mailboxID).Where(awaitingMessageSQL, spellings).
		Select(messageThreadFields).Order("messages.received_at DESC, messages.id DESC").Limit(limit + 1)
	if after, ok := decodeMessageCursor(in.Cursor); ok {
		query = query.Where("(messages.received_at, messages.id) < (?, ?)", after.ReceivedAt, after.ID)
	}
	var rows []models.Message
	if err := query.Find(&rows).Error; err != nil {
		return zero, fmt.Errorf("list waiting threads: %w", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	var nextCursor string
	if hasMore {
		last := rows[len(rows)-1]
		encoded, _ := json.Marshal(messageCursor{ReceivedAt: last.ReceivedAt, ID: last.ID})
		nextCursor = base64.StdEncoding.EncodeToString(encoded)
	}

	threads := make([]AwaitingThread, len(rows))
	for i := range rows {
		since := rows[i].ReceivedAt
		rows[i].AwaitingSince = &since
		threads[i] = AwaitingThread{ThreadID: rows[i].ThreadID, AwaitingSince: since, LastMessage: messageOf(rows[i])}
	}
	// No total: counting walks every message the account sent, about 4 seconds on a million
	// message mailbox, where a page takes about 60 milliseconds.
	return endpoint.List[AwaitingThread]{Data: threads, Pagination: &respond.Pagination{
		Cursor: nextCursor, HasMore: hasMore,
	}}, nil
}
