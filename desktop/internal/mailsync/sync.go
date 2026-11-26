package mailsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/rest-mail/rest-mail-server/desktop/internal/apiclient"
	"github.com/rest-mail/rest-mail-server/desktop/internal/localstore"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
)

const DefaultBodyBudget int64 = 32 << 20

type Synchronizer struct {
	Client    *apiclient.Client
	Transport apiclient.Transport
	mu        sync.Mutex
}

func (syncer *Synchronizer) Sync(ctx context.Context, store *localstore.Store, account uint, budget int64) error {
	syncer.mu.Lock()
	defer syncer.mu.Unlock()
	folders, err := syncer.Client.ListFolders(ctx, account)
	if err != nil {
		return err
	}
	if folders == nil {
		return errors.New("mail sync folders missing")
	}
	previous, _ := store.Folders(ctx)
	unchanged := make(map[string]bool)
	for _, folder := range folders {
		for _, old := range previous {
			if folder.Folder == old.Folder && folder.HighestModSeq > 1 && folder.HighestModSeq == old.HighestModSeq && folder.UIDNext == old.UIDNext && folder.Total == old.Total && folder.Unread == old.Unread && store.FolderComplete(ctx, folder.Folder) {
				unchanged[folder.Folder] = true
			}
		}
	}
	if err = store.PutFolders(ctx, folders); err != nil {
		return err
	}
	for _, folder := range folders {
		if unchanged[folder.Folder] {
			continue
		}
		cursor, generation, err := store.BeginScan(ctx, folder.Folder)
		if err != nil {
			return err
		}
		var validity uint32
		for {
			page, err := syncer.Client.ListMessages(ctx, account, folder.Folder, apiclient.Page{Cursor: cursor, Limit: 100, IncludeSnoozed: true}, "")
			if err != nil {
				var apiError *apiclient.Error
				if errors.As(err, &apiError) && apiError.Status == http.StatusBadRequest {
					_ = store.RestartScan(ctx, folder.Folder)
				}
				return err
			}
			if page.Data == nil {
				return errors.New("mail sync message page missing")
			}
			if validity != 0 && page.UIDValidity != validity {
				_ = store.RestartScan(ctx, folder.Folder)
				return errors.New("folder UIDVALIDITY changed during sync")
			}
			validity = page.UIDValidity
			next := ""
			done := page.Pagination == nil || !page.Pagination.HasMore
			if !done {
				next = page.Pagination.Cursor
				if next == "" || next == cursor {
					return errors.New("mail sync cursor did not advance")
				}
			}
			for index := range page.Data {
				if page.Data[index].MailboxID != 0 && page.Data[index].MailboxID != account {
					return errors.New("mail sync response crossed account boundaries")
				}
				page.Data[index].MailboxID = account
				page.Data[index].Folder = folder.Folder
			}
			if err = store.SavePage(ctx, folder.Folder, next, generation, validity, page.Data, done); err != nil {
				return err
			}
			if done {
				break
			}
			cursor = next
		}
	}
	for budget > 0 {
		pending, err := store.Pending(ctx, budget)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			break
		}
		progress := false
		for _, header := range pending {
			if int64(header.SizeBytes) > budget {
				continue
			}
			message, err := syncer.Client.GetMessage(ctx, header.ID)
			if err != nil {
				var apiError *apiclient.Error
				if errors.As(err, &apiError) && apiError.Status == http.StatusNotFound {
					if err = store.Remove(ctx, header.ID); err != nil {
						return err
					}
					continue
				}
				return err
			}
			bytes := int64(len(message.BodyText) + len(message.BodyHTML))
			if message.ID != header.ID || message.MailboxID != 0 && message.MailboxID != account {
				return errors.New("mail sync body identity mismatch")
			}
			if bytes > budget {
				continue
			}
			if message.Folder == "" {
				message.Folder = header.Folder
			}
			if err = syncer.saveBody(ctx, store, message); err != nil {
				return err
			}
			budget -= bytes
			progress = true
		}
		if !progress {
			break
		}
	}
	return nil
}

func (syncer *Synchronizer) Write(ctx context.Context, action func() error) error {
	syncer.mu.Lock()
	defer syncer.mu.Unlock()
	return action()
}

func (syncer *Synchronizer) Apply(ctx context.Context, store *localstore.Store, account, id uint) error {
	syncer.mu.Lock()
	defer syncer.mu.Unlock()
	message, err := syncer.Client.GetMessage(ctx, id)
	if err != nil {
		var apiError *apiclient.Error
		if errors.As(err, &apiError) && apiError.Status == http.StatusNotFound {
			return store.Remove(ctx, id)
		}
		return err
	}
	if message.ID != id || message.MailboxID != 0 && message.MailboxID != account {
		return errors.New("mail event crossed account boundaries")
	}
	message.MailboxID = account
	return syncer.saveBody(ctx, store, message)
}

func (syncer *Synchronizer) saveBody(ctx context.Context, store *localstore.Store, message apitypes.Message) error {
	attachments := []localstore.Attachment{}
	if message.HasAttachments {
		if syncer.Transport == nil {
			return errors.New("mail attachment transport unavailable")
		}
		response, err := syncer.Transport.Do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/messages/%d/attachments", message.ID), nil)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		var result struct {
			Data []localstore.Attachment `json:"data"`
		}
		if response.StatusCode != http.StatusOK {
			return errors.New("mail attachment metadata unavailable")
		}
		if err = json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&result); err != nil {
			return err
		}
		if result.Data == nil {
			return errors.New("mail attachment metadata missing")
		}
		attachments = result.Data
	}
	if err := store.Put(ctx, message, false); err != nil {
		return err
	}
	if err := store.PutAttachments(ctx, message.ID, attachments); err != nil {
		return err
	}
	return store.Put(ctx, message, true)
}
