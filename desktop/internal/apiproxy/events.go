package apiproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rest-mail/rest-mail-server/desktop/internal/session"
)

type MailEvent struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

func MailEvents(ctx context.Context, manager Manager, accountID int64, emit func(MailEvent)) {
	identity := manager.Status()
	lastID := ""
	for ctx.Err() == nil {
		current := manager.Status()
		if !current.SignedIn || current.Server != identity.Server || current.Email != identity.Email {
			return
		}
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d/events", accountID), nil)
		request.Header.Set("Accept", "text/event-stream")
		request.Header.Set("Last-Event-ID", lastID)
		response, err := manager.DoRequest(ctx, request)
		if err == nil {
			if response.StatusCode == http.StatusOK && session.IsEventStream(response.Header.Get("Content-Type")) {
				emit(MailEvent{Type: "stream_ready", Data: map[string]any{}})
				readMailEvents(ctx, response.Body, manager, &lastID, func(event MailEvent) {
					current := manager.Status()
					if current.SignedIn && current.Server == identity.Server && current.Email == identity.Email {
						emit(event)
					}
				})
			}
			response.Body.Close()
			if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusUnauthorized {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func readMailEvents(ctx context.Context, body io.Reader, manager Manager, lastID *string, emit func(MailEvent)) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	var eventType, eventID string
	var payload []string
	for scanner.Scan() && ctx.Err() == nil {
		line := scanner.Text()
		if line == "" {
			if eventID != "" {
				*lastID = eventID
			}
			switch eventType {
			case "new_message", "folder_update", "message_updated", "message_deleted", "message_sent":
				var data map[string]any
				if json.Unmarshal(manager.Redact([]byte(strings.Join(payload, "\n"))), &data) == nil && data != nil {
					emit(MailEvent{Type: eventType, Data: sanitize(data, manager).(map[string]any)})
				}
			}
			eventType, eventID, payload = "", "", nil
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			eventType = value
		case "id":
			if !strings.ContainsRune(value, '\x00') {
				eventID = value
			}
		case "data":
			payload = append(payload, value)
		}
	}
}
