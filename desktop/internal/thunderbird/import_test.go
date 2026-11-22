package thunderbird

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	shared "github.com/rest-mail/rest-mail-server/pkg/mailimport"
)

func settled(t *testing.T, service *Service) Status {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		status := service.Status(false)
		if status.State != "running" && status.State != "cancelling" {
			return status
		}
		select {
		case <-deadline.C:
			t.Fatal("import did not finish")
		case <-ticker.C:
		}
	}
}

func testService(t *testing.T, request shared.Request) (*Service, Profile) {
	t.Helper()
	_, profiles := fixture(t)
	service := New(request, filepath.Join(t.TempDir(), "progress.json"))
	service.discover = func() ([]Profile, error) { return profiles, nil }
	service.running = func() bool { return false }
	t.Cleanup(service.Close)
	service.Profiles()
	return service, profiles[0]
}

func TestImportHTTPPreservesMetadataDeduplicatesAndRecordsProgress(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}
	opens, closes := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(request.URL.Path, "/import-sessions"):
			opens++
			writer.WriteHeader(http.StatusCreated)
			io.WriteString(writer, `{"data":{"id":"0123456789abcdef0123456789abcdef"}}`)
		case request.Method == http.MethodDelete:
			closes++
			io.WriteString(writer, `{"data":{}}`)
		default:
			if request.URL.Query().Get("import_session") != "0123456789abcdef0123456789abcdef" {
				t.Error("session missing")
			}
			var message shared.Message
			if err := json.NewDecoder(request.Body).Decode(&message); err != nil {
				t.Error(err)
			}
			if message.InternalDate.IsZero() || strings.Contains(string(message.RawMessage), "X-Mozilla") {
				t.Error("source metadata not extracted")
			}
			if message.Folder == "INBOX" && (len(message.Flags) != 1 || message.Flags[0] != `\Seen`) {
				t.Error("read flag lost")
			}
			key := message.Folder + string(message.RawMessage)
			duplicate := seen[key]
			seen[key] = true
			json.NewEncoder(writer).Encode(map[string]any{"data": shared.Result{ID: 1, MessageID: "synthetic", Folder: message.Folder, Imported: !duplicate}})
		}
	}))
	defer server.Close()
	service, profile := testService(t, func(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
		request, err := http.NewRequestWithContext(ctx, method, server.URL+path, body)
		if err != nil {
			return nil, err
		}
		return server.Client().Do(request)
	})
	selection := Selection{ProfileID: profile.ID, AccountID: 7}
	before := map[string][32]byte{}
	for _, folder := range profile.Folders {
		selection.Folders = append(selection.Folders, folder.ID)
		data, err := os.ReadFile(filepath.Join(profile.path, folder.ID))
		if err != nil {
			t.Fatal(err)
		}
		before[folder.ID] = sha256.Sum256(data)
	}
	for run := 0; run < 2; run++ {
		if _, err := service.Start(selection); err != nil {
			t.Fatal(err)
		}
		status := settled(t, service)
		if status.State != "completed" || status.Failed != 0 || status.Messages != 3 || status.Expunged != 1 || status.Bytes != status.TotalBytes {
			t.Fatalf("state=%s processed=%d expunged=%d failed=%d", status.State, status.Messages, status.Expunged, status.Failed)
		}
		if run == 0 && status.Imported != 3 || run == 1 && status.Duplicates != 3 {
			t.Fatal("deduplication result lost")
		}
	}
	for _, folder := range profile.Folders {
		data, _ := os.ReadFile(filepath.Join(profile.path, folder.ID))
		if before[folder.ID] != sha256.Sum256(data) {
			t.Fatal("source modified")
		}
	}
	mu.Lock()
	if opens != 2 || closes != 2 {
		t.Errorf("sessions opened=%d closed=%d", opens, closes)
	}
	mu.Unlock()
	info, err := os.Stat(service.progressFile)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("progress must be private")
	}
	restored := New(nil, service.progressFile)
	defer restored.Close()
	if restored.Status(false).Duplicates != 3 {
		t.Fatal("progress not restored")
	}
}

func TestImportCancelClosesSessionAndRejectsConcurrentRun(t *testing.T) {
	started := make(chan struct{})
	closed := make(chan struct{})
	service, profile := testService(t, func(ctx context.Context, method, path string, _ io.Reader) (*http.Response, error) {
		body := `{"data":{"id":"0123456789abcdef0123456789abcdef"}}`
		status := http.StatusCreated
		if method == http.MethodDelete {
			close(closed)
			body = `{"data":{}}`
			status = http.StatusOK
		} else if strings.Contains(path, "/import?") {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	selection := Selection{ProfileID: profile.ID, AccountID: 7, Folders: []string{profile.Folders[0].ID}}
	if _, err := service.Start(selection); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("upload not reached")
	}
	if _, err := service.Start(selection); err == nil {
		t.Fatal("concurrent run allowed")
	}
	service.Status(true)
	status := settled(t, service)
	if status.State != "cancelled" || status.Imported != 0 || status.Failed != 0 {
		t.Fatalf("state=%s imported=%d failed=%d", status.State, status.Imported, status.Failed)
	}
	select {
	case <-closed:
	default:
		t.Fatal("session not closed after cancellation")
	}
}

func TestImportValidationSourceChangeAndFailureReasons(t *testing.T) {
	service, profile := testService(t, func(_ context.Context, method, path string, _ io.Reader) (*http.Response, error) {
		if method == http.MethodPost && strings.HasSuffix(path, "/import-sessions") {
			return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(`{"data":{"id":"0123456789abcdef0123456789abcdef"}}`))}, nil
		}
		if method == http.MethodDelete {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":{}}`))}, nil
		}
		return &http.Response{StatusCode: 413, Body: io.NopCloser(strings.NewReader(`private upstream body`))}, nil
	})
	for _, selection := range []Selection{{}, {ProfileID: "unknown", AccountID: 7, Folders: []string{"x"}}, {ProfileID: profile.ID, AccountID: 7, Folders: []string{"../elsewhere"}}} {
		if _, err := service.Start(selection); err == nil {
			t.Fatal("invalid selection accepted")
		}
	}
	selection := Selection{ProfileID: profile.ID, AccountID: 7, Folders: []string{profile.Folders[0].ID}}
	if _, err := service.Start(selection); err != nil {
		t.Fatal(err)
	}
	status := settled(t, service)
	if status.State != "failed" || status.Failed != 1 || !strings.Contains(status.Failures[0].Reason, "HTTP 413") || strings.Contains(status.Failures[0].Reason, "private") {
		t.Fatal("safe failure not recorded")
	}
	file, err := os.OpenFile(filepath.Join(profile.path, profile.Folders[0].ID), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	file.WriteString("changed\n")
	file.Close()
	if _, err := service.Start(selection); err != nil {
		t.Fatal(err)
	}
	status = settled(t, service)
	if status.Failed != 1 || !strings.Contains(status.Failures[0].Reason, "Source changed") {
		t.Fatal("changed source not refused")
	}
}

func TestInterruptedProgressSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "progress.json")
	data, _ := json.Marshal(Status{State: "running", Messages: 12, Selection: Selection{ProfileID: "sample", AccountID: 7, Folders: []string{"Inbox"}}})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	service := New(nil, path)
	defer service.Close()
	status := service.Status(false)
	if status.State != "interrupted" || status.Messages != 12 || status.Selection.AccountID != 7 {
		t.Fatal("checkpoint lost")
	}
}
