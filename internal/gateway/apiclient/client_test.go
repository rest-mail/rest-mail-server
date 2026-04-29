package apiclient

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestServer creates an httptest.Server with a mux for registering handlers.
func newTestServer(t *testing.T) (*httptest.Server, *http.ServeMux) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, mux
}

// ── Constructor ──────────────────────────────────────────────────────

func TestNew(t *testing.T) {
	c := New("http://localhost:8080")
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if c.baseURL != "http://localhost:8080" {
		t.Fatalf("expected baseURL 'http://localhost:8080', got %q", c.baseURL)
	}
	if c.httpClient == nil {
		t.Fatal("expected non-nil httpClient")
	}
}

// ── APIError ─────────────────────────────────────────────────────────

func TestAPIError_Error(t *testing.T) {
	e := &APIError{StatusCode: 403, Body: `{"error":"forbidden"}`}
	msg := e.Error()
	if !strings.Contains(msg, "403") {
		t.Fatalf("expected '403' in error message, got %q", msg)
	}
	if !strings.Contains(msg, "forbidden") {
		t.Fatalf("expected 'forbidden' in error message, got %q", msg)
	}
}

// ── Login ────────────────────────────────────────────────────────────

func TestLogin_Success(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %q", ct)
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["email"] != "user@test.com" {
			t.Errorf("expected email user@test.com, got %q", body["email"])
		}
		if body["password"] != "secret" {
			t.Errorf("expected password 'secret', got %q", body["password"])
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"access_token": "tok-123",
				"expires_in":   3600,
				"user": map[string]interface{}{
					"id":           1,
					"email":        "user@test.com",
					"display_name": "Test User",
				},
			},
		})
	})

	c := New(srv.URL)
	resp, err := c.Login("user@test.com", "secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Data.AccessToken != "tok-123" {
		t.Fatalf("expected token 'tok-123', got %q", resp.Data.AccessToken)
	}
	if resp.Data.ExpiresIn != 3600 {
		t.Fatalf("expected expires_in 3600, got %d", resp.Data.ExpiresIn)
	}
	if resp.Data.User.Email != "user@test.com" {
		t.Fatalf("expected email 'user@test.com', got %q", resp.Data.User.Email)
	}
}

func TestLogin_Unauthorized(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid credentials"}`))
	})

	c := New(srv.URL)
	_, err := c.Login("user@test.com", "wrong")
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.StatusCode != 401 {
		t.Fatalf("expected status 401, got %d", apiErr.StatusCode)
	}
}

// ── CheckMailbox ─────────────────────────────────────────────────────

func TestCheckMailbox_Exists(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/mailboxes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		addr := r.URL.Query().Get("address")
		if addr != "user@mail1.test" {
			t.Errorf("expected address 'user@mail1.test', got %q", addr)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"exists":     true,
				"mailbox_id": 42,
				"address":    "user@mail1.test",
			},
		})
	})

	c := New(srv.URL)
	resp, err := c.CheckMailbox("user@mail1.test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Data.Exists {
		t.Fatal("expected mailbox to exist")
	}
	if resp.Data.MailboxID != 42 {
		t.Fatalf("expected mailbox_id 42, got %d", resp.Data.MailboxID)
	}
}

func TestCheckMailbox_NotExists(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/mailboxes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"exists": false,
			},
		})
	})

	c := New(srv.URL)
	resp, err := c.CheckMailbox("nobody@mail1.test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Data.Exists {
		t.Fatal("expected mailbox to not exist")
	}
}

// ── DeliverMessage ───────────────────────────────────────────────────

func TestDeliverMessage_Success(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/messages/deliver", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		var req DeliverRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Address != "user@mail1.test" {
			t.Errorf("expected address 'user@mail1.test', got %q", req.Address)
		}
		if req.Subject != "Hello" {
			t.Errorf("expected subject 'Hello', got %q", req.Subject)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"id":         100,
				"mailbox_id": 42,
				"subject":    "Hello",
			},
		})
	})

	c := New(srv.URL)
	resp, err := c.DeliverMessage(&DeliverRequest{
		Address:  "user@mail1.test",
		Sender:   "sender@example.com",
		Subject:  "Hello",
		BodyText: "Hello world",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Data.ID != 100 {
		t.Fatalf("expected message ID 100, got %d", resp.Data.ID)
	}
	if resp.Data.Subject != "Hello" {
		t.Fatalf("expected subject 'Hello', got %q", resp.Data.Subject)
	}
}

func TestDeliverMessage_ServerError(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/messages/deliver", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal server error"}`))
	})

	c := New(srv.URL)
	_, err := c.DeliverMessage(&DeliverRequest{Address: "user@mail1.test"})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.StatusCode != 500 {
		t.Fatalf("expected status 500, got %d", apiErr.StatusCode)
	}
}

// ── SendMessage ──────────────────────────────────────────────────────

func TestSendMessage_Success(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/messages/send", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer my-token" {
			t.Errorf("expected 'Bearer my-token', got %q", auth)
		}
		var req SendRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.From != "sender@test.com" {
			t.Errorf("expected from 'sender@test.com', got %q", req.From)
		}
		if len(req.To) != 1 || req.To[0] != "recipient@test.com" {
			t.Errorf("expected to ['recipient@test.com'], got %v", req.To)
		}
		w.WriteHeader(http.StatusOK)
	})

	c := New(srv.URL)
	err := c.SendMessage("my-token", &SendRequest{
		From:     "sender@test.com",
		To:       []string{"recipient@test.com"},
		Subject:  "Test",
		BodyText: "Test body",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSendMessage_Forbidden(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/messages/send", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	})

	c := New(srv.URL)
	err := c.SendMessage("bad-token", &SendRequest{From: "a@b.com", To: []string{"c@d.com"}})
	if err == nil {
		t.Fatal("expected error for 403 response")
	}
}

// ── ListFolders ──────────────────────────────────────────────────────

func TestListFolders_Success(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/accounts/1/folders", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer tok" {
			t.Errorf("expected 'Bearer tok', got %q", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{
				{"name": "INBOX", "total": 10, "unread": 3},
				{"name": "Sent", "total": 5, "unread": 0},
			},
		})
	})

	c := New(srv.URL)
	resp, err := c.ListFolders("tok", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 folders, got %d", len(resp.Data))
	}
	if resp.Data[0].Name != "INBOX" {
		t.Fatalf("expected first folder 'INBOX', got %q", resp.Data[0].Name)
	}
	if resp.Data[0].Total != 10 {
		t.Fatalf("expected total 10, got %d", resp.Data[0].Total)
	}
	if resp.Data[0].Unread != 3 {
		t.Fatalf("expected unread 3, got %d", resp.Data[0].Unread)
	}
}

// ── ListMessages ─────────────────────────────────────────────────────

func TestListMessages_Success(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/accounts/5/folders/INBOX/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "100" {
			t.Errorf("expected limit=100, got %q", r.URL.Query().Get("limit"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{
				{
					"id":      1,
					"subject": "Test Message",
					"sender":  "alice@test.com",
					"folder":  "INBOX",
					"is_read": false,
				},
			},
			"pagination": map[string]interface{}{
				"cursor":   "",
				"has_more": false,
				"total":    1,
			},
		})
	})

	c := New(srv.URL)
	resp, err := c.ListMessages("tok", 5, "INBOX")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 message, got %d", len(resp.Data))
	}
	if resp.Data[0].Subject != "Test Message" {
		t.Fatalf("expected subject 'Test Message', got %q", resp.Data[0].Subject)
	}
	if resp.Pagination == nil {
		t.Fatal("expected pagination to be present")
	}
	if resp.Pagination.Total != 1 {
		t.Fatalf("expected pagination total 1, got %d", resp.Pagination.Total)
	}
}

// ── GetMessage ───────────────────────────────────────────────────────

func TestGetMessage_Success(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/messages/42", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"id":        42,
				"subject":   "Detailed Message",
				"body_text": "Hello",
				"body_html": "<p>Hello</p>",
				"sender":    "alice@test.com",
				"folder":    "INBOX",
			},
		})
	})

	c := New(srv.URL)
	resp, err := c.GetMessage("tok", 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Data.ID != 42 {
		t.Fatalf("expected message ID 42, got %d", resp.Data.ID)
	}
	if resp.Data.BodyText != "Hello" {
		t.Fatalf("expected body_text 'Hello', got %q", resp.Data.BodyText)
	}
	if resp.Data.BodyHTML != "<p>Hello</p>" {
		t.Fatalf("expected body_html '<p>Hello</p>', got %q", resp.Data.BodyHTML)
	}
}

func TestGetMessage_NotFound(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/messages/999", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	})

	c := New(srv.URL)
	_, err := c.GetMessage("tok", 999)
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.StatusCode != 404 {
		t.Fatalf("expected status 404, got %d", apiErr.StatusCode)
	}
}

// ── UpdateMessage ────────────────────────────────────────────────────

func TestUpdateMessage_Success(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/messages/10", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("expected PATCH, got %s", r.Method)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer tok" {
			t.Errorf("expected 'Bearer tok', got %q", auth)
		}
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["is_read"] != true {
			t.Errorf("expected is_read=true, got %v", body["is_read"])
		}
		w.WriteHeader(http.StatusOK)
	})

	c := New(srv.URL)
	err := c.UpdateMessage("tok", 10, map[string]interface{}{"is_read": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── DeleteMessage ────────────────────────────────────────────────────

func TestDeleteMessage_Success(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/messages/10", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer tok" {
			t.Errorf("expected 'Bearer tok', got %q", auth)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	c := New(srv.URL)
	err := c.DeleteMessage("tok", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeleteMessage_Unauthorized(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/messages/10", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	})

	c := New(srv.URL)
	err := c.DeleteMessage("bad-tok", 10)
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
}

// ── GetQuota ─────────────────────────────────────────────────────────

func TestGetQuota_Success(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/accounts/1/quota", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"quota_bytes":      1073741824,
				"quota_used_bytes": 536870912,
				"message_count":    150,
				"percent_used":     50.0,
			},
		})
	})

	c := New(srv.URL)
	resp, err := c.GetQuota("tok", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Data.QuotaBytes != 1073741824 {
		t.Fatalf("expected quota_bytes 1073741824, got %d", resp.Data.QuotaBytes)
	}
	if resp.Data.PercentUsed != 50.0 {
		t.Fatalf("expected percent_used 50.0, got %f", resp.Data.PercentUsed)
	}
	if resp.Data.MessageCount != 150 {
		t.Fatalf("expected message_count 150, got %d", resp.Data.MessageCount)
	}
}

// ── Search ───────────────────────────────────────────────────────────

func TestSearch_WithFolder(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/accounts/1/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if q != "hello world" {
			t.Errorf("expected query 'hello world', got %q", q)
		}
		folder := r.URL.Query().Get("folder")
		if folder != "INBOX" {
			t.Errorf("expected folder 'INBOX', got %q", folder)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{},
		})
	})

	c := New(srv.URL)
	resp, err := c.Search("tok", 1, "hello world", "INBOX")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Data) != 0 {
		t.Fatalf("expected 0 results, got %d", len(resp.Data))
	}
}

func TestSearch_WithoutFolder(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/accounts/1/search", func(w http.ResponseWriter, r *http.Request) {
		folder := r.URL.Query().Get("folder")
		if folder != "" {
			t.Errorf("expected no folder param, got %q", folder)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{},
		})
	})

	c := New(srv.URL)
	_, err := c.Search("tok", 1, "test", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── ListDomains ──────────────────────────────────────────────────────

func TestListDomains_Success(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/admin/domains", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{
				{"id": 1, "name": "mail1.test", "server_type": "traditional", "active": true},
				{"id": 2, "name": "mail3.test", "server_type": "restmail", "active": true},
			},
		})
	})

	c := New(srv.URL)
	resp, err := c.ListDomains("tok")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 domains, got %d", len(resp.Data))
	}
	if resp.Data[0].Name != "mail1.test" {
		t.Fatalf("expected first domain 'mail1.test', got %q", resp.Data[0].Name)
	}
	if resp.Data[1].ServerType != "restmail" {
		t.Fatalf("expected server_type 'restmail', got %q", resp.Data[1].ServerType)
	}
}

// ── CreateDomain ─────────────────────────────────────────────────────

func TestCreateDomain_Success(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/admin/domains", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["name"] != "newdomain.test" {
			t.Errorf("expected name 'newdomain.test', got %v", body["name"])
		}
		if body["server_type"] != "traditional" {
			t.Errorf("expected server_type 'traditional', got %v", body["server_type"])
		}
		w.WriteHeader(http.StatusCreated)
	})

	c := New(srv.URL)
	err := c.CreateDomain("tok", "newdomain.test", "traditional")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── DeleteDomain ─────────────────────────────────────────────────────

func TestDeleteDomain_Success(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/admin/domains/5", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	c := New(srv.URL)
	err := c.DeleteDomain("tok", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── ListMailboxes ────────────────────────────────────────────────────

func TestListMailboxes_Success(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/admin/mailboxes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{
				{"id": 1, "address": "user@mail1.test", "display_name": "User One", "domain_id": 1, "active": true},
			},
		})
	})

	c := New(srv.URL)
	resp, err := c.ListMailboxes("tok")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 mailbox, got %d", len(resp.Data))
	}
	if resp.Data[0].Address != "user@mail1.test" {
		t.Fatalf("expected address 'user@mail1.test', got %q", resp.Data[0].Address)
	}
}

// ── CreateMailbox ────────────────────────────────────────────────────

func TestCreateMailbox_Success(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/admin/mailboxes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["address"] != "new@mail1.test" {
			t.Errorf("expected address 'new@mail1.test', got %v", body["address"])
		}
		if body["display_name"] != "New User" {
			t.Errorf("expected display_name 'New User', got %v", body["display_name"])
		}
		if body["password"] != "pass123" {
			t.Errorf("expected password 'pass123', got %v", body["password"])
		}
		// domain_id comes as float64 from JSON
		if body["domain_id"] != float64(1) {
			t.Errorf("expected domain_id 1, got %v", body["domain_id"])
		}
		w.WriteHeader(http.StatusCreated)
	})

	c := New(srv.URL)
	err := c.CreateMailbox("tok", "new@mail1.test", "New User", "pass123", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── DeleteMailbox ────────────────────────────────────────────────────

func TestDeleteMailbox_Success(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/admin/mailboxes/3", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	c := New(srv.URL)
	err := c.DeleteMailbox("tok", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── ResetPassword ────────────────────────────────────────────────────

func TestResetPassword_Success(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/admin/mailboxes/3", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("expected PATCH, got %s", r.Method)
		}
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		if body["password"] != "newpass456" {
			t.Errorf("expected password 'newpass456', got %v", body["password"])
		}
		w.WriteHeader(http.StatusOK)
	})

	c := New(srv.URL)
	err := c.ResetPassword("tok", 3, "newpass456")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── ListPipelines ────────────────────────────────────────────────────

func TestListPipelines_WithDomainID(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/admin/pipelines", func(w http.ResponseWriter, r *http.Request) {
		domainID := r.URL.Query().Get("domain_id")
		if domainID != "2" {
			t.Errorf("expected domain_id=2, got %q", domainID)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{
				{"id": 1, "domain_id": 2, "direction": "inbound", "active": true},
			},
		})
	})

	c := New(srv.URL)
	resp, err := c.ListPipelines("tok", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 pipeline, got %d", len(resp.Data))
	}
}

func TestListPipelines_NoDomainID(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/admin/pipelines", func(w http.ResponseWriter, r *http.Request) {
		domainID := r.URL.Query().Get("domain_id")
		if domainID != "" {
			t.Errorf("expected no domain_id param, got %q", domainID)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{},
		})
	})

	c := New(srv.URL)
	_, err := c.ListPipelines("tok", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── TogglePipeline ───────────────────────────────────────────────────

func TestTogglePipeline_Success(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/admin/pipelines/7", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("expected PATCH, got %s", r.Method)
		}
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		if body["active"] != false {
			t.Errorf("expected active=false, got %v", body["active"])
		}
		w.WriteHeader(http.StatusOK)
	})

	c := New(srv.URL)
	err := c.TogglePipeline("tok", 7, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── QueueStats ───────────────────────────────────────────────────────

func TestQueueStats_Success(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/admin/queue/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"total":   100,
				"pending": 25,
				"failed":  3,
			},
		})
	})

	c := New(srv.URL)
	resp, err := c.QueueStats("tok")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Data.Total != 100 {
		t.Fatalf("expected total 100, got %d", resp.Data.Total)
	}
	if resp.Data.Pending != 25 {
		t.Fatalf("expected pending 25, got %d", resp.Data.Pending)
	}
	if resp.Data.Failed != 3 {
		t.Fatalf("expected failed 3, got %d", resp.Data.Failed)
	}
}

// ── ListBans ─────────────────────────────────────────────────────────

func TestListBans_Success(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/admin/bans", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("active") != "true" {
			t.Errorf("expected active=true query param")
		}
		if r.URL.Query().Get("limit") != "1" {
			t.Errorf("expected limit=1 query param")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{
				{"id": 1, "ip": "10.0.0.1", "protocol": "smtp"},
			},
			"pagination": map[string]interface{}{
				"total": 5,
			},
		})
	})

	c := New(srv.URL)
	resp, err := c.ListBans("tok")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 ban, got %d", len(resp.Data))
	}
	if resp.Data[0].IP != "10.0.0.1" {
		t.Fatalf("expected IP '10.0.0.1', got %q", resp.Data[0].IP)
	}
	if resp.Pagination == nil {
		t.Fatal("expected pagination to be present")
	}
	if resp.Pagination.Total != 5 {
		t.Fatalf("expected pagination total 5, got %d", resp.Pagination.Total)
	}
}

// ── HTTP helper edge cases ───────────────────────────────────────────

func TestGet_NetworkError(t *testing.T) {
	// Client pointing at a non-existent server.
	c := New("http://127.0.0.1:1")
	_, err := c.CheckMailbox("user@test.com")
	if err == nil {
		t.Fatal("expected network error")
	}
}

func TestPostAuth_NilOutput(t *testing.T) {
	// When out is nil and status is 2xx, postAuth should succeed.
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	c := New(srv.URL)
	err := c.postAuth("/api/v1/test", "tok", map[string]string{"key": "val"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDecodeResponse_NilOutput(t *testing.T) {
	// When out is nil, decodeResponse should not attempt to decode.
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not json"))
	})

	c := New(srv.URL)
	resp, err := c.httpClient.Get(srv.URL + "/api/v1/test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	err = c.decodeResponse(resp, nil)
	if err != nil {
		t.Fatalf("expected nil error for nil output, got: %v", err)
	}
}

func TestCheckStatus_VariousStatusCodes(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantErr    bool
	}{
		{"200 OK", 200, false},
		{"201 Created", 201, false},
		{"204 No Content", 204, false},
		{"299 edge", 299, false},
		{"301 redirect", 301, true},
		{"400 bad request", 400, true},
		{"404 not found", 404, true},
		{"500 internal error", 500, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, mux := newTestServer(t)
			mux.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				w.Write([]byte(`{"status":"test"}`))
			})

			c := New(srv.URL)
			err := c.get("/test", nil)
			if tt.wantErr && err == nil {
				t.Fatalf("expected error for status %d, got nil", tt.statusCode)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected no error for status %d, got: %v", tt.statusCode, err)
			}
		})
	}
}

func TestAuthorizationHeader_Propagation(t *testing.T) {
	srv, mux := newTestServer(t)
	var capturedAuth string
	mux.HandleFunc("/api/v1/admin/domains", func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"data": []interface{}{}})
	})

	c := New(srv.URL)
	_, err := c.ListDomains("super-secret-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedAuth != "Bearer super-secret-token" {
		t.Fatalf("expected 'Bearer super-secret-token', got %q", capturedAuth)
	}
}

func TestContentType_SetOnPost(t *testing.T) {
	srv, mux := newTestServer(t)
	var capturedContentType string
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		capturedContentType = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"access_token": "tok",
				"expires_in":   3600,
				"user":         map[string]interface{}{"id": 1, "email": "a@b.com"},
			},
		})
	})

	c := New(srv.URL)
	_, err := c.Login("a@b.com", "pass")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedContentType != "application/json" {
		t.Fatalf("expected Content-Type 'application/json', got %q", capturedContentType)
	}
}

func TestRequestBody_Roundtrip(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/messages/deliver", func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		var req DeliverRequest
		_ = json.Unmarshal(bodyBytes, &req)
		if req.Sender != "alice@test.com" {
			t.Errorf("expected sender 'alice@test.com', got %q", req.Sender)
		}
		if req.ClientIP != "10.0.0.5" {
			t.Errorf("expected client_ip '10.0.0.5', got %q", req.ClientIP)
		}
		if req.HeloName != "mx.test.com" {
			t.Errorf("expected helo_name 'mx.test.com', got %q", req.HeloName)
		}
		if req.RawMessage != "raw content here" {
			t.Errorf("expected raw_message 'raw content here', got %q", req.RawMessage)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"id": 1, "mailbox_id": 1, "subject": "Test"},
		})
	})

	c := New(srv.URL)
	_, err := c.DeliverMessage(&DeliverRequest{
		Address:    "user@test.com",
		Sender:     "alice@test.com",
		Subject:    "Test",
		BodyText:   "Body",
		ClientIP:   "10.0.0.5",
		HeloName:   "mx.test.com",
		RawMessage: "raw content here",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
