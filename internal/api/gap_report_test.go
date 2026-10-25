package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
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

// gapGenerators includes disabled-by-default push routes and non-production
// testing routes. A production-only snapshot must never close their issues.
// No network or live database is needed to construct these actual registrations.
func gapGenerators(t *testing.T) []*oapifly.Generator {
	t.Helper()
	var generators []*oapifly.Generator
	for _, routers := range gapRouters(t) {
		generators = append(generators, routers.generator("report"))
	}
	return generators
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
		routers := NewRouters(newFailingGormDB(t), jwt, &config.Config{Environment: environment}, nil)
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

// TestAPIGapReport is also the report command used by api-gaps.yml. It exports
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
		report.Warnings = append(report.Warnings, warnings[key])
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

func TestStrictAPIAreas(t *testing.T) {
	for _, generator := range gapGenerators(t) {
		if err := generator.Check(); err != nil {
			t.Error(err)
		}
	}
}
