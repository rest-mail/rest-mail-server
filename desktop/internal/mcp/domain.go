package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/rest-mail/rest-mail-server/desktop/internal/apiclient"
	"github.com/rest-mail/rest-mail-server/desktop/internal/session"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
)

type SessionClient interface {
	Status() session.SessionStatus
	SignIn(string, string, string) (session.SignInResult, error)
	SubmitTOTP(string) error
	SignOut() error
}

type MailClient interface {
	ListFolders(context.Context, uint) ([]apitypes.FolderSummary, error)
	ListMessages(context.Context, uint, string, apiclient.Page, string) (apiclient.MessagePage, error)
	GetMessage(context.Context, uint) (apitypes.Message, error)
	Search(context.Context, uint, string, string, apiclient.Page) (apiclient.MessagePage, error)
	SendMessage(context.Context, apitypes.SendMessageRequest) error
	SaveDraft(context.Context, uint, apiclient.Draft) (apitypes.Message, error)
	SetFlags(context.Context, []uint, apitypes.UpdateMessageBody) error
	MoveMessages(context.Context, []uint, string) error
	DeleteMessages(context.Context, []uint, bool) error
}

type MailDomain struct {
	session SessionClient
	mail    MailClient
}

func NewDomain(manager SessionClient, client MailClient) *MailDomain {
	return &MailDomain{session: manager, mail: client}
}

var errInvalidArguments = errors.New("invalid MCP arguments")

func decodeArgs[Args any](data json.RawMessage) (Args, error) {
	var args Args
	if json.Unmarshal(data, &args) != nil {
		return args, errInvalidArguments
	}
	return args, nil
}

func (domain *MailDomain) Call(ctx context.Context, name string, data json.RawMessage) (any, error) {
	switch name {
	case "session_status":
		status := domain.session.Status()
		return map[string]any{"signedIn": status.SignedIn, "server": status.Server, "email": status.Email, "accountId": status.AccountID}, nil
	case "sign_in":
		args, err := decodeArgs[SignInArgs](data)
		if err != nil {
			return nil, err
		}
		result, err := domain.session.SignIn(args.Server, args.Email, args.Password)
		return map[string]any{"needsTOTP": result.NeedsTOTP}, err
	case "submit_totp":
		args, err := decodeArgs[TOTPArgs](data)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, domain.session.SubmitTOTP(args.Code)
	case "sign_out":
		return map[string]any{"ok": true}, domain.session.SignOut()
	}
	status := domain.session.Status()
	if !status.SignedIn {
		return nil, session.ErrSignedOut
	}
	switch name {
	case "list_folders":
		return domain.mail.ListFolders(ctx, status.AccountID)
	case "list_messages":
		args, err := decodeArgs[ListMessagesArgs](data)
		if err != nil {
			return nil, err
		}
		if args.Page < 0 || args.Page > 100 || args.Folder == "" {
			return nil, errInvalidArguments
		}
		page := apiclient.Page{Limit: 50}
		for index := 1; ; index++ {
			result, err := domain.mail.ListMessages(ctx, status.AccountID, args.Folder, page, args.Query)
			if err != nil || index >= args.Page {
				return result, err
			}
			if result.Pagination == nil || !result.Pagination.HasMore || result.Pagination.Cursor == "" {
				return apiclient.MessagePage{Data: []apitypes.Message{}}, nil
			}
			page.Cursor = result.Pagination.Cursor
		}
	case "get_message":
		args, err := decodeArgs[MessageArgs](data)
		if err != nil {
			return nil, err
		}
		id, err := messageID(args.ID)
		if err != nil {
			return nil, err
		}
		return domain.mail.GetMessage(ctx, id)
	case "search":
		args, err := decodeArgs[SearchArgs](data)
		if err != nil {
			return nil, err
		}
		return domain.mail.Search(ctx, status.AccountID, args.Query, "", apiclient.Page{Limit: 50})
	case "send_message", "save_draft":
		args, err := decodeArgs[ComposeArgs](data)
		if err != nil {
			return nil, err
		}
		if name == "save_draft" {
			return domain.mail.SaveDraft(ctx, 0, apiclient.Draft{From: status.Email, To: args.To, Cc: args.CC, Subject: args.Subject, BodyText: args.Body, BodyHTML: args.HTML})
		}
		return map[string]any{"ok": true}, domain.mail.SendMessage(ctx, apitypes.SendMessageRequest{From: status.Email, To: args.To, Cc: args.CC, Subject: args.Subject, BodyText: args.Body, BodyHTML: args.HTML})
	case "set_flags":
		args, err := decodeArgs[FlagsArgs](data)
		if err != nil {
			return nil, err
		}
		ids, err := messageIDs(args.IDs)
		if err != nil || (args.Seen == nil && args.Flagged == nil) {
			return nil, errInvalidArguments
		}
		return map[string]any{"ok": true}, domain.mail.SetFlags(ctx, ids, apitypes.UpdateMessageBody{IsRead: args.Seen, IsFlagged: args.Flagged})
	case "move_messages":
		args, err := decodeArgs[MoveArgs](data)
		if err != nil {
			return nil, err
		}
		ids, err := messageIDs(args.IDs)
		if err != nil || args.Folder == "" {
			return nil, errInvalidArguments
		}
		return map[string]any{"ok": true}, domain.mail.MoveMessages(ctx, ids, args.Folder)
	case "delete_messages":
		args, err := decodeArgs[DeleteArgs](data)
		if err != nil {
			return nil, err
		}
		ids, err := messageIDs(args.IDs)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, domain.mail.DeleteMessages(ctx, ids, false)
	default:
		return nil, fmt.Errorf("unknown MCP domain tool")
	}
}

func messageID(value string) (uint, error) {
	parsed, err := strconv.ParseUint(value, 10, strconv.IntSize)
	if err != nil || parsed == 0 {
		return 0, errInvalidArguments
	}
	return uint(parsed), nil
}

func messageIDs(values []string) ([]uint, error) {
	if len(values) == 0 || len(values) > 500 {
		return nil, errInvalidArguments
	}
	ids := make([]uint, len(values))
	for index, value := range values {
		id, err := messageID(value)
		if err != nil {
			return nil, err
		}
		ids[index] = id
	}
	return ids, nil
}

func domainErrorCode(err error) string {
	switch {
	case errors.Is(err, session.ErrSignedOut):
		return "signed_out"
	case errors.Is(err, errInvalidArguments):
		return "invalid_arguments"
	case errors.Is(err, context.DeadlineExceeded):
		return "domain_timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	}
	var apiError *apiclient.Error
	var sessionError *session.APIError
	status := 0
	if errors.As(err, &apiError) {
		status = apiError.Status
	}
	if errors.As(err, &sessionError) {
		status = sessionError.Status
	}
	switch status {
	case 400, 422:
		return "invalid_arguments"
	case 401:
		return "unauthorized"
	case 403:
		return "forbidden"
	case 404:
		return "not_found"
	case 409:
		return "conflict"
	case 429:
		return "rate_limited"
	default:
		return "domain_error"
	}
}
