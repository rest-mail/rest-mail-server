package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/audit"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// maxTransferMessages bounds one copy or move, so one request holds one
// transaction of a known size.
const maxTransferMessages = 1000

// MessageTransferHandler copies or moves a set of messages between the
// folders of one account in one database transaction: every message or none.
// It is how the IMAP gateway answers COPY and MOVE, which must leave the
// destination as it was when they fail (RFC 3501 §6.4.7, RFC 6851 §3.3).
type MessageTransferHandler struct {
	db     *gorm.DB
	broker messageEventPublisher
	// rowFault, when set, is called before each message is written, with its
	// 1-based place in the set; an error fails the transaction there. Tests
	// use it to prove a failure on a later row undoes the earlier ones.
	rowFault func(n int) error
}

func NewMessageTransferHandler(db *gorm.DB, broker messageEventPublisher) *MessageTransferHandler {
	return &MessageTransferHandler{db: db, broker: broker}
}

type transferMessagesRequest struct {
	ID   uint32                        `path:"id" minimum:"1" example:"1"`
	Body apitypes.TransferMessagesBody `body:""`
}

// Register adds the operation to the mail routes. The handler checks
// mail:write on the account.
func (h *MessageTransferHandler) Register(rs endpoint.Routes) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/accounts/{id}/messages/transfer",
		OperationID: "transferMessages", Summary: "Copy or move a set of messages to another folder", Tags: []string{"Messages"},
		Description:      "Copies, or with move true moves, up to 1000 messages of one folder of the account to another folder in one transaction: every message is copied or moved, or none is. Each gets a new, higher ID in the destination, reported in the order of message_ids; flags, keywords, internal date and the stored source are kept. A move removes the originals. Requires mail:write on the account. A message that is not in source_folder of this account, or that the caller may not reach, is not found, and nothing changes. Storage the copies need is charged to the account's quota; a copy that would exceed it is refused with mailbox_full and nothing changes. Each transfer is recorded in a transactional messages.transfer audit entry.",
		Errors:           []int{http.StatusNotFound, http.StatusConflict},
		Security:         handlerChecked(models.CapMailWrite, folderSchemes...),
		DescribedRequest: transferMessagesRequest{}}, h.transferMessages)
}

var errTransferQuota = endpoint.Refuse(http.StatusConflict, "mailbox_full", "The account has insufficient quota for the copies")

func transferFolder(name string) string {
	if strings.EqualFold(name, "INBOX") {
		return "INBOX"
	}
	return name
}

func (h *MessageTransferHandler) transferMessages(r *http.Request, in transferMessagesRequest) (apitypes.TransferMessagesResult, error) {
	var result apitypes.TransferMessagesResult
	body := in.Body
	body.SourceFolder, body.Folder = transferFolder(body.SourceFolder), transferFolder(body.Folder)
	problems := map[string]string{}
	if body.SourceFolder == "" {
		problems["source_folder"] = "required: the folder that holds the messages"
	} else if problem := rmail.FolderNameProblem(body.SourceFolder); problem != "" {
		problems["source_folder"] = problem
	}
	if body.Folder == "" {
		problems["folder"] = "required: the destination folder"
	} else if problem := rmail.FolderNameProblem(body.Folder); problem != "" {
		problems["folder"] = problem
	}
	switch n := len(body.MessageIDs); {
	case n == 0:
		problems["message_ids"] = "required: 1 to 1000 message IDs"
	case n > maxTransferMessages:
		problems["message_ids"] = "at most 1000 message IDs"
	default:
		seen := make(map[uint]bool, n)
		for _, id := range body.MessageIDs {
			if id == 0 {
				problems["message_ids"] = "every ID must be a positive integer"
				break
			}
			if seen[id] {
				problems["message_ids"] = "each ID may appear once"
				break
			}
			seen[id] = true
		}
	}
	if len(problems) != 0 {
		return result, endpoint.Invalid(problems)
	}
	claims := middleware.GetClaims(r)
	if claims == nil {
		return result, errNotSignedIn
	}
	accountID := uint(in.ID)
	if _, err := resolveAccountMailbox(h.db, accountID, claims, models.CapMailWrite); err != nil {
		if errors.Is(err, errNoAccess) {
			return result, endpoint.Refuse(http.StatusNotFound, "mailbox_not_found", "Account not found")
		}
		if errors.Is(err, errNotAllowed) {
			return result, endpoint.Refuse(http.StatusForbidden, "forbidden", "Insufficient permissions")
		}
		return result, err
	}

	ids := make([]uint, 0, len(body.MessageIDs))
	err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		var sources []models.Message
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id IN ? AND mailbox_id = ? AND folder = ? AND is_deleted = ?", body.MessageIDs, accountID, body.SourceFolder, false).
			Find(&sources).Error; err != nil {
			return err
		}
		if len(sources) != len(body.MessageIDs) {
			return errMessageNotFound
		}
		byID := make(map[uint]models.Message, len(sources))
		for _, msg := range sources {
			byID[msg.ID] = msg
		}
		var charge int64
		var counted, subjectBytes, bodyBytes, attachmentBytes int64
		for n, id := range body.MessageIDs {
			if h.rowFault != nil {
				if err := h.rowFault(n + 1); err != nil {
					return err
				}
			}
			src := byID[id]
			var attachments []models.Attachment
			if src.HasAttachments {
				if err := tx.Where("message_id = ?", src.ID).Order("id").Find(&attachments).Error; err != nil {
					return err
				}
			}
			copied := src
			copied.ID, copied.Folder = 0, body.Folder
			copied.CreatedAt, copied.UpdatedAt = time.Time{}, time.Time{}
			copied.Mailbox = models.Mailbox{}
			if err := tx.Omit("Mailbox").Create(&copied).Error; err != nil {
				return err
			}
			ids = append(ids, copied.ID)
			if body.Move {
				// The attachments go with the message, and the original goes.
				if src.HasAttachments {
					if err := tx.Model(&models.Attachment{}).Where("message_id = ?", src.ID).
						Update("message_id", copied.ID).Error; err != nil {
						return err
					}
				}
				deleted := tx.Delete(&models.Message{}, src.ID)
				if deleted.Error != nil {
					return deleted.Error
				}
				if deleted.RowsAffected != 1 {
					return errMessageNotFound
				}
				continue
			}
			var size int64
			for i := range attachments {
				size += attachments[i].SizeBytes
				attachments[i].ID, attachments[i].MessageID, attachments[i].CreatedAt = 0, copied.ID, time.Time{}
			}
			if len(attachments) != 0 {
				if err := tx.Create(&attachments).Error; err != nil {
					return err
				}
			}
			charge += int64(src.SizeBytes) + size
			counted++
			subjectBytes += int64(len(src.Subject))
			bodyBytes += int64(len(src.BodyText) + len(src.BodyHTML))
			attachmentBytes += size
		}
		if counted != 0 {
			if err := repositories.ReserveStorage(tx, accountID, charge); err != nil {
				if errors.Is(err, repositories.ErrStorageQuota) {
					return errTransferQuota
				}
				return err
			}
			usage := tx.Model(&models.QuotaUsage{}).Where("mailbox_id = ?", accountID).Updates(map[string]any{
				"subject_bytes":    gorm.Expr("subject_bytes + ?", subjectBytes),
				"body_bytes":       gorm.Expr("body_bytes + ?", bodyBytes),
				"attachment_bytes": gorm.Expr("attachment_bytes + ?", attachmentBytes),
				"message_count":    gorm.Expr("message_count + ?", counted),
			})
			// The counters are kept as delivery keeps them: an account without
			// its counter row still takes the copies.
			if usage.Error != nil {
				return usage.Error
			}
		}
		entry := middleware.NewAuditEntry("messages.transfer", r, time.Now())
		entry.Status = http.StatusOK
		entry.Target["account_id"] = strconv.FormatUint(uint64(accountID), 10)
		entry.Target["source_folder"] = body.SourceFolder
		entry.Target["folder"] = body.Folder
		entry.Target["move"] = strconv.FormatBool(body.Move)
		entry.Target["messages"] = strconv.Itoa(len(ids))
		return audit.Append(tx, entry)
	})
	if err != nil {
		return apitypes.TransferMessagesResult{}, fmt.Errorf("transfer messages: %w", err)
	}
	if h.broker != nil {
		h.broker.Publish(r.Context(), accountID, SSEEvent{Type: "folder_update", Data: map[string]interface{}{"folder": body.Folder}})
		if body.Move {
			h.broker.Publish(r.Context(), accountID, SSEEvent{Type: "folder_update", Data: map[string]interface{}{"folder": body.SourceFolder}})
		}
	}
	result.IDs = ids
	return result, nil
}
