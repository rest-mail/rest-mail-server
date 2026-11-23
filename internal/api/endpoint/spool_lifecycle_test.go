package endpoint

import (
	"io"
	"os"
	"strings"
	"testing"
)

type panickingSpoolReader struct{}

func (panickingSpoolReader) Read([]byte) (int, error) { panic("interrupted spool copy") }

func TestAC01UploadSpoolCleansUpWhenSourcePanics(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	func() {
		defer func() {
			if got := recover(); got != "interrupted spool copy" {
				t.Fatalf("spool recovered %v, want the original panic", got)
			}
		}()
		_, _, _ = Spool(io.MultiReader(strings.NewReader("partial upload"), panickingSpoolReader{}))
	}()
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("panicking upload left %d spool file(s): %v", len(files), files)
	}
}
