package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
)

// ScheduledMessage is a message the account asked to be sent later, as the list of those
// still waiting answers it. The stored request it will send is never written.
type ScheduledMessage struct {
	ID            uint            `json:"id"`
	AccountID     uint            `json:"account_id"`
	From          string          `json:"from"`
	To            json.RawMessage `json:"to"`
	Subject       string          `json:"subject"`
	SendAt        time.Time       `json:"scheduled_at"`
	Status        string          `json:"status" enums:"pending,sending,sent,failed,cancelled"`
	Attempts      int             `json:"attempts"`
	SentMessageID *uint           `json:"sent_message_id,omitempty"`
	LastError     string          `json:"last_error,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

func scheduledMessageOf(m models.ScheduledSend) ScheduledMessage {
	return ScheduledMessage{
		ID:            m.ID,
		AccountID:     m.AccountID,
		From:          m.From,
		To:            storedList(m.To),
		Subject:       m.Subject,
		SendAt:        m.SendAt,
		Status:        m.Status,
		Attempts:      m.Attempts,
		SentMessageID: m.SentMessageID,
		LastError:     m.LastError,
		CreatedAt:     m.CreatedAt,
		UpdatedAt:     m.UpdatedAt,
	}
}

// CalendarEventSummary is an invitation the account has received, with how many versions
// of it arrived and whether it was cancelled.
type CalendarEventSummary struct {
	UID         string `json:"uid"`
	Method      string `json:"method"`
	Status      string `json:"status"`
	Summary     string `json:"summary"`
	Sequence    int    `json:"sequence"`
	IsCancelled bool   `json:"is_cancelled"`
	MessageID   *uint  `json:"message_id,omitempty"`
	Versions    int    `json:"versions"`
}

// SendDiscarded is the answer to a send the outbound pipeline discarded: nothing is sent and
// nothing kept.
type SendDiscarded struct {
	Status string `json:"status" enums:"discarded"`
}

// sendOutcomes are the answers a send gives, each with its own status: the sent message (201),
// the message scheduled for later (202), or the pipeline's discard (200).
var sendOutcomes = []oapifly.EndpointResponse{
	{Status: http.StatusCreated, Description: "Sent; the copy kept in Sent", Type: oapifly.TypeOf[endpoint.Data[Message]]()},
	{Status: http.StatusAccepted, Description: "Accepted and scheduled for the time in scheduled_at", Type: oapifly.TypeOf[endpoint.Data[ScheduledMessage]]()},
	{Status: http.StatusOK, Description: "Discarded by the outbound pipeline", Type: oapifly.TypeOf[endpoint.Data[SendDiscarded]]()},
}

// sentOrDiscarded are the answers of a send that cannot be scheduled: a draft's, a forward's.
var sentOrDiscarded = []oapifly.EndpointResponse{sendOutcomes[0], sendOutcomes[2]}

// ForwardRefusalResponse covers binding validation and a send's quota/policy refusal.
// Their code and message are always present; only validation has field details.
type ForwardRefusalResponse struct {
	Error ForwardRefusalBody `json:"error"`
}
type ForwardRefusalBody struct {
	Code    string                     `json:"code"`
	Message string                     `json:"message"`
	Details *respond.ValidationDetails `json:"details,omitempty"`
}

// sendRefusals are what a send refuses with: a field missing or invalid (400), no session
// (401), an address the session may not send as or the outbound pipeline's reject or
// quarantine (403), the sender domain's policy or a bad scheduled_at (422), the account's send
// rate (429, with Retry-After) and a deferral (503).
var sendRefusals = []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
	http.StatusTooManyRequests, http.StatusServiceUnavailable}

// CalendarReplied is the answer to replying to an invitation.
type CalendarReplied struct {
	Response string `json:"response" enums:"ACCEPTED,DECLINED,TENTATIVE"`
	Status   string `json:"status" enums:"sent"`
}

// scheduledPath names a scheduled message by its id.
type scheduledPath struct {
	ID uint32 `path:"id" minimum:"1" example:"1"`
}

// calendarReplyRequest replies to the invitation in a message. response is ACCEPTED,
// DECLINED or TENTATIVE in any case; from is the address the reply is sent from. Both are
// checked in the handler, after the message is found, as they always were.
type calendarReplyRequest struct {
	ID   uint32            `path:"id" minimum:"1" example:"1"`
	Body calendarReplyBody `body:""`
}

type calendarReplyBody struct {
	Response string `json:"response,omitempty"`
	From     string `json:"from,omitempty"`
}

// saveDraftBody is a new draft; every field is optional. Without a from, the draft is kept
// in the caller's own mailbox.
type saveDraftBody struct {
	From       string   `json:"from,omitempty"`
	To         []string `json:"to,omitempty"`
	Cc         []string `json:"cc,omitempty"`
	Subject    string   `json:"subject,omitempty"`
	BodyText   string   `json:"body_text,omitempty"`
	BodyHTML   string   `json:"body_html,omitempty"`
	InReplyTo  string   `json:"in_reply_to,omitempty"`
	References string   `json:"references,omitempty"`
}

// updateDraftRequest changes what is given. in_reply_to and references are accepted and
// left as the draft was saved: the mobile app sends a reply's draft whole.
type updateDraftRequest struct {
	ID   uint32          `path:"id" minimum:"1" example:"1"`
	Body updateDraftBody `body:""`
}

type updateDraftBody struct {
	From       *string  `json:"from,omitempty"`
	To         []string `json:"to,omitempty"`
	Cc         []string `json:"cc,omitempty"`
	Subject    *string  `json:"subject,omitempty"`
	BodyText   *string  `json:"body_text,omitempty"`
	BodyHTML   *string  `json:"body_html,omitempty"`
	InReplyTo  *string  `json:"in_reply_to,omitempty"`
	References *string  `json:"references,omitempty"`
}

// RegisterCompose mounts the drafts, the scheduled sends, the calendar invitations and the
// replies to them.
func (h *MessageHandler) RegisterCompose(rs endpoint.Routes) {
	reads := handlerChecked(models.CapMailRead, folderSchemes...)
	writes := handlerChecked(models.CapMailWrite, folderSchemes...)
	readAndSend := make([]oapifly.SecurityRequirement, len(folderSchemes))
	for i, scheme := range folderSchemes {
		readAndSend[i] = oapifly.SecurityRequirement{scheme: {models.CapMailRead, models.CapMailSend}}
	}
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/messages/draft",
		OperationID: "saveDraft", Summary: "Save a new draft", Tags: []string{"Drafts"}, Status: http.StatusCreated,
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden}, Security: writes}, h.saveDraft)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPut, Path: "/api/v1/messages/draft/{id}",
		OperationID: "updateDraft", Summary: "Update an existing draft", Tags: []string{"Drafts"},
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}, Security: writes}, h.updateDraft)
	// A scheduled send is seen and called back with what sending it takes: mail:send as its
	// from address, checked in the handler (#743).
	sends := handlerChecked(models.CapMailSend, folderSchemes...)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/messages/scheduled",
		OperationID: "listScheduledMessages", Summary: "List the messages waiting to be sent", Tags: []string{"Messages"},
		Description: "The account's waiting sends from the addresses the session may send as (mail:send); the others are left out.",
		Errors:      []int{http.StatusUnauthorized}, Security: sends}, h.listScheduledSends)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/messages/scheduled/{id}",
		OperationID: "cancelScheduledMessage", Summary: "Cancel a scheduled message", Tags: []string{"Messages"},
		Description: "A waiting send from an address the session may not send as (mail:send) is not found, as the list leaves it out.",
		Status:      http.StatusNoContent, Errors: []int{http.StatusUnauthorized, http.StatusNotFound}, Security: sends}, h.cancelScheduledSend)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/messages/{id}/calendar-reply",
		OperationID: "respondToCalendar", Summary: "Reply to a calendar invitation", Tags: []string{"Calendar"},
		Description: "The invitation's message is read with mail:read; the reply is sent from an address the session may send as (mail:send).",
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusServiceUnavailable},
		Security:    readAndSend}, h.respondToCalendar)
	writeAndSend := make([]oapifly.SecurityRequirement, len(folderSchemes))
	for i, scheme := range folderSchemes {
		writeAndSend[i] = oapifly.SecurityRequirement{scheme: {models.CapMailWrite, models.CapMailSend}}
	}
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/messages/send",
		OperationID: "sendMessage", Summary: "Send a message", Tags: []string{"Messages"},
		Description: "Sent at once (201), or with scheduled_at stored and sent then (202); the outbound pipeline may " +
			"discard it (200). The sender is an address the session may send as (mail:send).",
		Status: http.StatusCreated, SuccessStatuses: []int{http.StatusOK, http.StatusAccepted},
		Responses: append(append([]oapifly.EndpointResponse{}, sendOutcomes...), oapifly.EndpointResponse{Status: http.StatusUnprocessableEntity, Description: "Binding validation, sender policy or scheduling refusal", Type: oapifly.TypeOf[ForwardRefusalResponse]()}),
		Errors:    sendRefusals, Security: sends}, h.sendMessage)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/messages/draft/{id}/send",
		OperationID: "sendDraft", Summary: "Send a draft", Tags: []string{"Drafts"},
		Description: "The draft (mail:write) is sent as sendMessage sends, and deleted once it is; a refused send keeps it.",
		Status:      http.StatusCreated, SuccessStatuses: []int{http.StatusOK}, Responses: sentOrDiscarded,
		Errors: append([]int{http.StatusNotFound}, sendRefusals...), Security: writeAndSend}, h.sendDraft)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/messages/{id}/forward",
		OperationID: "forwardMessage", Summary: "Forward a message", Tags: []string{"Messages"},
		Description: "The message (mail:read) is forwarded through the normal sender authorization, rate limit and outbound policy, with Fwd: and the note above its original header. Stored attachments, including empty and inline parts, are re-attached; the Sent copy owns independent attachment objects. The final signed MIME message is bounded by SMTP_MAX_MESSAGE_SIZE, and sender quota includes the copied parts. Sent, attachment references, quota and recipient work commit together. Unavailable or changed source objects fail the forward instead of dropping parts.",
		Status:      http.StatusCreated, SuccessStatuses: []int{http.StatusOK}, Responses: append(append([]oapifly.EndpointResponse{}, sentOrDiscarded...), oapifly.EndpointResponse{Status: http.StatusUnprocessableEntity, Description: "Binding validation, sender policy or sender quota refusal", Type: oapifly.TypeOf[ForwardRefusalResponse]()}),
		Errors: append([]int{http.StatusNotFound}, sendRefusals...), Security: readAndSend}, h.forwardMessage)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/accounts/{id}/calendar-events",
		OperationID: "listCalendarEvents", Summary: "List the calendar invitations an account has received", Tags: []string{"Calendar"},
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden}, Security: reads}, h.listCalendarEvents)
}
