package mailsync

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/desktop/internal/apiclient"
	"github.com/rest-mail/rest-mail-server/desktop/internal/localstore"
	apitypes "github.com/rest-mail/rest-mail-server/pkg/apitypes"
)

func TestApplyAttachmentRetriesIdentityAndDelete(t *testing.T) {
	fixture := newFixture(t)
	message := synthetic(1)
	message.HasAttachments = true
	fixture.messages[1] = message
	store, err := localstore.Open(t.TempDir(), localstore.AccountKey("server", "owner", 1))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	syncer := Synchronizer{Client: apiclient.New(fixture), Transport: fixture}
	ctx := t.Context()
	if err = syncer.Apply(ctx, store, 1, 1); err == nil {
		t.Fatal("failed attachment metadata marked complete")
	}
	if _, err = store.Get(ctx, 1, true); err == nil {
		t.Fatal("incomplete attachment body cached")
	}
	metadataServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		json.NewEncoder(writer).Encode(map[string]any{"data": []localstore.Attachment{{ID: 42, Filename: "synthetic.txt", SizeBytes: 3}}})
	}))
	defer metadataServer.Close()
	syncer.Transport = &restFixture{server: metadataServer}
	if err = syncer.Apply(ctx, store, 1, 1); err != nil {
		t.Fatal(err)
	}
	attachments, err := store.Attachments(ctx, 1)
	if err != nil || len(attachments) != 1 {
		t.Fatalf("attachment retry: %+v %v", attachments, err)
	}
	fixture.mu.Lock()
	message.MailboxID = 2
	fixture.messages[1] = message
	fixture.mu.Unlock()
	if err = syncer.Apply(ctx, store, 1, 1); err == nil {
		t.Fatal("cross-account event cached")
	}
	fixture.mu.Lock()
	delete(fixture.messages, 1)
	fixture.mu.Unlock()
	if err = syncer.Apply(ctx, store, 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(ctx, 1, false); err == nil {
		t.Fatal("deleted event remains")
	}
	failure := errors.New("write failed")
	if err = syncer.Write(ctx, func() error { return failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err = syncer.saveBody(ctx, store, synthetic(2)); err != nil {
		t.Fatal(err)
	}
	message = synthetic(3)
	message.HasAttachments = true
	syncer.Transport = nil
	if err = syncer.saveBody(ctx, store, message); err == nil {
		t.Fatal("missing attachment transport accepted")
	}
}

func TestSyncMalformedPagesNeverPruneCache(t *testing.T) {
	for _, missing := range []string{"folders", "messages"} {
		t.Run(missing, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if missing == "messages" && strings.HasSuffix(request.URL.Path, "/folders") {
					io.WriteString(writer, `{"data":[{"folder":"INBOX"}]}`)
				} else {
					io.WriteString(writer, `{}`)
				}
			}))
			defer server.Close()
			transport := &restFixture{server: server}
			store, err := localstore.Open(t.TempDir(), localstore.AccountKey("server", "owner", 1))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err = store.Put(t.Context(), apitypes.Message{ID: 1, Folder: "INBOX", Subject: "preserve"}, true); err != nil {
				t.Fatal(err)
			}
			syncer := Synchronizer{Client: apiclient.New(transport)}
			if err = syncer.Sync(t.Context(), store, 1, 0); err == nil {
				t.Fatal("missing data accepted")
			}
			if _, err = store.Get(t.Context(), 1, true); err != nil {
				t.Fatalf("cache pruned on malformed response: %v", err)
			}
		})
	}
}
