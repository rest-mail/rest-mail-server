package imapmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/rest-mail/rest-mail-server/desktop/internal/apiclient"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
)

const maxMessageSize = 32 << 20

func (store *Store) ListFolders(ctx context.Context, account uint) ([]apitypes.FolderSummary, error) {
	folders := []apitypes.FolderSummary{}
	err := store.withClient(ctx, account, func(client *imapclient.Client) error {
		listed, err := client.List("", "*", nil).Collect()
		if err != nil {
			return err
		}
		for _, folder := range listed {
			selectable := true
			for _, attr := range folder.Attrs {
				if attr == imap.MailboxAttrNoSelect || attr == imap.MailboxAttrNonExistent {
					selectable = false
				}
			}
			if !selectable {
				continue
			}
			status, err := client.Status(folder.Mailbox, &imap.StatusOptions{NumMessages: true, NumUnseen: true, UIDNext: true}).Wait()
			if err != nil {
				return err
			}
			item := apitypes.FolderSummary{Folder: folder.Mailbox, Role: folderRole(folder), UIDNext: uint64(status.UIDNext), HighestModSeq: 1}
			if status.NumMessages != nil {
				item.Total = int64(*status.NumMessages)
			}
			if status.NumUnseen != nil {
				item.Unread = int64(*status.NumUnseen)
			}
			folders = append(folders, item)
		}
		return nil
	})
	return folders, err
}

func folderRole(folder *imap.ListData) *string {
	role := ""
	if strings.EqualFold(folder.Mailbox, "INBOX") {
		role = "inbox"
	}
	for _, attr := range folder.Attrs {
		switch attr {
		case imap.MailboxAttrSent:
			role = "sent"
		case imap.MailboxAttrDrafts:
			role = "drafts"
		case imap.MailboxAttrTrash:
			role = "trash"
		case imap.MailboxAttrJunk:
			role = "junk"
		case imap.MailboxAttrArchive:
			role = "archive"
		}
	}
	if role == "" {
		switch strings.ToLower(folder.Mailbox) {
		case "sent":
			role = "sent"
		case "drafts":
			role = "drafts"
		case "trash":
			role = "trash"
		case "spam", "junk":
			role = "junk"
		case "archive":
			role = "archive"
		}
	}
	if role == "" {
		return nil
	}
	return &role
}

type pageCursor struct {
	Account  uint   `json:"a"`
	Folder   string `json:"f"`
	Validity uint32 `json:"v"`
	Before   uint32 `json:"b"`
	Query    string `json:"q"`
}

func (store *Store) ListMessages(ctx context.Context, account uint, folder string, page apiclient.Page, query string) (apiclient.MessagePage, error) {
	result := apiclient.MessagePage{Data: []apitypes.Message{}}
	limit := page.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	err := store.withClient(ctx, account, func(client *imapclient.Client) error {
		selected, err := client.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
		if err != nil {
			return err
		}
		cursor := pageCursor{Account: account, Folder: folder, Validity: selected.UIDValidity, Query: query}
		if page.Cursor != "" {
			encoded, err := base64.RawURLEncoding.DecodeString(page.Cursor)
			if err != nil || json.Unmarshal(encoded, &cursor) != nil || cursor.Account != account || cursor.Folder != folder || cursor.Validity != selected.UIDValidity || cursor.Query != query || cursor.Before == 0 {
				return errors.New("invalid or stale message cursor")
			}
		}
		criteria := &imap.SearchCriteria{NotFlag: []imap.Flag{imap.FlagDeleted}}
		if query != "" {
			criteria.Text = []string{query}
		}
		found, err := client.UIDSearch(criteria, nil).Wait()
		if err != nil {
			return err
		}
		uids := found.AllUIDs()
		sort.Slice(uids, func(left, right int) bool { return uids[left] > uids[right] })
		result.Pagination = &apiclient.Pagination{Total: int64(len(uids))}
		for _, uid := range uids {
			if cursor.Before != 0 && uint32(uid) >= cursor.Before {
				continue
			}
			if len(result.Data) == limit {
				result.Pagination.HasMore = true
				break
			}
			buffers, err := client.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{UID: true, Flags: true, Envelope: true, InternalDate: true, RFC822Size: true, BodyStructure: &imap.FetchItemBodyStructure{Extended: true}}).Collect()
			if err != nil {
				return err
			}
			if len(buffers) == 0 {
				continue
			}
			message, err := store.summary(account, folder, selected.UIDValidity, buffers[0])
			if err != nil {
				return err
			}
			result.Data = append(result.Data, message)
		}
		if result.Pagination.HasMore {
			cursor.Before = uint32(result.Data[len(result.Data)-1].ID)
			encoded, _ := json.Marshal(cursor)
			result.Pagination.Cursor = base64.RawURLEncoding.EncodeToString(encoded)
		}
		return nil
	})
	return result, err
}

func (store *Store) summary(account uint, folder string, validity uint32, buffer *imapclient.FetchMessageBuffer) (apitypes.Message, error) {
	id, err := store.messageID(account, folder, validity, uint32(buffer.UID))
	message := apitypes.Message{ID: id, MailboxID: account, Folder: folder, Class: "person", SizeBytes: int(buffer.RFC822Size), RawSize: int(buffer.RFC822Size), ReceivedAt: buffer.InternalDate, CreatedAt: buffer.InternalDate, UpdatedAt: buffer.InternalDate, RecipientsTo: json.RawMessage("[]"), RecipientsCc: json.RawMessage("[]")}
	if err != nil {
		return message, err
	}
	for _, flag := range buffer.Flags {
		switch flag {
		case imap.FlagSeen:
			message.IsRead = true
		case imap.FlagFlagged:
			message.IsFlagged = true
			message.IsStarred = true
		case imap.FlagAnswered:
			message.IsAnswered = true
		case imap.FlagDraft:
			message.IsDraft = true
		}
	}
	if envelope := buffer.Envelope; envelope != nil {
		message.Subject = envelope.Subject
		message.MsgID = envelope.MessageID
		message.InReplyTo = strings.Join(envelope.InReplyTo, " ")
		if !envelope.Date.IsZero() {
			message.DateHeader = &envelope.Date
		}
		if len(envelope.From) > 0 {
			message.Sender = envelope.From[0].Addr()
			message.SenderName = envelope.From[0].Name
		}
		message.RecipientsTo = recipients(envelope.To)
		message.RecipientsCc = recipients(envelope.Cc)
	}
	if buffer.BodyStructure != nil {
		buffer.BodyStructure.Walk(func(_ []int, part imap.BodyStructure) bool {
			if single, ok := part.(*imap.BodyStructureSinglePart); ok && single.Filename() != "" {
				message.HasAttachments = true
			}
			return true
		})
	}
	return message, nil
}

func recipients(addresses []imap.Address) json.RawMessage {
	result := []string{}
	for _, address := range addresses {
		if address.Host != "" {
			result = append(result, address.Addr())
		}
	}
	encoded, _ := json.Marshal(result)
	return encoded
}

func (store *Store) GetMessage(ctx context.Context, id uint) (apitypes.Message, []Attachment, []byte, error) {
	var message apitypes.Message
	attachments := []Attachment{}
	var raw []byte
	identity, uid, ok := store.resolve(id)
	if !ok {
		return message, attachments, nil, errors.New("IMAP message not found")
	}
	err := store.withClient(ctx, identity.Account, func(client *imapclient.Client) error {
		if err := selectIdentity(client, identity); err != nil {
			return err
		}
		section := &imap.FetchItemBodySection{Peek: true, Partial: &imap.SectionPartial{Size: maxMessageSize + 1}}
		fetch := client.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{UID: true, Flags: true, Envelope: true, InternalDate: true, RFC822Size: true, BodySection: []*imap.FetchItemBodySection{section}})
		found := false
		for item := fetch.Next(); item != nil; item = fetch.Next() {
			buffer := &imapclient.FetchMessageBuffer{}
			for field := item.Next(); field != nil; field = item.Next() {
				switch field := field.(type) {
				case imapclient.FetchItemDataUID:
					buffer.UID = field.UID
				case imapclient.FetchItemDataFlags:
					buffer.Flags = field.Flags
				case imapclient.FetchItemDataEnvelope:
					buffer.Envelope = field.Envelope
				case imapclient.FetchItemDataInternalDate:
					buffer.InternalDate = field.Time
				case imapclient.FetchItemDataRFC822Size:
					buffer.RFC822Size = field.Size
				case imapclient.FetchItemDataBodySection:
					if field.Literal == nil || !field.MatchCommand(section) {
						return errors.New("invalid message section")
					}
					var err error
					raw, err = io.ReadAll(io.LimitReader(field.Literal, maxMessageSize+1))
					if err != nil || len(raw) > maxMessageSize {
						return errors.New("message exceeds 32 MiB limit")
					}
				}
			}
			if uint32(buffer.UID) != uid || int64(len(raw)) != buffer.RFC822Size {
				return errors.New("incomplete IMAP message")
			}
			var err error
			message, err = store.summary(identity.Account, identity.Name, identity.Validity, buffer)
			if err != nil {
				return err
			}
			found = true
		}
		if err := fetch.Close(); err != nil {
			return err
		}
		if !found {
			return errors.New("IMAP message not found")
		}
		var err error
		attachments, err = store.parseMessage(&message, raw)
		return err
	})
	return message, attachments, raw, err
}

func selectIdentity(client *imapclient.Client, identity folderIdentity) error {
	selected, err := client.Select(identity.Name, nil).Wait()
	if err != nil {
		return err
	}
	if selected.UIDValidity != identity.Validity {
		return errors.New("IMAP message identity expired")
	}
	return nil
}

func safeMove(client *imapclient.Client, uid imap.UIDSet, folder string) error {
	if !client.Caps().Has(imap.CapMove) && !client.Caps().Has(imap.CapUIDPlus) && !client.Caps().Has(imap.CapIMAP4rev2) {
		return errors.New("safe move requires MOVE or UIDPLUS")
	}
	_, err := client.Move(uid, folder).Wait()
	return err
}

func (store *Store) UpdateMessage(ctx context.Context, id uint, update apitypes.UpdateMessageBody) error {
	if update.Keywords != nil || update.UnchangedSince != nil {
		return errors.New("keyword and conditional updates are not supported for IMAP accounts")
	}
	identity, uid, ok := store.resolve(id)
	if !ok {
		return errors.New("IMAP message not found")
	}
	return store.withClient(ctx, identity.Account, func(client *imapclient.Client) error {
		if err := selectIdentity(client, identity); err != nil {
			return err
		}
		set := imap.UIDSetNum(imap.UID(uid))
		for _, entry := range []struct {
			value *bool
			flag  imap.Flag
		}{{update.IsRead, imap.FlagSeen}, {update.IsFlagged, imap.FlagFlagged}, {update.IsStarred, imap.FlagFlagged}, {update.IsAnswered, imap.FlagAnswered}, {update.IsDraft, imap.FlagDraft}, {update.IMAPDeleted, imap.FlagDeleted}} {
			if entry.value == nil {
				continue
			}
			operation := imap.StoreFlagsDel
			if *entry.value {
				operation = imap.StoreFlagsAdd
			}
			if err := client.Store(set, &imap.StoreFlags{Op: operation, Flags: []imap.Flag{entry.flag}, Silent: true}, nil).Close(); err != nil {
				return err
			}
		}
		if update.Folder != nil {
			return safeMove(client, set, *update.Folder)
		}
		return nil
	})
}

func (store *Store) DeleteMessage(ctx context.Context, id uint, permanent bool) error {
	identity, uid, ok := store.resolve(id)
	if !ok {
		return errors.New("IMAP message not found")
	}
	folder := ""
	if !permanent {
		folders, err := store.ListFolders(ctx, identity.Account)
		if err != nil {
			return err
		}
		for _, candidate := range folders {
			if candidate.Role != nil && *candidate.Role == "trash" {
				folder = candidate.Folder
				break
			}
		}
		if folder == "" {
			return errors.New("create a Trash folder before deleting messages")
		}
	}
	return store.withClient(ctx, identity.Account, func(client *imapclient.Client) error {
		if err := selectIdentity(client, identity); err != nil {
			return err
		}
		set := imap.UIDSetNum(imap.UID(uid))
		if !permanent && folder != identity.Name {
			return safeMove(client, set, folder)
		}
		if !client.Caps().Has(imap.CapUIDPlus) && !client.Caps().Has(imap.CapIMAP4rev2) {
			return errors.New("safe permanent deletion requires UIDPLUS")
		}
		if err := client.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}, Silent: true}, nil).Close(); err != nil {
			return err
		}
		return client.UIDExpunge(set).Close()
	})
}

func (store *Store) Search(ctx context.Context, account uint, query, folder string, page apiclient.Page) (apiclient.MessagePage, error) {
	if folder != "" {
		return store.ListMessages(ctx, account, folder, page, query)
	}
	folders, err := store.ListFolders(ctx, account)
	result := apiclient.MessagePage{Data: []apitypes.Message{}, Pagination: &apiclient.Pagination{}}
	if err != nil {
		return result, err
	}
	limit := page.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	var cursor struct {
		Account uint   `json:"a"`
		Query   string `json:"q"`
		Folder  string `json:"f"`
		Cursor  string `json:"c"`
	}
	cursor.Account = account
	cursor.Query = query
	if page.Cursor != "" {
		encoded, err := base64.RawURLEncoding.DecodeString(page.Cursor)
		if err != nil || json.Unmarshal(encoded, &cursor) != nil || cursor.Account != account || cursor.Query != query || cursor.Folder == "" {
			return result, errors.New("invalid search cursor")
		}
	}
	sort.Slice(folders, func(left, right int) bool { return folders[left].Folder < folders[right].Folder })
	for index, candidate := range folders {
		if cursor.Folder != "" && candidate.Folder < cursor.Folder {
			continue
		}
		innerCursor := ""
		if candidate.Folder == cursor.Folder {
			innerCursor = cursor.Cursor
		}
		found, err := store.ListMessages(ctx, account, candidate.Folder, apiclient.Page{Limit: limit - len(result.Data), Cursor: innerCursor}, query)
		if err != nil {
			return result, err
		}
		result.Data = append(result.Data, found.Data...)
		if found.Pagination.HasMore || len(result.Data) == limit && index < len(folders)-1 {
			cursor.Folder = candidate.Folder
			cursor.Cursor = found.Pagination.Cursor
			if !found.Pagination.HasMore {
				cursor.Folder = folders[index+1].Folder
				cursor.Cursor = ""
			}
			encoded, _ := json.Marshal(cursor)
			result.Pagination.HasMore = true
			result.Pagination.Cursor = base64.RawURLEncoding.EncodeToString(encoded)
			break
		}
	}
	return result, nil
}

func (store *Store) Folder(ctx context.Context, account uint, method, folder, replacement string) error {
	if folder == "" || strings.ContainsAny(folder+replacement, "\r\n\x00") {
		return fmt.Errorf("invalid folder name")
	}
	return store.withClient(ctx, account, func(client *imapclient.Client) error {
		switch method {
		case "POST":
			return client.Create(folder, nil).Wait()
		case "PATCH":
			return client.Rename(folder, replacement, nil).Wait()
		case "DELETE":
			return client.Delete(folder).Wait()
		}
		return errors.New("unsupported folder operation")
	})
}
