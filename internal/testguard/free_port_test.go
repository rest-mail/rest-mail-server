// Package testguard holds tests that scan the repository for patterns that must
// not come back.
package testguard

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A test that listens on port 0, closes the listener and then uses the port
// races every other process on the machine for it, and loses now and then with
// "bind: address already in use". Each loss costs a CI run (#1964). Keep the
// listener open and hand it to the server instead: the SMTP gateway, the IMAP
// server and the metrics server each have a Serve that takes one.
//
// This reads one function at a time, so it does not see a port that leaves
// through a helper or a struct field and a close that happens elsewhere.
func TestNoFreePortProbes(t *testing.T) {
	root := repoRoot(t)
	var found []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == "node_modules" || name == "testdata" || (name != "." && strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(source), `:0"`) {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		probes, err := freePortProbes(relative, source)
		if err != nil {
			return err
		}
		found = append(found, probes...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Fatalf("listeners closed so their port can be used afterwards, which races other processes for it; "+
			"keep the listener open and hand it to the server:\n%s", strings.Join(found, "\n"))
	}
}

func TestFreePortProbesFindsTheRace(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want bool
	}{
		"closed, then the port is used": {`
			l, _ := net.Listen("tcp", "127.0.0.1:0")
			port := l.Addr().(*net.TCPAddr).Port
			_ = l.Close()
			use(port)`, true},
		"closed in an if, port through a second variable": {`
			l, _ := net.Listen("tcp", "127.0.0.1:0")
			addr := l.Addr().(*net.TCPAddr)
			port := addr.Port
			if err := l.Close(); err != nil { panic(err) }
			use(port)`, true},
		"closed, then the address is read": {`
			l, _ := net.Listen("tcp", "127.0.0.1:0")
			l.Close()
			use(l.Addr().String())`, true},
		"deferred close, port returned": {`
			l, _ := net.Listen("tcp", ":0")
			defer l.Close()
			return l.Addr().(*net.TCPAddr).Port`, true},
		"deferred closure, port returned through a variable": {`
			l, _ := net.Listen("tcp", "127.0.0.1:0")
			defer func() { _ = l.Close() }()
			port := l.Addr().(*net.TCPAddr).Port
			return port`, true},
		"UDP": {`
			c, _ := net.ListenPacket("udp", "127.0.0.1:0")
			port := c.LocalAddr().(*net.UDPAddr).Port
			c.Close()
			use(port)`, true},
		"stays open until the test ends": {`
			l, _ := net.Listen("tcp", "127.0.0.1:0")
			defer l.Close()
			port := l.Addr().(*net.TCPAddr).Port
			use(port)`, false},
		"closed by a cleanup": {`
			l, _ := net.Listen("tcp", "127.0.0.1:0")
			t.Cleanup(func() { _ = l.Close() })
			return l.Addr().(*net.TCPAddr).Port`, false},
		"closed after the last use of the port": {`
			l, _ := net.Listen("tcp", "127.0.0.1:0")
			port := l.Addr().(*net.TCPAddr).Port
			use(port)
			l.Close()`, false},
		"closed on a failed try, nothing kept": {`
			u, _ := net.ListenPacket("udp", "127.0.0.1:0")
			if _, err := net.Listen("tcp", u.LocalAddr().String()); err != nil { u.Close() }`, false},
		"second listener on the port, first closed only if that fails": {`
			for {
				u, _ := net.ListenPacket("udp", "127.0.0.1:0")
				l, err := net.Listen("tcp", u.LocalAddr().String())
				if err == nil {
					udp, tcp = u, l
					break
				}
				u.Close()
			}
			use(tcp)`, false},
		"a fixed port": {`
			l, _ := net.Listen("tcp", "127.0.0.1:2525")
			port := l.Addr().(*net.TCPAddr).Port
			l.Close()
			use(port)`, false},
	} {
		t.Run(name, func(t *testing.T) {
			source := "package p\nfunc f() {" + tc.body + "\n}"
			found, err := freePortProbes("p.go", []byte(source))
			if err != nil {
				t.Fatal(err)
			}
			if got := len(found) > 0; got != tc.want {
				t.Fatalf("flagged = %v (%v), want %v", got, found, tc.want)
			}
		})
	}
}

// repoRoot is the directory of the nearest go.mod above this package.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's directory")
		}
		dir = parent
	}
}

// freePortProbes reports, as file:line, each listener in source that is opened
// on port 0 and closed while its address is still wanted.
func freePortProbes(path string, source []byte) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, source, 0)
	if err != nil {
		return nil, err
	}
	var found []string
	ast.Inspect(file, func(n ast.Node) bool {
		var body *ast.BlockStmt
		switch fn := n.(type) {
		case *ast.FuncDecl:
			body = fn.Body
		case *ast.FuncLit:
			body = fn.Body
		}
		if body != nil {
			for _, pos := range probesIn(body) {
				found = append(found, fmt.Sprintf("%s:%d", path, fset.Position(pos).Line))
			}
		}
		return true
	})
	return found, nil
}

// probesIn checks one function body, leaving out the functions nested in it: a
// close inside a t.Cleanup runs after the test, not before the port is used.
func probesIn(body *ast.BlockStmt) []token.Pos {
	var probes []token.Pos
	for name, opened := range portZeroListeners(body) {
		derived := map[string]bool{}
		var closedAt token.Pos
		deferred, used, returned := false, false, false
		inspectFunction(body, func(n ast.Node) {
			switch n := n.(type) {
			case *ast.AssignStmt:
				// A second listener on the same port holds the port, so it is
				// not a copy of the address to be used once the first has gone.
				if n.Pos() > opened && !isListenCall(n.Rhs[0]) && readsAddress(n.Rhs, name, derived) {
					for _, target := range n.Lhs {
						if id, ok := target.(*ast.Ident); ok && id.Name != "_" && id.Name != "err" {
							derived[id.Name] = true
						}
					}
				}
			case *ast.DeferStmt:
				if closes(n, name) {
					deferred = true
				}
			case *ast.CallExpr:
				if isCloseOf(n, name) && closedAt == token.NoPos && !insideDefer(body, n) {
					closedAt = n.Pos()
				}
			case *ast.ReturnStmt:
				for _, result := range n.Results {
					if readsAddress([]ast.Expr{result}, name, derived) {
						returned = true
					}
				}
			}
		})
		if closedAt != token.NoPos {
			ast.Inspect(body, func(n ast.Node) bool {
				if expr, ok := n.(ast.Expr); ok && n.Pos() > closedAt && readsAddress([]ast.Expr{expr}, name, derived) {
					used = true
				}
				return !used
			})
		}
		if used || (deferred && returned) {
			probes = append(probes, opened)
		}
	}
	return probes
}

// portZeroListeners names the variables a body assigns a listener on port 0 to.
func portZeroListeners(body *ast.BlockStmt) map[string]token.Pos {
	listeners := map[string]token.Pos{}
	inspectFunction(body, func(n ast.Node) {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Rhs) != 1 || len(assign.Lhs) == 0 {
			return
		}
		if !isListenCall(assign.Rhs[0]) {
			return
		}
		for _, arg := range assign.Rhs[0].(*ast.CallExpr).Args {
			literal, ok := arg.(*ast.BasicLit)
			if ok && literal.Kind == token.STRING && strings.HasSuffix(literal.Value, `:0"`) {
				if id, ok := assign.Lhs[0].(*ast.Ident); ok {
					listeners[id.Name] = assign.Pos()
				}
			}
		}
	})
	return listeners
}

// isListenCall says whether expr is a call to a function named Listen or
// starting with it, such as net.Listen, tls.Listen or net.ListenPacket.
func isListenCall(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	return ok && strings.HasPrefix(selector.Sel.Name, "Listen")
}

// inspectFunction visits a function body without entering nested functions.
func inspectFunction(body *ast.BlockStmt, visit func(ast.Node)) {
	ast.Inspect(body, func(n ast.Node) bool {
		if _, nested := n.(*ast.FuncLit); nested {
			return false
		}
		if n != nil {
			visit(n)
		}
		return true
	})
}

// readsAddress says whether exprs read the listener's address, directly or
// through a variable already derived from it.
func readsAddress(exprs []ast.Expr, listener string, derived map[string]bool) bool {
	reads := false
	for _, expr := range exprs {
		ast.Inspect(expr, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Ident:
				reads = reads || derived[n.Name]
			case *ast.CallExpr:
				if method, ok := methodOf(n, listener); ok && (method == "Addr" || method == "LocalAddr") {
					reads = true
				}
			}
			return !reads
		})
	}
	return reads
}

// closes says whether a defer closes the listener, directly or in a closure.
func closes(stmt *ast.DeferStmt, listener string) bool {
	found := false
	ast.Inspect(stmt.Call, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && isCloseOf(call, listener) {
			found = true
		}
		return !found
	})
	return found
}

// insideDefer says whether call is the call a defer statement in body makes.
func insideDefer(body *ast.BlockStmt, call *ast.CallExpr) bool {
	inside := false
	inspectFunction(body, func(n ast.Node) {
		if stmt, ok := n.(*ast.DeferStmt); ok && stmt.Call == call {
			inside = true
		}
	})
	return inside
}

func isCloseOf(call *ast.CallExpr, listener string) bool {
	method, ok := methodOf(call, listener)
	return ok && method == "Close"
}

// methodOf returns the method name of a call on the named variable.
func methodOf(call *ast.CallExpr, variable string) (string, bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	id, ok := selector.X.(*ast.Ident)
	if !ok || id.Name != variable {
		return "", false
	}
	return selector.Sel.Name, true
}
