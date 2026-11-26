package localmail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/desktop/internal/apiclient"
	"github.com/rest-mail/rest-mail-server/desktop/internal/imapmail"
	"github.com/rest-mail/rest-mail-server/desktop/internal/localstore"
	"github.com/rest-mail/rest-mail-server/desktop/internal/session"
	apitypes "github.com/rest-mail/rest-mail-server/pkg/apitypes"
)

type backendFixture struct {
	mu          sync.Mutex
	server      *httptest.Server
	status      session.SessionStatus
	messages    map[uint]apitypes.Message
	offline     bool
	events      chan string
	searches    int
	rejectWrite bool
}

func (backend *backendFixture) Status() session.SessionStatus {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.status
}
func (backend *backendFixture) ListAccounts() []imapmail.Account {
	return []imapmail.Account{{ID: 1, Address: backend.Status().Email, Type: "restmail"}}
}
func (backend *backendFixture) Redact(data []byte) []byte {
	return bytes.ReplaceAll(data, []byte("credential-secret"), []byte("[redacted]"))
}
func (backend *backendFixture) DoRequest(ctx context.Context, request *http.Request) (*http.Response, error) {
	backend.mu.Lock()
	offline := backend.offline
	backend.mu.Unlock()
	if offline {
		return nil, errors.New("offline")
	}
	copy := request.Clone(ctx)
	target := *request.URL
	copy.URL = &target
	copy.URL.Scheme = "http"
	copy.URL.Host = strings.TrimPrefix(backend.server.URL, "http://")
	copy.RequestURI = ""
	return backend.server.Client().Do(copy)
}

func newBackend(t *testing.T) *backendFixture {
	t.Helper()
	backend := &backendFixture{status: session.SessionStatus{SignedIn: true, Server: "http://localhost", Email: "reader@test", AccountID: 1}, messages: make(map[uint]apitypes.Message), events: make(chan string, 20)}
	backend.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/events") {
			writer.Header().Set("Content-Type", "text/event-stream")
			writer.WriteHeader(200)
			writer.(http.Flusher).Flush()
			for {
				select {
				case <-request.Context().Done():
					return
				case event := <-backend.events:
					fmt.Fprint(writer, event)
					writer.(http.Flusher).Flush()
				}
			}
		}
		backend.mu.Lock()
		defer backend.mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case request.URL.Path == "/api/v1/accounts":
			json.NewEncoder(writer).Encode(map[string]any{"data": []map[string]any{{"id": 1, "address": "reader@test", "is_primary": true}}})
		case request.URL.Path == "/api/v1/accounts/1/folders":
			json.NewEncoder(writer).Encode(map[string]any{"data": []apitypes.FolderSummary{{Folder: "INBOX"}, {Folder: "Archive"}}})
		case strings.Contains(request.URL.Path, "/folders/"):
			parts := strings.Split(request.URL.Path, "/")
			folder := parts[6]
			messages := []apitypes.Message{}
			for _, message := range backend.messages {
				if message.Folder == folder {
					message.BodyText = ""
					messages = append(messages, message)
				}
			}
			json.NewEncoder(writer).Encode(map[string]any{"data": messages})
		case strings.HasSuffix(request.URL.Path, "/search"):
			backend.searches++
			json.NewEncoder(writer).Encode(map[string]any{"data": []apitypes.Message{{ID: 999, Folder: "INBOX", Subject: "only remote"}}})
		case strings.HasPrefix(request.URL.Path, "/api/v1/messages/"):
			id, _ := strconv.ParseUint(strings.TrimPrefix(request.URL.Path, "/api/v1/messages/"), 10, 53)
			message, found := backend.messages[uint(id)]
			if !found {
				writer.WriteHeader(404)
				return
			}
			if request.Method == http.MethodPatch {
				if backend.rejectWrite {
					writer.WriteHeader(503)
					return
				}
				var updates apitypes.UpdateMessageBody
				if err := json.NewDecoder(request.Body).Decode(&updates); err != nil {
					writer.WriteHeader(400)
					return
				}
				if updates.IsRead != nil {
					message.IsRead = *updates.IsRead
				}
				if updates.IsFlagged != nil {
					message.IsFlagged = *updates.IsFlagged
				}
				if updates.Folder != nil {
					message.Folder = *updates.Folder
				}
				backend.messages[uint(id)] = message
			}
			if request.Method == http.MethodDelete {
				delete(backend.messages, uint(id))
				writer.WriteHeader(204)
				return
			}
			json.NewEncoder(writer).Encode(map[string]any{"data": message})
		default:
			writer.WriteHeader(404)
		}
	}))
	t.Cleanup(backend.server.Close)
	return backend
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("local sync condition did not converge within five seconds")
}

func newService(t *testing.T, backend *backendFixture) *Service {
	t.Helper()
	service, err := New(backend, t.TempDir(), "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Error(err)
		}
	})
	if err = service.Activate(); err != nil {
		t.Fatal(err)
	}
	return service
}

func testMessage(id uint) apitypes.Message {
	return apitypes.Message{ID: id, MailboxID: 1, Folder: "INBOX", Subject: fmt.Sprintf("Café arrival%d", id), BodyText: "offline body", SizeBytes: 12, ReceivedAt: time.Now().UTC()}
}

func TestSSEArrivalSearchOfflineReadsAndWriteThrough(t *testing.T) {
	backend := newBackend(t)
	backend.messages[1] = testMessage(1)
	service := newService(t, backend)
	ctx := t.Context()
	client := apiclient.New(service)
	waitFor(t, func() bool { status, err := service.IndexStatus(ctx, 1); return err == nil && status.Bodies == 1 })
	backend.mu.Lock()
	backend.messages[2] = testMessage(2)
	backend.mu.Unlock()
	backend.events <- "event: new_message\nid: 2\ndata: {\"id\":2}\n\n"
	waitFor(t, func() bool {
		result, err := service.LocalSearch(ctx, 1, "arrival2", "")
		return err == nil && len(result.Data) == 1
	})
	read := true
	if err := client.SetFlags(ctx, []uint{1}, apitypes.UpdateMessageBody{IsRead: &read, IsFlagged: &read}); err != nil {
		t.Fatal(err)
	}
	result, err := service.LocalSearch(ctx, 1, "is:unread", "")
	if err != nil || len(result.Data) != 1 || result.Data[0].ID != 2 {
		t.Fatalf("write-through flags: %+v %v", result, err)
	}
	if err = client.MoveMessages(ctx, []uint{1}, "Archive"); err != nil {
		t.Fatal(err)
	}
	result, err = service.LocalSearch(ctx, 1, "in:Archive", "")
	if err != nil || len(result.Data) != 1 {
		t.Fatalf("write-through move: %+v %v", result, err)
	}
	backend.mu.Lock()
	backend.rejectWrite = true
	backend.mu.Unlock()
	if err = client.MoveMessages(ctx, []uint{2}, "Archive"); err == nil {
		t.Fatal("failed upstream mutation reported success")
	}
	result, err = service.LocalSearch(ctx, 1, "in:INBOX", "")
	if err != nil || len(result.Data) != 1 {
		t.Fatalf("failed write modified cache: %+v %v", result, err)
	}
	backend.mu.Lock()
	backend.rejectWrite = false
	backend.offline = true
	backend.mu.Unlock()
	service.Wake(1)
	waitFor(t, func() bool { status, err := service.IndexStatus(ctx, 1); return err == nil && status.Offline })
	message, err := client.GetMessage(ctx, 2)
	if err != nil || message.BodyText != "offline body" {
		t.Fatalf("offline read: %+v %v", message, err)
	}
	folders, err := client.ListFolders(ctx, 1)
	if err != nil || len(folders) != 2 {
		t.Fatalf("offline folders: %+v %v", folders, err)
	}
	page, err := client.ListMessages(ctx, 1, "INBOX", apiclient.Page{}, "")
	if err != nil || len(page.Data) != 1 {
		t.Fatalf("offline list: %+v %v", page, err)
	}
	result, err = service.LocalSearch(ctx, 1, "cafe", "")
	if err != nil || len(result.Data) != 2 || !result.Offline {
		t.Fatalf("offline search: %+v %v", result, err)
	}
	backend.mu.Lock()
	backend.offline = false
	backend.mu.Unlock()
	if err = client.DeleteMessages(ctx, []uint{2}, true); err != nil {
		t.Fatal(err)
	}
	result, err = service.LocalSearch(ctx, 1, "arrival2", "")
	if err != nil || len(result.Data) != 0 {
		t.Fatalf("write-through delete: %+v %v", result, err)
	}
}

func TestSearchLocalFirstRemoteSupplementAndCredentialRedaction(t *testing.T) {
	backend := newBackend(t)
	message := testMessage(1)
	message.Subject += " credential-secret"
	message.SizeBytes = int(DefaultBodyBudgetForTest)
	backend.messages[1] = message
	service := newService(t, backend)
	ctx := t.Context()
	waitFor(t, func() bool { status, err := service.IndexStatus(ctx, 1); return err == nil && status.Messages == 1 })
	response, err := service.Do(ctx, "GET", "/api/v1/accounts/1/search?q=cafe", nil)
	if err != nil {
		t.Fatal(err)
	}
	var result localstore.Results
	json.NewDecoder(response.Body).Decode(&result)
	response.Body.Close()
	if result.Source != "local" || len(result.Data) != 1 || strings.Contains(result.Data[0].Subject, "credential-secret") {
		t.Fatalf("local/redaction: %+v", result)
	}
	backend.mu.Lock()
	searches := backend.searches
	backend.mu.Unlock()
	if searches != 0 {
		t.Fatal("local result waited for server search")
	}
	response, err = service.Do(ctx, "GET", "/api/v1/accounts/1/search?q=cafe&desktop_remote=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	json.NewDecoder(response.Body).Decode(&result)
	response.Body.Close()
	if result.Source != "local+server" || len(result.Data) != 2 || result.Data[1].Source != "server" {
		t.Fatalf("remote supplement: %+v", result)
	}
	service.mu.Lock()
	account := service.accounts[1]
	service.mu.Unlock()
	for _, path := range []string{account.store.Path(), account.store.Path() + "-wal"} {
		data, readError := os.ReadFile(path)
		if readError != nil && !os.IsNotExist(readError) {
			t.Fatal(readError)
		}
		if bytes.Contains(data, []byte("credential-secret")) {
			t.Fatal("credential persisted in SQLite")
		}
	}
}

const DefaultBodyBudgetForTest = 64 << 20

func TestSignOutIsolationRemovalAndTemporaryCleanup(t *testing.T) {
	backend := newBackend(t)
	backend.messages[1] = testMessage(1)
	service := newService(t, backend)
	ctx := t.Context()
	waitFor(t, func() bool { status, err := service.IndexStatus(ctx, 1); return err == nil && status.Bodies == 1 })
	root := service.root
	if err := service.Forget(1); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(root, "*.sqlite*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("account data remains: %v %v", files, err)
	}
	backend.mu.Lock()
	backend.status.SignedIn = false
	backend.mu.Unlock()
	if _, err = service.Do(ctx, "GET", "/api/v1/messages/1", nil); !errors.Is(err, session.ErrSignedOut) {
		t.Fatalf("signed-out cached read: %v", err)
	}
	if err = service.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("temporary store remains: %v", err)
	}
}
