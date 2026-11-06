package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	acmeclient "github.com/rest-mail/rest-mail-server/internal/acme"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
)

type gapOperation struct {
	OperationID string `json:"operationId"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	File        string `json:"file"`
	Line        int    `json:"line"`
}

type gapReport struct {
	Version    int               `json:"version"`
	Operations []gapOperation    `json:"operations"`
	Warnings   []oapifly.Warning `json:"warnings"`
}

// gapGenerators includes disabled-by-default push and ACME routes and non-production
// testing routes. A production-only snapshot must never close their issues.
// No network or live database is needed to construct these actual registrations.
// They are built once per test binary and read by about 70 tests: each generator
// validates every example pair, and its Report and Generate only read the result.
func gapGenerators(t *testing.T) []*oapifly.Generator {
	t.Helper()
	builtGapGenerators.once.Do(func() {
		for _, routers := range gapRouters(t) {
			builtGapGenerators.generators = append(builtGapGenerators.generators, routers.generator("report"))
		}
	})
	if len(builtGapGenerators.generators) == 0 {
		t.Fatal("the gap generators could not be built: see the first test that asked for them")
	}
	return builtGapGenerators.generators
}

var builtGapGenerators struct {
	once       sync.Once
	generators []*oapifly.Generator
}

func gapRouters(t *testing.T) []*Routers {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "report-apns.p8")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RESTMAIL_APNS_KEY_FILE", keyFile)
	t.Setenv("RESTMAIL_APNS_KEY_ID", "REPORTKEY1")
	t.Setenv("RESTMAIL_APNS_TEAM_ID", "REPORTTEAM")
	t.Setenv("RESTMAIL_APNS_TOPIC", "")
	var all []*Routers
	for _, environment := range []string{"production", "test"} {
		jwt := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
		routers := NewRouters(newFailingGormDB(t), jwt, &config.Config{Environment: environment}, nil, acmeclient.NewClient(acmeclient.ClientConfig{}))
		t.Cleanup(routers.Close)
		all = append(all, routers)
	}
	return all
}

// gapSources locates the registration's literal operation ID rather than guessing
// a handler name from it. Reflection reports need this fallback source location.
func gapSources(t *testing.T) map[string]gapOperation {
	t.Helper()
	sources := map[string]gapOperation{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			name, ok := kv.Key.(*ast.Ident)
			if !ok || name.Name != "OperationID" {
				return true
			}
			value, ok := kv.Value.(*ast.BasicLit)
			if !ok || value.Kind != token.STRING {
				return true
			}
			id, err := strconv.Unquote(value.Value)
			if err != nil {
				t.Fatal(err)
			}
			pos := fset.Position(value.Pos())
			sources[id] = gapOperation{File: filepath.ToSlash(filepath.Join("internal/api", pos.Filename)), Line: pos.Line}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sources
}

// TestAPIGapReport is the report command for API gaps. It exports
// warnings without failing on them; strict enforcement has its own test below.
func TestAPIGapReport(t *testing.T) {
	sources := gapSources(t)
	operations := map[string]gapOperation{}
	warnings := map[string]oapifly.Warning{}
	for _, generator := range gapGenerators(t) {
		doc := generator.Generate()
		data, err := json.Marshal(doc["paths"])
		if err != nil {
			t.Fatal(err)
		}
		var paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		}
		if err := json.Unmarshal(data, &paths); err != nil {
			t.Fatal(err)
		}
		for path, methods := range paths {
			for method, op := range methods {
				if op.OperationID == "" {
					t.Fatalf("%s %s has no operation ID; cannot reconcile its warnings", method, path)
				}
				source := sources[op.OperationID]
				if source.File == "" {
					t.Fatalf("%s has no source location", op.OperationID)
				}
				source.OperationID, source.Method, source.Path = op.OperationID, strings.ToUpper(method), path
				if old, ok := operations[op.OperationID]; ok && old != source {
					t.Fatalf("ambiguous operation ID %s", op.OperationID)
				}
				operations[op.OperationID] = source
			}
		}
		for _, warning := range generator.Report() {
			if warning.File == "" {
				source := sources[warning.OperationID]
				warning.File, warning.Line = source.File, source.Line
			}
			data, err := json.Marshal(warning)
			if err != nil {
				t.Fatal(err)
			}
			warnings[string(data)] = warning
		}
	}
	report := gapReport{Version: 1, Operations: []gapOperation{}, Warnings: []oapifly.Warning{}}
	for _, op := range operations {
		report.Operations = append(report.Operations, op)
	}
	sort.Slice(report.Operations, func(i, j int) bool { return report.Operations[i].OperationID < report.Operations[j].OperationID })
	keys := make([]string, 0, len(warnings))
	for key := range warnings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if gapWarningApplies(operations, warnings[key]) {
			report.Warnings = append(report.Warnings, warnings[key])
		}
	}
	for _, id := range []string{"listPushDevices", "registerPushDevice", "removePushDevice", "seedTestData", "login"} {
		if _, ok := operations[id]; !ok {
			t.Fatalf("report omits configuration-dependent operation %s", id)
		}
	}
	if path := os.Getenv("RESTMAIL_API_GAP_REPORT"); path != "" {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// A pair is orphaned only when no supported configuration registers its
// operation. Other warnings, including invalid pairs in enabled profiles,
// remain visible. Reconcile after collecting every profile's operations.
func gapWarningApplies(operations map[string]gapOperation, warning oapifly.Warning) bool {
	_, registered := operations[warning.OperationID]
	return warning.Kind != "example-orphaned" || !registered
}

// TestStrictAPIAreas checks every supported configuration. One that omits an operation,
// as production omits the test routes, reports that operation's pairs as orphaned; the
// configuration that registers it checks them, so only those orphans are excused.
func TestStrictAPIAreas(t *testing.T) {
	generators := gapGenerators(t)
	documented := make([]map[string]bool, len(generators))
	registered := map[string]bool{}
	for i, generator := range generators {
		documented[i] = generatedOperationIDs(t, generator)
		for id := range documented[i] {
			registered[id] = true
		}
	}
	for i, generator := range generators {
		err := generator.Check()
		var strict *oapifly.StrictError
		if !errors.As(err, &strict) {
			if err != nil {
				t.Error(err)
			}
			continue
		}
		for _, w := range strict.Warnings {
			if w.Kind == oapifly.WarnExampleOrphaned && !documented[i][w.OperationID] && registered[w.OperationID] {
				continue
			}
			t.Errorf("%s: %s", w.Kind, w.Message)
		}
	}
}

// generatedOperationIDs are the operations a configuration's description documents.
func generatedOperationIDs(t *testing.T, generator *oapifly.Generator) map[string]bool {
	t.Helper()
	data, err := json.Marshal(generator.Generate()["paths"])
	if err != nil {
		t.Fatal(err)
	}
	var paths map[string]map[string]struct {
		OperationID string `json:"operationId"`
	}
	if err := json.Unmarshal(data, &paths); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, methods := range paths {
		for _, op := range methods {
			ids[op.OperationID] = true
		}
	}
	return ids
}
