package imap

import "github.com/restmail/restmail/internal/gateway/apiclient"

// Backend is the exact set of REST API operations the IMAP gateway depends on.
// It is deliberately narrow: only the methods this protocol actually calls.
//
// The concrete *apiclient.Client satisfies this interface structurally, so the
// production wiring in cmd/imap-gateway is unchanged — it keeps passing the real
// client. Tests substitute an in-memory implementation, exercising the full
// session state machine (FETCH/UID FETCH/STORE/IDLE/…) without a live API.
type Backend interface {
	// Login authenticates a mailbox user and returns an access token.
	Login(email, password string) (*apiclient.LoginResponse, error)
	// ListFolders returns all folders for an account.
	ListFolders(token string, accountID uint) (*apiclient.FolderListResponse, error)
	// ListMessages returns the full folder in oldest-first order.
	ListMessages(token string, accountID uint, folder string) (*apiclient.MessageListResponse, error)
	// GetMessage returns a full message by ID.
	GetMessage(token string, msgID uint) (*apiclient.MessageDetailResponse, error)
	// GetRawMessage returns the pristine stored RFC 2822 bytes for a message,
	// or an empty string with nil error when no stored raw exists.
	GetRawMessage(token string, msgID uint) (string, error)
	// UpdateMessage updates message flags (or moves it via a "folder" update).
	UpdateMessage(token string, msgID uint, updates map[string]interface{}) error
	// DeleteMessage deletes a message.
	DeleteMessage(token string, msgID uint) error
	// DeliverMessage delivers a message to a local mailbox (COPY/APPEND).
	DeliverMessage(req *apiclient.DeliverRequest) (*apiclient.DeliverResponse, error)
	// GetQuota returns quota usage for an account.
	GetQuota(token string, accountID uint) (*apiclient.QuotaResponse, error)
}
