package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/auth"
	"gorm.io/gorm"
)

// SSEEvent represents a server-sent event with a type and data payload.
type SSEEvent struct {
	Type string      `json:"type"` // "new_message", "folder_update"
	Data interface{} `json:"data"`
}

// numberedEvent wraps an SSEEvent with a monotonic ID for replay support.
type numberedEvent struct {
	ID    uint64
	Event SSEEvent
}

const ringSize = 64

// DefaultMaxSSEStreamsPerAccount bounds the number of concurrent SSE streams a
// single account may hold open. Each stream costs a goroutine, a file
// descriptor, and a broker subscription channel, so without a cap one account
// (or a stolen token) could open unbounded streams and exhaust server resources
// (#204). The default is generous for legitimate multi-tab / multi-device use
// while still bounding abuse.
const DefaultMaxSSEStreamsPerAccount = 8

// mailboxState tracks the event counter and ring buffer for one mailbox.
type mailboxState struct {
	counter atomic.Uint64
	mu      sync.Mutex
	ring    [ringSize]numberedEvent
	ringPos int
}

// SSEBroker is an in-memory pub/sub broker that fans out SSEEvents
// to all subscriber channels registered for a given mailbox ID.
type SSEBroker struct {
	mu          sync.RWMutex
	subscribers map[uint]map[chan numberedEvent]struct{}
	states      sync.Map // uint (mailboxID) -> *mailboxState

	// maxStreams caps concurrent SSE streams per mailbox (#204). active tracks
	// the current count per mailbox; both are guarded by activeMu, kept separate
	// from mu so the acquire/release accounting never contends with event fan-out.
	maxStreams int
	activeMu   sync.Mutex
	active     map[uint]int
}

// NewSSEBroker creates a new SSEBroker ready for use, with the default
// per-account concurrent-stream cap.
func NewSSEBroker() *SSEBroker {
	return newSSEBroker(DefaultMaxSSEStreamsPerAccount)
}

// newSSEBroker builds a broker with an explicit per-account stream cap. A
// non-positive cap falls back to the default so the broker is always bounded.
func newSSEBroker(maxStreams int) *SSEBroker {
	if maxStreams <= 0 {
		maxStreams = DefaultMaxSSEStreamsPerAccount
	}
	return &SSEBroker{
		subscribers: make(map[uint]map[chan numberedEvent]struct{}),
		maxStreams:  maxStreams,
		active:      make(map[uint]int),
	}
}

// AcquireStream reserves one concurrent-stream slot for mailboxID. It returns
// false without reserving when the per-account cap is already reached. Every
// successful AcquireStream MUST be paired with exactly one ReleaseStream (on
// disconnect) so the slot is returned.
func (b *SSEBroker) AcquireStream(mailboxID uint) bool {
	b.activeMu.Lock()
	defer b.activeMu.Unlock()
	if b.active[mailboxID] >= b.maxStreams {
		return false
	}
	b.active[mailboxID]++
	return true
}

// ReleaseStream returns a slot previously taken by AcquireStream. It is safe to
// call once per successful acquire; the per-mailbox entry is dropped when it
// reaches zero so the map cannot grow without bound.
func (b *SSEBroker) ReleaseStream(mailboxID uint) {
	b.activeMu.Lock()
	defer b.activeMu.Unlock()
	if b.active[mailboxID] <= 1 {
		delete(b.active, mailboxID)
		return
	}
	b.active[mailboxID]--
}

// Subscribe creates a buffered channel for the given mailbox ID and registers
// it in the subscribers map. The caller must eventually call Unsubscribe.
func (b *SSEBroker) Subscribe(mailboxID uint) chan numberedEvent {
	ch := make(chan numberedEvent, 16)

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.subscribers[mailboxID] == nil {
		b.subscribers[mailboxID] = make(map[chan numberedEvent]struct{})
	}
	b.subscribers[mailboxID][ch] = struct{}{}

	return ch
}

// SubscribeWithReplay creates a subscription and replays events after lastEventID.
func (b *SSEBroker) SubscribeWithReplay(mailboxID uint, lastEventID uint64) chan numberedEvent {
	ch := b.Subscribe(mailboxID)

	if lastEventID == 0 {
		return ch
	}

	val, ok := b.states.Load(mailboxID)
	if !ok {
		return ch
	}
	state := val.(*mailboxState)

	state.mu.Lock()
	defer state.mu.Unlock()

	// Collect events to replay from the ring buffer
	var replay []numberedEvent
	for i := 0; i < ringSize; i++ {
		evt := state.ring[i]
		if evt.ID > lastEventID {
			replay = append(replay, evt)
		}
	}

	// Sort by ID (they may not be in order in the ring)
	for i := 0; i < len(replay); i++ {
		for j := i + 1; j < len(replay); j++ {
			if replay[j].ID < replay[i].ID {
				replay[i], replay[j] = replay[j], replay[i]
			}
		}
	}

	// Send replayed events (non-blocking)
	for _, evt := range replay {
		select {
		case ch <- evt:
		default:
		}
	}

	return ch
}

// Unsubscribe removes the channel from the subscribers map and closes it.
func (b *SSEBroker) Unsubscribe(mailboxID uint, ch chan numberedEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if subs, ok := b.subscribers[mailboxID]; ok {
		delete(subs, ch)
		if len(subs) == 0 {
			delete(b.subscribers, mailboxID)
		}
	}
	close(ch)
}

// Publish sends an event to all subscribers for the given mailbox ID.
// The send is non-blocking: if a subscriber's channel buffer is full,
// the event is dropped for that subscriber.
func (b *SSEBroker) Publish(mailboxID uint, event SSEEvent) {
	// Get or create state for this mailbox
	val, _ := b.states.LoadOrStore(mailboxID, &mailboxState{})
	state := val.(*mailboxState)

	id := state.counter.Add(1)
	numbered := numberedEvent{ID: id, Event: event}

	// Store in ring buffer
	state.mu.Lock()
	state.ring[state.ringPos%ringSize] = numbered
	state.ringPos++
	state.mu.Unlock()

	// Fan out to subscribers
	b.mu.RLock()
	defer b.mu.RUnlock()

	for ch := range b.subscribers[mailboxID] {
		select {
		case ch <- numbered:
		default:
			// Drop event if subscriber is not keeping up
		}
	}
}

// EventHandler serves Server-Sent Events for real-time push notifications.
type EventHandler struct {
	db         *gorm.DB
	broker     *SSEBroker
	jwtService *auth.JWTService
}

// NewEventHandler creates a new EventHandler.
func NewEventHandler(db *gorm.DB, broker *SSEBroker, jwtService *auth.JWTService) *EventHandler {
	return &EventHandler{
		db:         db,
		broker:     broker,
		jwtService: jwtService,
	}
}

// Events handles GET /api/v1/accounts/{id}/events as a Server-Sent Events stream.
// Authentication accepts EITHER the restmail_access httpOnly cookie (browser
// EventSource, which attaches it automatically) or an Authorization: Bearer
// header (programmatic clients) — the same dual transport as JWTMiddleware. This
// removes the old need for a header-capable fetch() SSE shim: a browser can now
// use the native EventSource, whose cookie rides along, with no token in JS or
// in the URL.
func (h *EventHandler) Events(w http.ResponseWriter, r *http.Request) {
	// 1. Auth via the access cookie or Authorization: Bearer header.
	token, errMsg := middleware.AccessTokenFromRequest(r)
	if errMsg != "" {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", errMsg)
		return
	}

	claims, err := h.jwtService.ValidateAccessToken(token)
	if err != nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Invalid or expired token")
		return
	}

	// 2. Parse account ID from URL
	accountID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	// 3. Resolve mailbox ID (same logic as MessageHandler.resolveAccountMailbox)
	mailboxID, err := h.resolveAccountMailbox(uint(accountID), claims.WebmailAccountID)
	if err != nil {
		respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}

	// 3a. Per-account concurrency cap (#204): bound the number of simultaneous
	// SSE streams for this mailbox so one account (or a stolen token) cannot open
	// unbounded streams and exhaust goroutines/FDs. Reserve a slot now that the
	// caller is authorized for this mailbox; release it on disconnect. Over the
	// cap we reject with 503 rather than accept without bound.
	if !h.broker.AcquireStream(mailboxID) {
		respond.Error(w, http.StatusServiceUnavailable, "too_many_streams", "Too many concurrent event streams for this account")
		return
	}
	defer h.broker.ReleaseStream(mailboxID)

	// 4. Ensure the response writer supports flushing
	flusher, ok := w.(http.Flusher)
	if !ok {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Streaming not supported")
		return
	}

	// 5. Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// 6. Subscribe to broker for this mailbox (with replay if reconnecting)
	var ch chan numberedEvent
	if lastID := r.Header.Get("Last-Event-ID"); lastID != "" {
		if id, err := strconv.ParseUint(lastID, 10, 64); err == nil {
			ch = h.broker.SubscribeWithReplay(mailboxID, id)
		}
	}
	if ch == nil {
		ch = h.broker.Subscribe(mailboxID)
	}
	defer h.broker.Unsubscribe(mailboxID, ch)

	// 7. Flush immediately so the client knows the connection is open
	flusher.Flush()

	// 8. Event loop
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case event := <-ch:
			data, err := json.Marshal(event.Event.Data)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.ID, event.Event.Type, data)
			flusher.Flush()

		case <-ticker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()

		case <-r.Context().Done():
			return
		}
	}
}

// resolveAccountMailbox looks up the mailbox ID for an account. It first checks
// if the account is a WebmailAccount owned by the caller, then falls back to
// checking LinkedAccounts.
func (h *EventHandler) resolveAccountMailbox(accountID, webmailAccountID uint) (uint, error) {
	return resolveAccountMailbox(h.db, accountID, webmailAccountID)
}
