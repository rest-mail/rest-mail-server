package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// masterKeyWriters is every function outside this command that encrypts a value
// with the master key, and the column that value is stored in, as the model and
// the column name rotate-key updates.
var masterKeyWriters = map[string]string{
	"cmd/api/operator_dkim.go ensureDKIMKey":                  "Domain.dkim_private_key",
	"internal/acme/account.go accountKey":                     "ACMEAccount.key_pem",
	"internal/acme/client.go StoreCertificate":                "Certificate.key_pem",
	"internal/api/handlers/certificates.go createCertificate": "Certificate.key_pem",
	"internal/api/handlers/dkim.go setKey":                    "Domain.dkim_private_key",
	"internal/api/handlers/relay.go save":                     "DomainRelay.password_enc",
	"internal/api/handlers/two_factor.go enroll":              "Credential.encrypted_secret",
	"internal/dkimrotate/stage.go Stage":                      "DKIMRotationKey.private_key",
}

// encryptHelpers are the functions that encrypt with the master key. A helper
// calling another helper stores nothing itself, so only its callers count.
var encryptHelpers = map[string]bool{
	"Encrypt":               true,
	"EncryptString":         true,
	"EncryptDKIMPrivateKey": true,
	"EncryptRelayPassword":  true,
	"SetPrivateKey":         true,
}

func callee(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		return fun.Sel.Name
	}
	return ""
}

// masterKeyWriteSites finds every function under cmd and internal, other than
// this command and the helpers themselves, that calls an encrypt helper.
func masterKeyWriteSites(t *testing.T, repoRoot string) map[string]bool {
	t.Helper()
	sites := map[string]bool{}
	for _, top := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, top), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "rotate-key" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			for _, decl := range file.Decls {
				name := "package-level"
				if fn, ok := decl.(*ast.FuncDecl); ok {
					name = fn.Name.Name
				}
				if encryptHelpers[name] {
					continue
				}
				ast.Inspect(decl, func(node ast.Node) bool {
					if call, ok := node.(*ast.CallExpr); ok && encryptHelpers[callee(call)] {
						sites[filepath.ToSlash(rel)+" "+name] = true
					}
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return sites
}

// rotatedColumns reads main.go for the columns rotate() really updates: each
// rotateXxx function it calls, the model that function loads and the column it
// writes. A list kept by hand could claim a column the command leaves out.
func rotatedColumns(t *testing.T) map[string]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	funcs := map[string]*ast.FuncDecl{}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			funcs[fn.Name.Name] = fn
		}
	}
	rotate, ok := funcs["rotate"]
	if !ok {
		t.Fatal("main.go has no rotate function")
	}
	columns := map[string]bool{}
	ast.Inspect(rotate.Body, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok {
			if fn, ok := funcs[callee(call)]; ok {
				columns[columnRotatedBy(t, fn)] = true
			}
		}
		return true
	})
	return columns
}

func columnRotatedBy(t *testing.T, fn *ast.FuncDecl) string {
	t.Helper()
	var model, column string
	ast.Inspect(fn, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.ArrayType:
			if elt, ok := n.Elt.(*ast.SelectorExpr); ok {
				model = elt.Sel.Name
			}
		case *ast.CallExpr:
			if callee(n) == "Update" && len(n.Args) == 2 {
				if lit, ok := n.Args[0].(*ast.BasicLit); ok {
					column, _ = strconv.Unquote(lit.Value)
				}
			}
		}
		return true
	})
	if model == "" || column == "" {
		t.Fatalf("%s: cannot tell which column it rotates (model %q, column %q)", fn.Name.Name, model, column)
	}
	return model + "." + column
}

// A new encrypted column must not be missed silently, as relay passwords were
// (#1911). The guard works from the code that encrypts, so it fires whatever
// the column is called. It cannot see a second column written by a function
// already listed in masterKeyWriters, or encryption that calls none of
// encryptHelpers.
func TestRotateCoversEveryEncryptedColumn(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	sites := masterKeyWriteSites(t, repoRoot)
	for site := range sites {
		if _, ok := masterKeyWriters[site]; !ok {
			t.Errorf("%s encrypts with the master key but is not in masterKeyWriters: "+
				"make cmd/rotate-key rotate what it stores, then list it", site)
		}
	}
	rotated := rotatedColumns(t)
	for site, column := range masterKeyWriters {
		if !sites[site] {
			t.Errorf("masterKeyWriters lists %s, which no longer encrypts with the master key", site)
		}
		if !rotated[column] {
			t.Errorf("%s stores %s under the master key, and rotate() does not rotate it", site, column)
		}
	}
}
