package attachments

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, name, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRetainsBytesInGoAndExposesOnlyMetadata(t *testing.T) {
	holder := New(100)
	path := fixture(t, "notes.txt", "PRIVATE CONTENT")
	files, err := holder.Add("one", []string{path})
	if err != nil || len(files) != 1 || files[0].Size != 15 {
		t.Fatalf("%v %v", files, err)
	}
	if string(holder.drafts["one"][0].data) != "PRIVATE CONTENT" {
		t.Fatal("file not retained")
	}
	encoded, _ := json.Marshal(files)
	if strings.Contains(string(encoded), "PRIVATE") || strings.Contains(string(encoded), filepath.Dir(path)) {
		t.Fatal("file data or path leaked")
	}
	if got := holder.Remove("one", files[0].ID); len(got) != 0 {
		t.Fatal(got)
	}
	if _, err := holder.Add("two", []string{path}); err != nil {
		t.Fatal(err)
	}
	holder.Clear("two")
	if len(holder.drafts["two"]) != 0 {
		t.Fatal("draft bytes retained after close")
	}
}

func TestAggregateLimitAndFailedBatchAreAtomic(t *testing.T) {
	holder := New(6)
	path := fixture(t, "small.txt", "123")
	files, err := holder.Add("one", []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Add("one", []string{path, path}); err == nil {
		t.Fatal("aggregate limit ignored")
	}
	if len(holder.drafts["one"]) != 1 {
		t.Fatal("partial failed batch retained")
	}
	if _, err := holder.Add("one", []string{path}); err != nil {
		t.Fatal("exact limit rejected", err)
	}
	holder.Remove("one", files[0].ID)
	if _, err := holder.Add("one", []string{path}); err != nil {
		t.Fatal("remove did not release capacity", err)
	}
}

func TestRejectsDirectoriesMissingFilesAndEmptyDrops(t *testing.T) {
	holder := New(0)
	for _, paths := range [][]string{nil, {t.TempDir()}, {filepath.Join(t.TempDir(), "missing")}} {
		if _, err := holder.Add("one", paths); err == nil {
			t.Fatalf("accepted %v", paths)
		}
	}
	if _, err := holder.Add("", []string{fixture(t, "a", "a")}); err == nil {
		t.Fatal("empty draft accepted")
	}
	if holder.limit != DefaultLimit {
		t.Fatal("wrong fallback limit")
	}
}
