package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/desktop/internal/localmail"
	"github.com/rest-mail/rest-mail-server/desktop/internal/localstore"
	"github.com/rest-mail/rest-mail-server/desktop/internal/session"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
)

type testLocalIndex struct {
	store     *localstore.Store
	reindexed bool
	forgotten bool
	account   uint
}

func (index *testLocalIndex) LocalSearch(ctx context.Context, account uint, query, folder string) (localstore.Results, error) {
	index.account = account
	return index.store.Search(ctx, query, folder, 50, 0)
}
func (index *testLocalIndex) IndexStatus(ctx context.Context, account uint) (localmail.IndexStatus, error) {
	index.account = account
	status, err := index.store.Status(ctx)
	return localmail.IndexStatus{Status: status, AccountID: account}, err
}
func (index *testLocalIndex) Reindex(ctx context.Context, account uint) error {
	index.account = account
	index.reindexed = true
	return index.store.Reset(ctx)
}
func (index *testLocalIndex) ForgetAll() error { index.forgotten = true; return index.store.Delete() }

func TestLocalSearchIndexStatusAndReindexMCP(t *testing.T) {
	store, err := localstore.Open(t.TempDir(), localstore.AccountKey("server", "reader", 42))
	if err != nil {
		t.Fatal(err)
	}
	index := &testLocalIndex{store: store}
	t.Cleanup(func() {
		if !index.forgotten {
			store.Close()
		}
	})
	if err = store.Put(t.Context(), apitypes.Message{ID: 7, Folder: "INBOX", Subject: "Café launch", BodyText: "searchable content"}, true); err != nil {
		t.Fatal(err)
	}
	manager := &fakeSession{status: session.SessionStatus{SignedIn: true, AccountID: 42}}
	domain := NewDomain(manager, &fakeMail{})
	domain.Local = index
	_, discovery := startTestServer(t, Options{Domain: domain})
	client := connect(t, discovery)
	result := call(t, client, "local_search", LocalSearchArgs{Query: "subject:cafe in:INBOX"})
	text := resultText(t, result)
	if result.IsError || !strings.Contains(text, "Café launch") || !strings.Contains(text, "⟦") || index.account != 42 {
		t.Fatalf("local_search: %s", text)
	}
	result = call(t, client, "index_status", AccountArgs{})
	if result.IsError || !strings.Contains(resultText(t, result), `\"bodies\":1`) {
		t.Fatalf("index_status: %s", resultText(t, result))
	}
	result = call(t, client, "reindex", AccountArgs{})
	if result.IsError || !index.reindexed {
		t.Fatalf("reindex: %s", resultText(t, result))
	}
	result = call(t, client, "local_search", LocalSearchArgs{Query: "before:invalid"})
	if !result.IsError {
		t.Fatal("invalid local query succeeded")
	}
	manager.status.SignedIn = false
	for _, name := range []string{"local_search", "index_status", "reindex"} {
		arguments := map[string]any{}
		if name == "local_search" {
			arguments["query"] = "cafe"
		}
		result = call(t, client, name, arguments)
		if !result.IsError || !strings.Contains(resultText(t, result), "signed_out") {
			t.Fatalf("signed-out %s: %s", name, resultText(t, result))
		}
	}
	manager.status.SignedIn = true
	result = call(t, client, "sign_out", SignOutArgs{DeleteLocalData: true})
	if result.IsError || !index.forgotten || manager.method != "SignOut" {
		t.Fatalf("sign-out purge: %s", resultText(t, result))
	}
}
