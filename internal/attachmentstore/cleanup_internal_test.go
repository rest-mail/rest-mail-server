package attachmentstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/attachmentstore/s3test"
)

func heldBy(refs ...string) func(context.Context, []string) ([]string, error) {
	return func(context.Context, []string) ([]string, error) { return refs, nil }
}

func oldFile(t *testing.T, path string, age time.Duration) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
	return path
}

// The S3 store and the filesystem tree are swept alike: an object past the grace
// period that no row holds goes, and every other one stays.
func TestSweepRemovesOnlyOldUnreferencedObjects(t *testing.T) {
	root := t.TempDir()
	month := 30 * 24 * time.Hour
	kept := oldFile(t, filepath.Join(root, "2026/01/01/kept"), month)
	gone := oldFile(t, filepath.Join(root, "imports/gone.bin"), month)
	young := oldFile(t, filepath.Join(root, "imports/young.bin"), time.Minute)

	fake := s3test.New(t)
	store := NewS3(fake.Config)
	ctx := context.Background()
	keptRef, _, err := store.Put(ctx, []byte("kept"))
	if err != nil {
		t.Fatal(err)
	}
	goneRef, _, err := store.Put(ctx, []byte("gone"))
	if err != nil {
		t.Fatal(err)
	}

	c := NewCleaner(nil, store, root, DefaultCleanupGrace, time.Hour)
	c.held = heldBy(kept, keptRef)
	result, err := c.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed != 2 || result.Bytes == 0 || result.Failed != 0 {
		t.Fatalf("result = %+v", result)
	}
	for path, want := range map[string]bool{kept: true, young: true, gone: false} {
		if _, err := os.Stat(path); (err == nil) != want {
			t.Errorf("%s: exists=%v, want %v", path, err == nil, want)
		}
	}
	if reader, err := store.Open(ctx, keptRef); err != nil {
		t.Errorf("the referenced object went: %v", err)
	} else {
		_ = reader.Close()
	}
	if reader, err := store.Open(ctx, goneRef); err == nil {
		_ = reader.Close()
		t.Error("the unreferenced object stayed")
	}
}

// An object is removed only after a query has said nothing refers to it.
func TestSweepRemovesNothingWhenTheLookupFails(t *testing.T) {
	root := t.TempDir()
	path := oldFile(t, filepath.Join(root, "orphan"), 30*24*time.Hour)
	c := NewCleaner(nil, nil, root, time.Hour, time.Hour)
	c.held = func(context.Context, []string) ([]string, error) { return nil, errors.New("database gone") }
	if _, err := c.Sweep(context.Background()); err == nil {
		t.Fatal("the failed lookup was not reported")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the file went: %v", err)
	}
}

// A symlink could point out of the tree, and only regular files are ours to remove.
func TestSweepLeavesSymlinksAlone(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	target := oldFile(t, filepath.Join(outside, "precious"), 30*24*time.Hour)
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	c := NewCleaner(nil, nil, root, time.Hour, time.Hour)
	c.held = heldBy()
	if _, err := c.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("the link went: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("its target went: %v", err)
	}
}

// A store with no attachments directory has nothing to sweep.
func TestSweepWithoutTheTree(t *testing.T) {
	c := NewCleaner(nil, nil, filepath.Join(t.TempDir(), "absent"), time.Hour, time.Hour)
	c.held = heldBy()
	if result, err := c.Sweep(context.Background()); err != nil || result.Removed != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

// A delivery that reuses a file touches it first, so a file touched after the sweep
// listed it stays even though the lookup found no row.
func TestSweepKeepsAFileTouchedDuringTheSweep(t *testing.T) {
	root := t.TempDir()
	path := oldFile(t, filepath.Join(root, "reused"), 30*24*time.Hour)
	c := NewCleaner(nil, nil, root, time.Hour, time.Hour)
	c.held = func(context.Context, []string) ([]string, error) {
		now := time.Now()
		return nil, os.Chtimes(path, now, now)
	}
	result, err := c.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed != 0 || result.Failed != 0 {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the file went: %v", err)
	}
}
