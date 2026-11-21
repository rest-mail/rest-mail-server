package imapmail

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func (store *Store) events(request *http.Request, id uint) *http.Response {
	reader, writer := io.Pipe()
	ctx, cancel := context.WithCancel(request.Context())
	stopPipe := context.AfterFunc(ctx, func() { _ = writer.CloseWithError(ctx.Err()) })
	store.mu.Lock()
	generation := store.watchContext
	store.mu.Unlock()
	stop := context.AfterFunc(generation, cancel)
	go func() {
		defer writer.Close()
		defer cancel()
		defer stop()
		defer stopPipe()
		store.watch(ctx, writer, id)
	}()
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: &eventBody{PipeReader: reader, cancel: cancel}}
}

type eventBody struct {
	*io.PipeReader
	cancel context.CancelFunc
}

func (body *eventBody) Close() error { body.cancel(); return body.PipeReader.Close() }

func waitPoll(ctx context.Context) bool {
	timer := time.NewTimer(60 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (store *Store) watch(ctx context.Context, writer io.Writer, id uint) {
	if _, err := io.WriteString(writer, ": ready\n\n"); err != nil {
		return
	}
	var previous imap.UID
	for ctx.Err() == nil {
		account, exists := store.Account(id)
		if !exists {
			return
		}
		password, err := keyringPassword(id)
		if err != nil {
			return
		}
		changes := make(chan struct{}, 1)
		handler := &imapclient.UnilateralDataHandler{Mailbox: func(_ *imapclient.UnilateralDataMailbox) {
			select {
			case changes <- struct{}{}:
			default:
			}
		}}
		client, conn, err := store.dial(ctx, account, password, handler)
		if err != nil {
			if !waitPoll(ctx) {
				return
			}
			continue
		}
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		err = func() error {
			selected, err := client.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait()
			if err != nil {
				return err
			}
			if previous == 0 {
				previous = selected.UIDNext
			}
			for ctx.Err() == nil {
				_ = conn.SetDeadline(time.Now().Add(75 * time.Second))
				if client.Caps().Has(imap.CapIdle) {
					idle, err := client.Idle()
					if err != nil {
						return err
					}
					timer := time.NewTimer(60 * time.Second)
					select {
					case <-ctx.Done():
					case <-changes:
					case <-timer.C:
					}
					timer.Stop()
					if err := idle.Close(); err != nil {
						return err
					}
					if err := idle.Wait(); err != nil {
						return err
					}
				} else if !waitPoll(ctx) {
					return ctx.Err()
				}
				if _, exists := store.Account(id); !exists {
					return context.Canceled
				}
				status, err := client.Status("INBOX", &imap.StatusOptions{UIDNext: true}).Wait()
				if err != nil {
					return err
				}
				kind := "folder_update"
				if status.UIDNext > previous {
					kind = "new_message"
				}
				previous = status.UIDNext
				payload := map[string]any{"account_id": id, "folder": "INBOX"}
				if kind == "new_message" && status.UIDNext > 1 {
					messages, err := client.Fetch(imap.UIDSetNum(status.UIDNext-1), &imap.FetchOptions{Envelope: true}).Collect()
					if err != nil {
						return err
					}
					if len(messages) > 0 && messages[0].Envelope != nil {
						envelope := messages[0].Envelope
						payload["subject"] = envelope.Subject
						if len(envelope.From) > 0 {
							payload["sender"] = envelope.From[0].Addr()
						}
					}
				}
				data, _ := json.Marshal(payload)
				if _, err := fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", kind, store.Redact(data)); err != nil {
					return err
				}
			}
			return ctx.Err()
		}()
		stop()
		_ = conn.Close()
		_ = client.Close()
		if err != nil && !waitPoll(ctx) {
			return
		}
	}
}
