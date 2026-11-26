package testguard

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// serverInternal is the import path prefix of this module's internal packages.
// The desktop module's own internal packages live under .../desktop/internal/
// and are not matched by it.
const serverInternal = "github.com/rest-mail/rest-mail-server/internal/"

// The desktop client is a REST Mail client like any other. It may build on the
// public packages under pkg/, but never on the server's internal packages: it
// could import them only because it lives inside this repository's tree, and a
// client outside the tree cannot (#2217). The desktop workflow does not run Go
// tests, so this check lives in the main module, whose tests CI always runs.
func TestDesktopImportsNoServerInternalPackage(t *testing.T) {
	desktop := filepath.Join(repoRoot(t), "desktop")
	found, err := serverInternalImports(desktop)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		t.Errorf("%s: desktop must import pkg/, not the server's internal/ packages", f)
	}
}

// The check must see an import in any desktop Go file, test files included,
// and must leave the desktop module's own internal packages alone.
func TestServerInternalImportsFindsOnlyServerPackages(t *testing.T) {
	dir := t.TempDir()
	write := func(name, source string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("ok.go", "package main\nimport (\n\t\"github.com/rest-mail/rest-mail-server/pkg/apitypes\"\n\t\"github.com/rest-mail/rest-mail-server/desktop/internal/session\"\n)\n")
	write("sub/bad_test.go", "package sub\nimport \"github.com/rest-mail/rest-mail-server/internal/mime\"\n")
	write("node_modules/dep/skip.go", "package dep\nimport \"github.com/rest-mail/rest-mail-server/internal/db\"\n")

	found, err := serverInternalImports(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("sub", "bad_test.go") + ":2 imports github.com/rest-mail/rest-mail-server/internal/mime"
	if len(found) != 1 || found[0] != want {
		t.Fatalf("found %q, want only %q", found, want)
	}
}

// serverInternalImports reports, as file:line, each import of a server
// internal package in the Go files under root.
func serverInternalImports(root string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && (name == "node_modules" || name == "testdata" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if strings.HasPrefix(imported, serverInternal) {
				line := fset.Position(spec.Pos()).Line
				found = append(found, fmt.Sprintf("%s:%d imports %s", relative, line, imported))
			}
		}
		return nil
	})
	return found, err
}
