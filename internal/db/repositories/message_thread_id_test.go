package repositories

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A message stored with no thread answers getMessage with a link to /threads/, which is a 404.
// E2E's sequenced run found that on a saved draft; this finds the next path before E2E does. Each
// entry here says why that literal has no ThreadID field.
var messageLiteralsWithoutThread = map[string]string{
	"internal/api/handlers/message_import.go": "storeImportedMessage assigns ThreadID once it has read the headers",
}

// isFolderMarker reports the zero-content row createFolder stores: it is deleted, so no route reads it.
func isFolderMarker(fields map[string]bool) bool { return fields["IsDeleted"] && fields["Folder"] }

func TestEveryStoredMessageNamesItsThread(t *testing.T) {
	// go test runs a package's tests in that package's directory.
	root := filepath.Join("..", "..", "..")
	checked := 0
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			files := token.NewFileSet()
			parsed, err := parser.ParseFile(files, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			relative, _ := filepath.Rel(root, path)
			ast.Inspect(parsed, func(node ast.Node) bool {
				literal, ok := node.(*ast.CompositeLit)
				if !ok || len(literal.Elts) == 0 {
					return true
				}
				selector, ok := literal.Type.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "Message" {
					return true
				}
				if pkg, ok := selector.X.(*ast.Ident); !ok || pkg.Name != "models" {
					return true
				}
				checked++
				fields := map[string]bool{}
				for _, element := range literal.Elts {
					if pair, ok := element.(*ast.KeyValueExpr); ok {
						if key, ok := pair.Key.(*ast.Ident); ok {
							fields[key.Name] = true
						}
					}
				}
				if !fields["ThreadID"] && !isFolderMarker(fields) && messageLiteralsWithoutThread[filepath.ToSlash(relative)] == "" {
					t.Errorf("%s: a stored message sets no ThreadID", files.Position(literal.Pos()))
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if checked == 0 {
		t.Fatal("no message literal found: this check looks in the wrong place")
	}
}

// A Message-ID of "<>" canonicalises to nothing, and that must not leave a message outside
// every thread.
func TestResolveThreadIDAlwaysNamesAThread(t *testing.T) {
	for _, id := range []string{"<>", " < > "} {
		first, err := ResolveThreadID(nil, 0, id, "", "")
		if err != nil {
			t.Fatal(err)
		}
		second, err := ResolveThreadID(nil, 0, id, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if first == "" || first == second {
			t.Errorf("message id %q: threads %q and %q, want two threads of their own", id, first, second)
		}
	}
}
