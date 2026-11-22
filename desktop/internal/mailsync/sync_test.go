package mailsync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/desktop/internal/apiclient"
	"github.com/rest-mail/rest-mail-server/desktop/internal/localstore"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
)

type restFixture struct {
	mu         sync.Mutex
	server     *httptest.Server
	messages   map[uint]apitypes.Message
	failCursor bool
	cursors    []string
}

func (fixture *restFixture) Do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, fixture.server.URL+path, body)
	if err != nil {
		return nil, err
	}
	return fixture.server.Client().Do(request)
}

func newFixture(t *testing.T) *restFixture {
	t.Helper()
	fixture := &restFixture{messages: make(map[uint]apitypes.Message)}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		path := request.URL.Path
		switch {
		case path == "/api/v1/accounts/1/folders":
			json.NewEncoder(writer).Encode(map[string]any{"data": []apitypes.FolderSummary{{Folder: "INBOX"}, {Folder: "Archive"}}})
		case strings.HasSuffix(path, "/messages") && strings.Contains(path, "/folders/"):
			parts := strings.Split(path, "/")
			folder := parts[6]
			cursor := request.URL.Query().Get("cursor")
			fixture.cursors = append(fixture.cursors, cursor)
			if fixture.failCursor && cursor != "" {
				writer.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			offset, _ := strconv.Atoi(cursor)
			messages := []apitypes.Message{}
			for _, message := range fixture.messages {
				if message.Folder == folder {
					message.BodyText = ""
					message.BodyHTML = ""
					messages = append(messages, message)
				}
			}
			sort.Slice(messages, func(left, right int) bool { return messages[left].ID > messages[right].ID })
			end := min(offset+2, len(messages))
			if offset > len(messages) {
				writer.WriteHeader(400)
				return
			}
			page := apiclient.MessagePage{Data: messages[offset:end], Pagination: &apiclient.Pagination{HasMore: end < len(messages)}}
			if page.Pagination.HasMore {
				page.Pagination.Cursor = strconv.Itoa(end)
			}
			json.NewEncoder(writer).Encode(page)
		case strings.HasPrefix(path, "/api/v1/messages/"):
			id, _ := strconv.ParseUint(strings.TrimPrefix(path, "/api/v1/messages/"), 10, 64)
			message, found := fixture.messages[uint(id)]
			if !found {
				writer.WriteHeader(404)
				return
			}
			json.NewEncoder(writer).Encode(map[string]any{"data": message})
		default:
			writer.WriteHeader(404)
		}
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func synthetic(id uint) apitypes.Message {
	return apitypes.Message{ID: id, MailboxID: 1, Folder: "INBOX", Subject: fmt.Sprintf("arrival %d", id), BodyText: strings.Repeat("synthetic body ", 10), SizeBytes: 150, ReceivedAt: time.Now().UTC()}
}

func TestRESTSyncHeadersBodiesFlagsMovesDeletesAndBudget(t *testing.T) {
	fixture := newFixture(t)
	for id := uint(1); id <= 5; id++ {
		fixture.messages[id] = synthetic(id)
	}
	store, err := localstore.Open(t.TempDir(), localstore.AccountKey("server", "owner", 1))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	syncer := Synchronizer{Client: apiclient.New(fixture), Transport: fixture}
	ctx := context.Background()
	if err = syncer.Sync(ctx, store, 1, 150); err != nil {
		t.Fatal(err)
	}
	status, err := store.Status(ctx)
	if err != nil || status.Messages != 5 || status.Bodies != 1 || status.Complete {
		t.Fatalf("budget: %+v %v", status, err)
	}
	if err = syncer.Sync(ctx, store, 1, DefaultBodyBudget); err != nil {
		t.Fatal(err)
	}
	status, err = store.Status(ctx)
	if err != nil || status.Bodies != 5 || !status.Complete {
		t.Fatalf("body backfill: %+v %v", status, err)
	}
	fixture.mu.Lock()
	fixture.messages[6] = synthetic(6)
	flagged := fixture.messages[2]
	flagged.IsRead = true
	flagged.IsFlagged = true
	fixture.messages[2] = flagged
	moved := fixture.messages[3]
	moved.Folder = "Archive"
	fixture.messages[3] = moved
	delete(fixture.messages, 1)
	fixture.mu.Unlock()
	if err = syncer.Sync(ctx, store, 1, DefaultBodyBudget); err != nil {
		t.Fatal(err)
	}
	result, err := store.Search(ctx, "is:unread in:INBOX", "", 50, 0)
	if err != nil || len(result.Data) != 3 {
		t.Fatalf("incremental arrivals/flags: %+v %v", result, err)
	}
	message, err := store.Get(ctx, 2, true)
	if err != nil || !message.IsFlagged || !message.IsRead {
		t.Fatalf("flags: %+v %v", message, err)
	}
	message, err = store.Get(ctx, 3, true)
	if err != nil || message.Folder != "Archive" {
		t.Fatalf("move: %+v %v", message, err)
	}
	result, err = store.Search(ctx, "subject:arrival", "", 50, 0)
	if err != nil || len(result.Data) != 5 {
		t.Fatalf("delete: %+v %v", result, err)
	}
}

func TestRESTSyncResumesCheckpointAfterRestart(t *testing.T) {
	fixture := newFixture(t)
	for id := uint(1); id <= 5; id++ {
		fixture.messages[id] = synthetic(id)
	}
	fixture.failCursor = true
	root := t.TempDir()
	key := localstore.AccountKey("server", "owner", 1)
	store, err := localstore.Open(root, key)
	if err != nil {
		t.Fatal(err)
	}
	syncer := Synchronizer{Client: apiclient.New(fixture)}
	if err = syncer.Sync(context.Background(), store, 1, 0); err == nil {
		t.Fatal("ignored interrupted header page")
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = localstore.Open(root, key)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fixture.mu.Lock()
	fixture.failCursor = false
	fixture.cursors = nil
	fixture.mu.Unlock()
	if err = syncer.Sync(context.Background(), store, 1, DefaultBodyBudget); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	first := fixture.cursors[0]
	fixture.mu.Unlock()
	if first != "2" {
		t.Fatalf("did not resume saved cursor: %q", first)
	}
	status, err := store.Status(context.Background())
	if err != nil || status.Messages != 5 || status.Bodies != 5 {
		t.Fatalf("resume: %+v %v", status, err)
	}
}
