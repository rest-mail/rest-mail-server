package localstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
)

func TestFolderPagingBodiesAttachmentsCatalogAndReset(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()
	if err := store.Identity(ctx, "account-key"); err != nil {
		t.Fatal(err)
	}
	if err := store.PutFolders(ctx, []apitypes.FolderSummary{{Folder: "INBOX"}, {Folder: "Removed"}}); err != nil {
		t.Fatal(err)
	}
	folders, err := store.Folders(ctx)
	if err != nil || len(folders) != 2 {
		t.Fatalf("folders: %+v %v", folders, err)
	}
	_, generation, err := store.BeginScan(ctx, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	var messages []apitypes.Message
	for index := uint(1); index <= 125; index++ {
		messages = append(messages, fixture(index, fmt.Sprintf("message %d", index), "body"))
	}
	if err = store.SavePage(ctx, "INBOX", "", generation, 99, messages, true); err != nil {
		t.Fatal(err)
	}
	if !store.FolderComplete(ctx, "INBOX") || store.FolderComplete(ctx, "missing") {
		t.Fatal("folder completeness incorrect")
	}
	page, more, err := store.List(ctx, "INBOX", 0, 0)
	if err != nil || !more || len(page) != 50 || page[0].ID != 125 {
		t.Fatalf("first page: %+v %t %v", page, more, err)
	}
	page, more, err = store.List(ctx, "INBOX", int64(page[len(page)-1].ID), 100)
	if err != nil || more || len(page) != 75 {
		t.Fatalf("second page: count=%d more=%t %v", len(page), more, err)
	}
	pending, err := store.Pending(ctx, 4)
	if err != nil || len(pending) != 100 {
		t.Fatalf("pending: %d %v", len(pending), err)
	}
	if err = store.PutBatch(ctx, messages, true, generation); err != nil {
		t.Fatal(err)
	}
	pending, err = store.Pending(ctx, 4)
	if err != nil || len(pending) != 0 {
		t.Fatalf("cached bodies still pending: %d %v", len(pending), err)
	}
	if err = store.PutAttachments(ctx, 1, []Attachment{{ID: 50, Filename: "résumé.txt", ContentType: "text/plain", SizeBytes: 4}}); err != nil {
		t.Fatal(err)
	}
	attachments, err := store.Attachments(ctx, 1)
	if err != nil || len(attachments) != 1 || attachments[0].Filename != "résumé.txt" {
		t.Fatalf("attachments: %+v %v", attachments, err)
	}
	if err = store.PutAttachments(ctx, 1, nil); err != nil {
		t.Fatal(err)
	}
	attachments, err = store.Attachments(ctx, 1)
	if err != nil || len(attachments) != 0 {
		t.Fatalf("stale attachments: %+v %v", attachments, err)
	}
	if err = store.PutCatalog(ctx, []apitypes.AccountResponse{{ID: 1, Address: "reader@test", IsPrimary: true}, {ID: 2, Address: "other@test"}}); err != nil {
		t.Fatal(err)
	}
	catalog, err := store.Catalog(ctx)
	if err != nil || len(catalog) != 2 {
		t.Fatalf("catalog: %+v %v", catalog, err)
	}
	if err = store.RemoveCatalog(ctx, 2); err != nil {
		t.Fatal(err)
	}
	catalog, err = store.Catalog(ctx)
	if err != nil || len(catalog) != 1 {
		t.Fatalf("catalog removal: %+v %v", catalog, err)
	}
	if err = store.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := store.Status(ctx)
	if err != nil || status.Complete || status.Bodies != 0 || status.Messages != 125 {
		t.Fatalf("reset: %+v %v", status, err)
	}
	if err = store.RestartScan(ctx, "INBOX"); err != nil {
		t.Fatal(err)
	}
	if err = store.PutFolders(ctx, []apitypes.FolderSummary{{Folder: "INBOX"}}); err != nil {
		t.Fatal(err)
	}
	folders, err = store.Folders(ctx)
	if err != nil || len(folders) != 1 {
		t.Fatalf("folder removal: %+v %v", folders, err)
	}
	result, err := store.Search(ctx, "message", "", 1, 0)
	if err != nil || len(result.Data) != 1 || !result.Pagination.HasMore || result.Pagination.Cursor != "local:1" {
		t.Fatalf("search paging: %+v %v", result, err)
	}
	if _, err = store.Search(ctx, "message", "", 1, -1); err == nil {
		t.Fatal("negative offset accepted")
	}
}

func TestNormalRootPrivateAndDeleteRemovesDatabaseSidecars(t *testing.T) {
	config := t.TempDir()
	root, err := Root(config, "")
	if err != nil {
		t.Fatal(err)
	}
	if root != filepath.Join(config, "REST Mail", "mail") {
		t.Fatalf("unexpected path: %s", root)
	}
	for _, directory := range []string{root, filepath.Dir(root)} {
		info, err := os.Stat(directory)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("private directory: %v %v", info, err)
		}
	}
	store, err := Open(root, AccountKey("server", "owner", 1))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Put(t.Context(), fixture(1, "delete", "body"), true); err != nil {
		t.Fatal(err)
	}
	path := store.Path()
	if err = store.Delete(); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if _, err = os.Stat(candidate); !os.IsNotExist(err) {
			t.Fatalf("account file remains %s: %v", candidate, err)
		}
	}
	if _, err = Open(root, "../bad"); err == nil {
		t.Fatal("unsafe account key accepted")
	}
}

func TestCanceledStoreOperationsDoNotMutateCache(t *testing.T) {
	store := testStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	checks := []func() error{
		func() error { return store.Identity(ctx, "identity") },
		func() error { return store.Put(ctx, fixture(1, "cancel", "body"), true) },
		func() error { return store.Remove(ctx, 1) },
		func() error { return store.Reset(ctx) },
		func() error { return store.PutFolders(ctx, nil) },
		func() error { return store.PutCatalog(ctx, nil) },
		func() error { return store.RemoveCatalog(ctx, 1) },
		func() error { return store.PutAttachments(ctx, 1, nil) },
		func() error { return store.RestartScan(ctx, "INBOX") },
		func() error { _, err := store.Folders(ctx); return err },
		func() error { _, err := store.Catalog(ctx); return err },
		func() error { _, err := store.Attachments(ctx, 1); return err },
		func() error { _, err := store.Pending(ctx, 10); return err },
		func() error { _, err := store.Get(ctx, 1, true); return err },
		func() error { _, _, err := store.List(ctx, "INBOX", 0, 50); return err },
		func() error { _, err := store.Search(ctx, "query", "", 50, 0); return err },
		func() error { _, _, err := store.BeginScan(ctx, "INBOX"); return err },
	}
	for index, check := range checks {
		if err := check(); err == nil {
			t.Fatalf("canceled operation %d succeeded", index)
		}
	}
}
