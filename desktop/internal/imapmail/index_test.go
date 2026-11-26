package imapmail

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/rest-mail/rest-mail-server/desktop/internal/apiclient"
	"github.com/rest-mail/rest-mail-server/desktop/internal/localstore"
	"github.com/rest-mail/rest-mail-server/desktop/internal/mailsync"
	apitypes "github.com/rest-mail/rest-mail-server/pkg/apitypes"
)

type indexTransport struct{ store *Store }

func (transport indexTransport) Do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	response, _, err := transport.store.Handle(request)
	return response, err
}

func TestIMAPIndexBackfillFlagsMovesDeletesAndUIDValidity(t *testing.T) {
	store, account, fixture, _ := setupStore(t, true, true)
	appendMail(t, fixture, "indexed-first", true)
	appendMail(t, fixture, "indexed-second", false)
	local, err := localstore.Open(t.TempDir(), localstore.AccountKey(account.Host, account.Address, account.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	transport := indexTransport{store}
	client := apiclient.New(transport)
	syncer := mailsync.Synchronizer{Client: client, Transport: transport}
	ctx := t.Context()
	if err = syncer.Sync(ctx, local, account.ID, mailsync.DefaultBodyBudget); err != nil {
		t.Fatal(err)
	}
	page, err := client.ListMessages(ctx, account.ID, "INBOX", apiclient.Page{}, "")
	if err != nil || page.UIDValidity == 0 {
		t.Fatalf("UIDVALIDITY missing: %+v %v", page, err)
	}
	result, err := local.Search(ctx, "indexed", "", 50, 0)
	if err != nil || len(result.Data) != 2 {
		t.Fatalf("backfill: %+v %v", result, err)
	}
	first, second := page.Data[0].ID, page.Data[1].ID
	read := true
	if err = client.SetFlags(ctx, []uint{first}, apitypes.UpdateMessageBody{IsRead: &read, IsFlagged: &read}); err != nil {
		t.Fatal(err)
	}
	if err = client.MoveMessages(ctx, []uint{second}, "Archive"); err != nil {
		t.Fatal(err)
	}
	appendMail(t, fixture, "indexed-arrival", false)
	if err = syncer.Sync(ctx, local, account.ID, mailsync.DefaultBodyBudget); err != nil {
		t.Fatal(err)
	}
	result, err = local.Search(ctx, "is:unread in:INBOX", "", 50, 0)
	if err != nil || len(result.Data) != 1 || result.Data[0].Subject != "indexed-arrival" {
		t.Fatalf("arrival/flags: %+v %v", result, err)
	}
	result, err = local.Search(ctx, "in:Archive has:attachment", "", 50, 0)
	if err != nil || len(result.Data) != 1 {
		t.Fatalf("move/attachments: %+v %v", result, err)
	}
	attachments, err := local.Attachments(ctx, result.Data[0].ID)
	if err != nil || len(attachments) != 1 {
		t.Fatalf("attachment metadata: %+v %v", attachments, err)
	}
	if err = client.DeleteMessages(ctx, []uint{first}, true); err != nil {
		t.Fatal(err)
	}
	if err = syncer.Sync(ctx, local, account.ID, mailsync.DefaultBodyBudget); err != nil {
		t.Fatal(err)
	}
	if _, err = local.Get(ctx, first, false); err == nil {
		t.Fatal("deleted IMAP message remains cached")
	}
}
