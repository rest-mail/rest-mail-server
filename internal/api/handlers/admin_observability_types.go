package handlers

import (
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

// Ban is the API representation, independent of database persistence.
type Ban struct {
	ID        uint       `json:"id"`
	IP        string     `json:"ip"`
	Reason    string     `json:"reason"`
	Protocol  string     `json:"protocol" enums:"smtp,imap,pop3,all"`
	CreatedBy string     `json:"created_by"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

func banOf(row models.Ban) Ban {
	return Ban{
		ID:        row.ID,
		IP:        row.IP,
		Reason:    row.Reason,
		Protocol:  row.Protocol,
		CreatedBy: row.CreatedBy,
		ExpiresAt: row.ExpiresAt,
		CreatedAt: row.CreatedAt,
	}
}

// DeliveryLogEntry is the API representation, independent of database persistence.
type DeliveryLogEntry struct {
	ID              uint       `json:"id"`
	MessageID       *uint      `json:"message_id"`
	Sender          string     `json:"sender"`
	Recipient       string     `json:"recipient"`
	Domain          string     `json:"domain"`
	BodyType        string     `json:"body_type"`
	Status          string     `json:"status" enums:"pending,delivering,deferred,delivered,bounced,expired"`
	Attempts        int        `json:"attempts"`
	LastAttempt     *time.Time `json:"last_attempt"`
	NextAttempt     time.Time  `json:"next_attempt"`
	LastError       string     `json:"last_error"`
	LastErrorCode   int        `json:"last_error_code"`
	FailureCategory string     `json:"failure_category" enums:",bad_domain,bad_mailbox,connection,dns,greylisted,mailbox_full,other_permanent,other_transient,policy,rate_limited,tls"`
	MaxRetries      int        `json:"max_retries"`
	CreatedAt       time.Time  `json:"created_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
}

func deliveryLogEntryOf(row models.OutboundQueue) DeliveryLogEntry {
	return DeliveryLogEntry{
		ID:              row.ID,
		MessageID:       row.MessageID,
		Sender:          row.Sender,
		Recipient:       row.Recipient,
		Domain:          row.Domain,
		BodyType:        row.BodyType,
		Status:          row.Status,
		Attempts:        row.Attempts,
		LastAttempt:     row.LastAttempt,
		NextAttempt:     row.NextAttempt,
		LastError:       row.LastError,
		LastErrorCode:   row.LastErrorCode,
		FailureCategory: string(row.FailureCategory),
		MaxRetries:      row.MaxRetries,
		CreatedAt:       row.CreatedAt,
		ExpiresAt:       row.ExpiresAt,
	}
}

// AuditEntry is the API representation, independent of database persistence.
type AuditEntry struct {
	Seq          int64             `json:"seq"`
	OccurredAt   time.Time         `json:"occurred_at"`
	ActorKind    string            `json:"actor_kind" enums:"account,system,anonymous"`
	ActorID      *uint             `json:"actor_id"`
	ActorName    string            `json:"actor_name"`
	CredentialID *uint             `json:"credential_id"`
	SessionID    string            `json:"session_id"`
	Action       string            `json:"action"`
	Outcome      string            `json:"outcome" enums:"success,denied,failed,error"`
	ErrorCode    string            `json:"error_code"`
	Method       string            `json:"method"`
	Route        string            `json:"route"`
	Status       int               `json:"status"`
	Target       map[string]string `json:"target"`
	IP           string            `json:"ip"`
	UserAgent    string            `json:"user_agent"`
	RequestID    string            `json:"request_id"`
	PrevHash     string            `json:"prev_hash"`
	Hash         string            `json:"hash"`
}

func auditEntryOf(row models.AuditEntry) AuditEntry {
	return AuditEntry{
		Seq:          row.Seq,
		OccurredAt:   row.OccurredAt,
		ActorKind:    row.ActorKind,
		ActorID:      row.ActorID,
		ActorName:    row.ActorName,
		CredentialID: row.CredentialID,
		SessionID:    row.SessionID,
		Action:       row.Action,
		Outcome:      row.Outcome,
		ErrorCode:    row.ErrorCode,
		Method:       row.Method,
		Route:        row.Route,
		Status:       row.Status,
		Target:       row.Target,
		IP:           row.IP,
		UserAgent:    row.UserAgent,
		RequestID:    row.RequestID,
		PrevHash:     row.PrevHash,
		Hash:         row.Hash,
	}
}
