package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	goimap "github.com/rest-mail/go-imap/pkg/imap"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// TestMessageKeywordsPersist: PATCH /messages/{id} keywords replaces the
// message's stored keyword set, the IMAP gateway's STORE path. The set is
// deduplicated without case and read back by GET; it leaves is_read and the
// other flags alone. A system flag, a non-atom, a keyword over 255 bytes or
// more than imap.MaxKeywords keywords is 422 and stores nothing.
func TestMessageKeywordsPersist(t *testing.T) {
	database := importProofDB(t)
	fixture := seedAccountsFixture(t, database)
	if err := database.Create(&models.QuotaUsage{MailboxID: fixture.user.ID}).Error; err != nil {
		t.Fatal(err)
	}
	importer := NewMessageImportHandler(database, 4096, nil, nil)
	body := apitypes.ImportMessageBody{RawMessage: []byte("Message-ID: <keywords@example.test>\r\nSubject: keywords\r\n\r\nbody\r\n"), Folder: "INBOX", Flags: []string{`\Seen`, "$Junk"}, InternalDate: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := authzRequest(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/messages/import", fixture.user.ID), fixture.admin.ID, nil, string(encoded))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	importMessageRoutes(importer).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body)
	}
	var imported endpoint.Data[apitypes.ImportMessageResult]
	if err := json.Unmarshal(response.Body.Bytes(), &imported); err != nil {
		t.Fatal(err)
	}
	routes := messageRoutes(NewMessageHandler(database, nil, nil, "", nil))
	target := fmt.Sprintf("/api/v1/messages/%d", imported.Data.ID)
	call := func(method, payload string) *httptest.ResponseRecorder {
		request := authzRequest(method, target, fixture.user.ID, nil, payload)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		routes.ServeHTTP(response, request)
		return response
	}
	stored := func() models.Message {
		t.Helper()
		var message models.Message
		if err := database.First(&message, imported.Data.ID).Error; err != nil {
			t.Fatal(err)
		}
		return message
	}
	if got := stored().Keywords; !slices.Equal(got, []string{"$Junk"}) {
		t.Fatalf("imported keywords %v, want [$Junk]", got)
	}

	if response := call(http.MethodPatch, `{"keywords":["$Forwarded","Work","work","$MDNSent"]}`); response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body)
	}
	want := []string{"$Forwarded", "Work", "$MDNSent"}
	message := stored()
	if !slices.Equal(message.Keywords, want) || !message.IsRead {
		t.Fatalf("stored keywords %v is_read %t, want %v and still read", message.Keywords, message.IsRead, want)
	}
	response = call(http.MethodGet, "")
	var got endpoint.Data[Message]
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || response.Code != http.StatusOK || !slices.Equal(got.Data.Keywords, want) {
		t.Fatalf("GET keywords: %d %s (%v)", response.Code, response.Body, err)
	}

	tooMany := make([]string, goimap.MaxKeywords+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf(`"k%d"`, i)
	}
	for _, payload := range []string{
		`{"keywords":["\\Seen"]}`,
		`{"keywords":["a b"]}`,
		`{"keywords":["` + strings.Repeat("x", goimap.MaxKeywordLength+1) + `"]}`,
		`{"keywords":[` + strings.Join(tooMany, ",") + `]}`,
	} {
		if response := call(http.MethodPatch, payload); response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("PATCH %.40s: %d %s, want 422", payload, response.Code, response.Body)
		}
		if got := stored().Keywords; !slices.Equal(got, want) {
			t.Fatalf("refused PATCH %.40s changed keywords to %v", payload, got)
		}
	}

	if response := call(http.MethodPatch, `{"keywords":[]}`); response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body)
	}
	if got := stored().Keywords; len(got) != 0 {
		t.Fatalf("PATCH [] left %v", got)
	}
}
