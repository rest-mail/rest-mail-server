package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/api/respond"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
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
	Status        string          `json:"status" enums:"pending,sending,sent,failed,cancelled,held"`
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

// sendMessageDescription is what a send's body is described as. It binds nothing: the handler
// checks from and to itself, after authorization, so these tags change no answer. Its list
// items get their format in internal/api/described_items.go, which a tag cannot do. The body
// is an anonymous struct so the description holds it inline: fuzz.pin in Vertrag 0.42.0 reads
// a body's properties without following a $ref, and the coverage run pins scheduled_at.
type sendMessageDescription struct {
	IdempotencyKey string `header:"Idempotency-Key" maxLength:"255" json:"-" description:"Optional account-scoped retry key, retained for 24 hours after success. Reuse only for the same route, resource and body."`
	Body           struct {
		apitypes.SendMessageRequest
		From string   `json:"from" minLength:"1" format:"idn-email"`
		To   []string `json:"to" minItems:"1"`
	} `body:""`
}

// sendOutcomes are the answers a send gives, each with its own status: the sent message (201),
// the message scheduled for later (202), or the pipeline's discard (200).
var sendOutcomes = []oapifly.EndpointResponse{
	{Status: http.StatusCreated, Description: "Queued for every recipient; the copy kept in Sent", Type: oapifly.TypeOf[endpoint.Data[Message]]()},
	{Status: http.StatusAccepted, Description: "Retained: scheduled for later, or held by a send limit until human release", Type: oapifly.TypeOf[endpoint.Data[ScheduledMessage]]()},
	{Status: http.StatusOK, Description: "Discarded by the outbound pipeline", Type: oapifly.TypeOf[endpoint.Data[SendDiscarded]]()},
}

// sentOrDiscarded are the answers of a send that cannot be scheduled: a draft's, a forward's.
var sentOrDiscarded = []oapifly.EndpointResponse{sendOutcomes[0], sendOutcomes[1], sendOutcomes[2]}

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

// sendRefusals are what a send refuses with: an unparseable body or wrong media type (400), a field missing or invalid (422), no session
// (401), an address the session may not send as or the outbound pipeline's reject or
// quarantine (403), the sender domain's policy or a bad scheduled_at (422), the account's send
// rate (429, with Retry-After) and a deferral (503).
var sendRefusals = []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
	http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusNotFound}

// idempotentSendRefusals add 409 idempotency_conflict for the routes that take an Idempotency-Key
// (send, send-draft and forward); unsubscribeMailto takes none, so it keeps sendRefusals.
var idempotentSendRefusals = append([]int{http.StatusConflict}, sendRefusals...)

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
// in the caller's own mailbox. The items of to and cc get their format in
// internal/api/described_items.go, which also describes from as empty or idn-email: an empty
// one means none, and other values are matched to a writable mailbox (otherwise 403).
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

// updateDraftBody changes what it names. Its to, cc and from are described as saveDraftBody's are.
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

// What a draft's addresses are held to, in the words of the operations that take them.
// internal/api/handlers/draft_fields_test.go holds these words to the handlers.
const (
	draftRecipientsDescription = "to and cc items are described as bare addresses, internationalized or not; the handler also accepts " +
		"whatever RFC 5322 reads as one address, such as a display name around one, and refuses any other item with 422. "
	draftSenderDescription = "any other from must be the exact address of a mailbox the caller may write to, or the answer is 403."
)

// RegisterCompose mounts the drafts, the scheduled sends, the calendar invitations and the
// replies to them.
func (h *MessageHandler) RegisterCompose(rs endpoint.Routes, audit ...func(string, ...middleware.AuditOption) endpoint.Middleware) {
	sendAudit := func(action string) []endpoint.Middleware {
		if len(audit) == 0 {
			return nil
		}
		return []endpoint.Middleware{audit[0](action, middleware.AuditBodyFields("from"))}
	}
	reads := handlerChecked(models.CapMailRead, folderSchemes...)
	writes := handlerChecked(models.CapMailWrite, folderSchemes...)
	readAndSend := make([]oapifly.SecurityRequirement, len(folderSchemes))
	for i, scheme := range folderSchemes {
		readAndSend[i] = oapifly.SecurityRequirement{scheme: {models.CapMailRead, models.CapMailSend}}
	}
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/messages/draft",
		OperationID: "saveDraft", Summary: "Save a new draft", Tags: []string{"Drafts"}, Status: http.StatusCreated,
		Description: "Sender lookup and storage follow request cancellation; dependency failures return 500, not forbidden. " +
			draftRecipientsDescription + "An empty from, like none, keeps the draft in the caller's own mailbox with no sender; " + draftSenderDescription,
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden}, Security: writes}, h.saveDraft)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPut, Path: "/api/v1/messages/draft/{id}",
		OperationID: "updateDraft", Summary: "Update an existing draft", Tags: []string{"Drafts"},
		Description: "Update and reload commit together. A failed write/reload returns 500 and rolls back; an absent or no-longer-draft row is 404. Declared in_reply_to and references fields are persisted; omitted/null fields stay unchanged. Dependency errors are distinct from denied access, and SQL follows request cancellation. " +
			draftRecipientsDescription + "An omitted to or cc stays as it is, and an empty list clears it. An empty from clears the sender and leaves the draft in its mailbox; " + draftSenderDescription,
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}, Security: writes}, h.updateDraft)
	// Seeing or cancelling a scheduled send needs its selected sending capability.
	sends := delegatedSendSecurity()
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/messages/scheduled",
		OperationID: "listScheduledMessages", Summary: "List the messages waiting to be sent", Tags: []string{"Messages"},
		Description: "The account's waiting sends that the session may send using their stored mode (mail:send, mail:send_as or mail:send_on_behalf); the others are left out.",
		Errors:      []int{http.StatusUnauthorized}, Security: sends}, h.listScheduledSends)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/messages/scheduled/{id}",
		OperationID: "cancelScheduledMessage", Summary: "Cancel a scheduled message", Tags: []string{"Messages"},
		Description: "A waiting send the session may not send using its stored mode is not found, as the list leaves it out. Lookup, authorization and cancellation update follow request cancellation.",
		Status:      http.StatusNoContent, Errors: []int{http.StatusUnauthorized, http.StatusNotFound}, Security: sends}, h.cancelScheduledSend)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/messages/{id}/calendar-reply",
		OperationID: "respondToCalendar", Summary: "Reply to a calendar invitation", Tags: []string{"Calendar"},
		Description: "The invitation's message is read with mail:read; the reply is sent from an address the session may send as (mail:send). The 200 means the reply is durably queued for the organizer, on this server or elsewhere, not that it has arrived: a later permanent refusal bounces to the sender as a DSN.",
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusServiceUnavailable},
		Security:    readAndSend}, h.respondToCalendar)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/messages/send",
		OperationID: "sendMessage", Summary: "Send a message", Tags: []string{"Messages"},
		DescribedRequest: sendMessageDescription{},
		Description: "Queued at once (201), or with scheduled_at stored and queued then (202); the outbound pipeline may " +
			"discard it (200). to needs at least one address. from, to, cc and bcc are described as bare addresses, " +
			"internationalized or not; the handler also accepts an RFC 5322 display name around one. scheduled_at must be at least a minute " +
			"ahead and within a year; null or omitted sends at once. " + queuedDeliveryDescription + sendIdempotencyDescription + delegatedSendingDescription,
		Status: http.StatusCreated, SuccessStatuses: []int{http.StatusOK, http.StatusAccepted},
		Responses: append(append([]oapifly.EndpointResponse{}, sendOutcomes...), oapifly.EndpointResponse{Status: http.StatusUnprocessableEntity, Description: "validation_failed with details.fields for binding, sender policy or scheduling rules", Type: oapifly.TypeOf[respond.ValidationErrorResponse]()}),
		Errors:    idempotentSendRefusals, Security: delegatedSendSecurity(), Middleware: sendAudit("mail.send")}, h.sendMessage)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/messages/draft/{id}/send",
		OperationID: "sendDraft", Summary: "Send a draft", Tags: []string{"Drafts"},
		Description: "The draft (mail:write) is sent as sendMessage sends, and deleted once it is; a refused send keeps it. Draft lookup/access dependency failures return 500 and follow request cancellation. " + queuedDeliveryDescription + sendIdempotencyDescription + delegatedSendingDescription,
		Status:      http.StatusCreated, SuccessStatuses: []int{http.StatusOK, http.StatusAccepted}, Responses: sentOrDiscarded,
		Errors: append([]int{http.StatusNotFound}, idempotentSendRefusals...), Security: delegatedSendSecurity(models.CapMailWrite), Middleware: sendAudit("mail.send_draft")}, h.sendDraft)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/messages/{id}/forward",
		OperationID: "forwardMessage", Summary: "Forward a message", Tags: []string{"Messages"},
		Description: "The message (mail:read) is forwarded through the normal sender authorization, rate limit and outbound policy, with Fwd: and the note above its original header. Stored attachments, including empty and inline parts, are re-attached; the Sent copy owns independent attachment objects. The final signed MIME message is bounded by SMTP_MAX_MESSAGE_SIZE, and sender quota includes the copied parts. Sent, attachment references, quota and queue entries commit together. Unavailable or changed source objects fail the forward instead of dropping parts. " + queuedDeliveryDescription + sendIdempotencyDescription + delegatedSendingDescription,
		Status:      http.StatusCreated, SuccessStatuses: []int{http.StatusOK, http.StatusAccepted}, Responses: append(append([]oapifly.EndpointResponse{}, sentOrDiscarded...), oapifly.EndpointResponse{Status: http.StatusUnprocessableEntity, Description: "Binding validation, sender policy or sender quota refusal", Type: oapifly.TypeOf[ForwardRefusalResponse]()}),
		Errors: append([]int{http.StatusNotFound}, idempotentSendRefusals...), Security: delegatedSendSecurity(models.CapMailRead), Middleware: sendAudit("mail.forward")}, h.forwardMessage)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/messages/{id}/unsubscribe",
		OperationID: "getUnsubscribeOptions", Summary: "List a message's unsubscribe targets", Tags: []string{"Messages"},
		Description: "The targets the message's List-Unsubscribe field offers (RFC 2369), checked because the sender wrote them; nothing is sent. one_click is present only when List-Unsubscribe-Post is List-Unsubscribe=One-Click and a DKIM signature this server verified as pass covers both List-Unsubscribe and List-Unsubscribe-Post (RFC 8058 §4); without one that HTTPS URI is listed first in links instead. links are other HTTPS pages for the person to open; the server never fetches them. A field given more than once, an item that does not parse, an http: URI and a mailto: URI naming more than one address are left out.",
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}, Security: reads}, h.getUnsubscribeOptions)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/messages/{id}/unsubscribe/one-click",
		OperationID: "unsubscribeOneClick", Summary: "Unsubscribe with RFC 8058 one-click", Tags: []string{"Messages"},
		Description: "The server POSTs List-Unsubscribe=One-Click to the message's one-click target, once, only on this request. The target must be HTTPS and it and every redirect hop must resolve only to public addresses, checked again on the address dialed; otherwise 422 unsubscribe_target_refused. The same refusal answers a one-click claim that no DKIM signature this server verified as pass covers, List-Unsubscribe and List-Unsubscribe-Post both (RFC 8058 §4). No cookie or credential is sent. 404 no_one_click_unsubscribe when the message has no one-click target; 502 unsubscribe_failed when the target cannot be reached or answers other than 2xx.",
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusBadGateway}, Security: writes,
		Middleware: sendAudit("mail.unsubscribe_one_click")}, h.unsubscribeOneClick)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/messages/{id}/unsubscribe/mailto",
		OperationID: "unsubscribeMailto", Summary: "Unsubscribe by sending the mailto: message", Tags: []string{"Messages"},
		Description: "Sends the message mailto target index asks for (its address, subject and body, RFC 6068) through sendMessage, so every send rule applies; an empty subject or body is sent as unsubscribe. from defaults to the message's account. 404 no_mailto_unsubscribe when there is no target at index. " + delegatedSendingDescription,
		Status:      http.StatusCreated, SuccessStatuses: []int{http.StatusOK, http.StatusAccepted}, Responses: sentOrDiscarded,
		Errors: append([]int{http.StatusNotFound}, sendRefusals...), Security: delegatedSendSecurity(models.CapMailRead), Middleware: sendAudit("mail.unsubscribe_mailto")}, h.unsubscribeMailto)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/accounts/{id}/calendar-events",
		OperationID: "listCalendarEvents", Summary: "List the calendar invitations an account has received", Tags: []string{"Calendar"},
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}, Security: reads}, h.listCalendarEvents)
}
