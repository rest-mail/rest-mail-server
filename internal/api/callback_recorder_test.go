package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

// changesCreateCallback matches a call that changes the list of gorm's create
// callbacks: db.Callback().Create().Before("gorm:create").Register(...).
var changesCreateCallback = regexp.MustCompile(`\.Callback\(\)\.Create\(\).*\.(Register|Remove|Replace)$`)

// stopTraceRecorder flushes the router's trace Recorder and stops its goroutine.
//
// A router's Recorder writes traces with CreateInBatches on the database it was
// given. Changing a create callback rewrites gorm's compiled list for that whole
// database, and no session of it has a list of its own, so a test that changes one
// while the Recorder runs races with its next flush. Register the callback before
// NewRouters, or call this first. The traces of later deliveries are then dropped,
// so a test that reads them cannot use this.
func stopTraceRecorder(routers *Routers) { routers.Recorder.Shutdown() }

// A test that changes a create callback with the Recorder running fails only under
// -race, and only when a flush lands in that moment. This reads the tests instead,
// so a run without -race fails too, whatever the timing.
func TestNoTestChangesACreateCallbackWhileTheRecorderRuns(t *testing.T) {
	names, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("found no tests to read")
	}
	fset := token.NewFileSet()
	changes := 0
	for _, name := range names {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			function, ok := decl.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			for _, change := range unsafeCreateCallbackChanges(function, &changes) {
				t.Errorf("%s: this changes a create callback while the trace Recorder may be running, "+
					"which races with its next flush: register it before NewRouters, or call stopTraceRecorder first",
					fset.Position(change))
			}
		}
	}
	// The test below and pipeline_configuration_rows_test.go change some, so none
	// found means this no longer recognises them.
	if changes == 0 {
		t.Fatal("found no create callback changes, so this checked nothing")
	}
}

// unsafeCreateCallbackChanges finds the create callback changes in one function
// that come neither before its NewRouters nor after it stops the Recorder.
func unsafeCreateCallbackChanges(function *ast.FuncDecl, changes *int) []token.Pos {
	var changed, routersMade, stopped []token.Pos
	ast.Inspect(function.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch name := types.ExprString(call.Fun); {
		case changesCreateCallback.MatchString(name):
			changed = append(changed, call.Pos())
		case name == "NewRouters":
			routersMade = append(routersMade, call.Pos())
		case name == "stopTraceRecorder":
			stopped = append(stopped, call.Pos())
		}
		return true
	})
	*changes += len(changed)
	var unsafe []token.Pos
	for _, change := range changed {
		stoppedFirst := anyBefore(stopped, change)
		routerAfter := len(routersMade) > 0 && !anyBefore(routersMade, change)
		if !stoppedFirst && !routerAfter {
			unsafe = append(unsafe, change)
		}
	}
	return unsafe
}

func anyBefore(positions []token.Pos, limit token.Pos) bool {
	for _, position := range positions {
		if position < limit {
			return true
		}
	}
	return false
}

func TestTheCreateCallbackCheckFollowsTheRouter(t *testing.T) {
	for name, test := range map[string]struct {
		body   string
		unsafe int
	}{
		"before the router":       {`db.Callback().Create().Before("gorm:create").Register("c", f); NewRouters(db)`, 0},
		"under a running router":  {`NewRouters(db); db.Callback().Create().Before("gorm:create").Register("c", f)`, 1},
		"removed, router running": {`NewRouters(db); db.Callback().Create().Remove("c")`, 1},
		"in a fixture's router":   {`f := newCredentialFixture(t); f.db.Callback().Create().Remove("c")`, 1},
		"after stopping":          {`f := newCredentialFixture(t); stopTraceRecorder(f.routers); f.db.Callback().Create().Remove("c")`, 0},
		"a query callback":        {`NewRouters(db); db.Callback().Query().Remove("c")`, 0},
	} {
		t.Run(name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "x_test.go", "package api\nfunc x() {\n"+test.body+"\n}\n", 0)
			if err != nil {
				t.Fatal(err)
			}
			changes := 0
			if got := unsafeCreateCallbackChanges(file.Decls[0].(*ast.FuncDecl), &changes); len(got) != test.unsafe {
				t.Fatalf("found %d unsafe, want %d", len(got), test.unsafe)
			}
		})
	}
}

// Under -race this fails if stopTraceRecorder does not stop the Recorder: traces
// keep arriving, so flushes run through the create callbacks being changed.
func TestChangingACreateCallbackAfterStoppingTheRecorderDoesNotRace(t *testing.T) {
	fixture := newCredentialFixture(t)
	stopTraceRecorder(fixture.routers)

	done := make(chan struct{})
	var traces sync.WaitGroup
	traces.Add(1)
	go func() {
		defer traces.Done()
		for {
			select {
			case <-done:
				return
			default:
				fixture.routers.Recorder.Record(models.MessageTrace{Outcome: "stored"})
			}
		}
	}()
	// A flush reads the list once, and the database pool's lock orders most reads
	// after any write made before a query, so this needs no query of its own and
	// long enough for several flushes (one every 500ms at the least).
	const callback = "stopped-recorder-callback"
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if err := fixture.db.Callback().Create().Before("gorm:create").Register(callback, func(*gorm.DB) {}); err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Callback().Create().Remove(callback); err != nil {
			t.Fatal(err)
		}
	}
	close(done)
	traces.Wait()
}
