package api

import (
	"bytes"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/examplepairs"
)

// requirementBodyCap is the group body cap the router under test runs with.
const requirementBodyCap = 512

// requirementSetups are the requirements every operation meets the same way, through
// the production router: no credential, a session store that cannot be read, a body
// over the cap, and a database that fails once the request reaches its handler (#1237).
var requirementSetups = map[string]bool{
	"principal=anonymous": true,
	"fault=session_store": true,
	"body=over_cap":       true,
	"fault=database":      true,
}

// requirementOf is a pair's single requirement as kind=value, or "" when it has none
// or more than one.
func requirementOf(pair oapifly.ExamplePair) string {
	if len(pair.Requires) != 1 {
		return ""
	}
	for kind, value := range pair.Requires {
		return kind + "=" + value
	}
	return ""
}

// Every description pair whose requirement is one of requirementSetups is sent through
// the production router with that requirement set up, and must answer exactly as the
// pair says: status, media type and body.
func TestRequirementExamplesThroughTheProductionRouter(t *testing.T) {
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	paths, _ := describedAPI(t)
	type at struct{ path, method string }
	byOperation := map[string]at{}
	for path, methods := range paths {
		for method, op := range methods {
			byOperation[op.OperationID] = at{path, strings.ToUpper(method)}
		}
	}
	jwt := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	cfg := &config.Config{Environment: "test", APIMaxBodyBytes: requirementBodyCap, MasterKey: accessMasterKey}
	// The session and grant checks are chosen when a router is built: the package's
	// seams stand them aside for stubbed, and useRealGrants puts the real ones back
	// for the router whose session store cannot be read.
	stubbed := NewRouter(newFailingGormDB(t), jwt, cfg, nil)
	token := adminToken(t, jwt, []string{"*"})
	useRealGrants(t)
	real := NewRouter(newFailingGormDB(t), jwt, cfg, nil)

	operations := make([]string, 0, len(pairs))
	for op := range pairs {
		operations = append(operations, op)
	}
	sort.Strings(operations)
	for _, op := range operations {
		route, described := byOperation[op]
		for name, pair := range pairs[op] {
			requirement := requirementOf(pair)
			if !requirementSetups[requirement] || !described {
				continue
			}
			t.Run(op+"/"+name, func(t *testing.T) {
				examplepairs.Proven(t, op, name)
				body := []byte(pair.Request.Body)
				req := httptest.NewRequest(route.method, messageCollectionReadURI(route.path, pair), bytes.NewReader(body))
				if requirement == "body=over_cap" {
					// Routes carry caps of their own above the group's; a declared length
					// past every one is refused before the body is read.
					req.ContentLength = 1 << 40
				}
				if len(body) > 0 {
					req.Header.Set("Content-Type", "application/json")
				}
				for key, value := range pair.Request.Header {
					if text, ok := value.(string); ok {
						req.Header.Set(key, text)
					}
				}
				if requirement != "principal=anonymous" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				router := stubbed
				if requirement == "fault=session_store" {
					router = real
				}
				rr := httptest.NewRecorder()
				router.ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status {
					t.Fatalf("%s %s with %s: got %d, want %d: %s", route.method, route.path, requirement, rr.Code, pair.Response.Status, rr.Body.String())
				}
				if pair.Response.MediaType != "" && !strings.HasPrefix(rr.Header().Get("Content-Type"), pair.Response.MediaType) {
					t.Fatalf("content type %q, want %q", rr.Header().Get("Content-Type"), pair.Response.MediaType)
				}
				if len(pair.Response.Body) > 0 {
					assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				}
			})
		}
	}
}
