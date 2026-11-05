package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// A list reads the headers of a stored source and leaves the rest in the database, and
// what it answers is what the message's own route answers, which reads it all.
func TestListedMessagesReadOnlyTheHeadersOfTheirSource(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fx := seedMsgDeleteFixture(t, tx, 0)

	const rest = "THE-REST-OF-THE-SOURCE"
	auth1 := "Authentication-Results: " + pipeline.AuthServID + "; spf=pass; dkim=pass\r\n"
	sources := []models.Message{
		{Subject: "crlf", RawMessage: auth1 + "List-Id: <news.example.test>\r\nSubject: crlf\r\n\r\n" + rest + "\r\n\r\n" + rest},
		{Subject: "lf", RawMessage: "Auto-Submitted: auto-generated\nSubject: lf\n\n" + rest + "\n\n" + rest},
		{Subject: "unicode", RawMessage: "Subject: =?UTF-8?Q?x?=\r\nX-Note: caf\u00e9\r\nPrecedence: bulk\r\n\r\n" + rest},
		{Subject: "headers only", RawMessage: "List-Id: <only.example.test>\r\nSubject: headers only\r\n"},
		{Subject: "octets", RawOctets: []byte("List-Unsubscribe: <https://example.test/u>\r\nX-Bad: \xff\r\n\r\n" + rest)},
		{Subject: "no source", BodyText: "a composed message"},
	}
	for i := range sources {
		sources[i].MailboxID, sources[i].Folder, sources[i].Sender = fx.mailbox.ID, "INBOX", "sender@delq.test"
		sources[i].ThreadID = "thread"
		if err := tx.Create(&sources[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), middleware.ClaimsKey, &auth.Claims{AccountID: fx.mailbox.ID})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	NewMessageHandler(tx, nil, nil, "", nil).RegisterMessages(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()})

	get := func(t *testing.T, target string) []byte {
		t.Helper()
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, target, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", target, rr.Code, rr.Body)
		}
		return rr.Body.Bytes()
	}

	read := watchMessagesRead(t, tx)
	for name, target := range map[string]string{
		"folder": fmt.Sprintf("/api/v1/accounts/%d/folders/INBOX/messages", fx.mailbox.ID),
		"thread": fmt.Sprintf("/api/v1/accounts/%d/threads/thread", fx.mailbox.ID),
	} {
		t.Run(name, func(t *testing.T) {
			read.reset()
			var listed struct {
				Data []map[string]any `json:"data"`
			}
			if err := json.Unmarshal(get(t, target), &listed); err != nil {
				t.Fatal(err)
			}
			if len(listed.Data) != len(sources) {
				t.Fatalf("listed %d, want %d", len(listed.Data), len(sources))
			}
			if rows := read.rows(); rows != len(sources) && rows != len(sources)+1 {
				t.Fatalf("watched %d rows read, want the %d listed", rows, len(sources))
			}
			if read.sawRest(rest) {
				t.Fatal("the list read a message past its headers")
			}
			for _, item := range listed.Data {
				var whole struct {
					Data map[string]any `json:"data"`
				}
				if err := json.Unmarshal(get(t, fmt.Sprintf("/api/v1/messages/%v", item["id"])), &whole); err != nil {
					t.Fatal(err)
				}
				for _, field := range []string{"class", "auth_results", "auth_verdict", "subject", "body_text", "size_bytes", "raw_size"} {
					a, _ := json.Marshal(item[field])
					b, _ := json.Marshal(whole.Data[field])
					if !bytes.Equal(a, b) {
						t.Errorf("%v: %s is %s listed and %s on its own", item["subject"], field, a, b)
					}
				}
			}
		})
	}

	// The class has to come from each source's headers, or agreeing above says little.
	var listed struct {
		Data []struct {
			Subject     string `json:"subject"`
			Class       string `json:"class"`
			AuthVerdict string `json:"auth_verdict"`
		} `json:"data"`
	}
	if err := json.Unmarshal(get(t, fmt.Sprintf("/api/v1/accounts/%d/folders/INBOX/messages", fx.mailbox.ID)), &listed); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"crlf": "newsletter", "lf": "notification", "unicode": "notification", "headers only": "newsletter", "octets": "newsletter", "no source": "person"}
	for _, m := range listed.Data {
		if m.Class != want[m.Subject] {
			t.Errorf("%s: class %q, want %q", m.Subject, m.Class, want[m.Subject])
		}
		if m.Subject == "crlf" && m.AuthVerdict != "signed" {
			t.Errorf("crlf: auth_verdict %q, want signed", m.AuthVerdict)
		}
	}
}

// messagesRead keeps the rows each query on messages brought back from the database.
type messagesRead struct {
	mu   sync.Mutex
	seen []models.Message
}

func (m *messagesRead) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen = nil
}

func (m *messagesRead) rows() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.seen)
}

func (m *messagesRead) sawRest(rest string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, row := range m.seen {
		if strings.Contains(row.RawMessage, rest) || bytes.Contains(row.RawOctets, []byte(rest)) {
			return true
		}
	}
	return false
}

func watchMessagesRead(t *testing.T, db *gorm.DB) *messagesRead {
	t.Helper()
	read := &messagesRead{}
	const name = "test:watch_messages_read"
	if err := db.Callback().Query().After("gorm:after_query").Register(name, func(q *gorm.DB) {
		if rows, ok := q.Statement.Dest.(*[]models.Message); ok {
			read.mu.Lock()
			defer read.mu.Unlock()
			read.seen = append(read.seen, *rows...)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(name) })
	return read
}

// A column the model gains has to reach the routes that list messages.
func TestMessageListFieldsNameEveryColumn(t *testing.T) {
	parsed, err := schema.Parse(&models.Message{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range parsed.Fields {
		if field.DBName == "" || field.IgnoreMigration {
			continue
		}
		name := "messages." + field.DBName
		if field.DBName == "references" {
			name = `messages."references"`
		}
		if !strings.Contains(messageListFields, name) {
			t.Errorf("messageListFields does not read %s", field.DBName)
		}
	}
}
