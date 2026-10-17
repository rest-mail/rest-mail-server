package api

import (
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/restmail/restmail"
	yaml "go.yaml.in/yaml/v2"
)

// Each operation's required capability is in the spec as OpenAPI 3.1
// security-requirement roles (#376), for example
//
//	security:
//	  - CookieAuth: [domains:read]
//	  - BearerAuth: [domains:read]
//
// so the authorization matrix can be checked from the spec itself. On the
// admin surface the router enforces the capability (RequireCapability), so
// this test holds the two together: for every admin operation in the spec, a
// session holding exactly its role reaches the handler, and one holding every
// other capability is refused with 403. An admin operation documented with no
// role must be reachable by an admin holding nothing.
//
// The mail routes check their capability (mail:read, mail:write, mail:send,
// mailbox:manage) in the handler, on the account the request names
// (handlers.mayReach), so there is no route table to read it from; their
// roles are kept in sync by the handlers' own tests, and the contract run
// checks them once vertrag has an authorization phase
// (antimatter-studios/vertrag#71).

type specOperation struct {
	method, path, operationID string
	roles                     []string
}

func adminOperations(t *testing.T) []specOperation {
	t.Helper()
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatalf("read the spec: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]interface{} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	var ops []specOperation
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/api/v1/admin/") {
			continue
		}
		for method, raw := range item {
			switch method {
			case "get", "post", "put", "patch", "delete":
			default:
				continue
			}
			var op struct {
				OperationID string                `yaml:"operationId"`
				Security    []map[string][]string `yaml:"security"`
			}
			encoded, err := yaml.Marshal(raw)
			if err != nil {
				t.Fatalf("%s %s: %v", method, path, err)
			}
			if err := yaml.Unmarshal(encoded, &op); err != nil {
				t.Fatalf("%s %s: %v", method, path, err)
			}
			// Every scheme must name the same roles: a capability is one
			// fact, whichever way the session's token arrives.
			var roles []string
			for i, requirement := range op.Security {
				for scheme, r := range requirement {
					got := append([]string(nil), r...)
					sort.Strings(got)
					if i == 0 {
						roles = got
					} else if strings.Join(got, ",") != strings.Join(roles, ",") {
						t.Errorf("%s: %s names roles %v, another scheme %v", op.OperationID, scheme, got, roles)
					}
				}
			}
			ops = append(ops, specOperation{strings.ToUpper(method), path, op.OperationID, roles})
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].operationID < ops[j].operationID })
	if len(ops) == 0 {
		t.Fatal("the spec has no admin operations")
	}
	return ops
}

var pathParameter = regexp.MustCompile(`\{[^}]+\}`)

func concretePath(path string) string {
	return pathParameter.ReplaceAllStringFunc(path, func(p string) string {
		if p == "{ip}" {
			return "192.0.2.1"
		}
		return "1"
	})
}

func TestOpenAPI_AdminRolesAreWhatTheRouterEnforces(t *testing.T) {
	router, jwtSvc := newTestRouter(t)
	ops := adminOperations(t)

	// Every capability the admin surface documents, so "everything but the
	// one it needs" is a real set of capabilities.
	all := map[string]bool{}
	for _, op := range ops {
		for _, r := range op.roles {
			if r != "*" {
				all[r] = true
			}
		}
	}

	for _, op := range ops {
		t.Run(op.operationID, func(t *testing.T) {
			path := concretePath(op.path)
			if len(op.roles) > 1 {
				t.Fatalf("%s documents %d roles; an admin operation needs one capability", op.operationID, len(op.roles))
			}
			if len(op.roles) == 0 {
				rr := doRequest(router, op.method, path, adminToken(t, jwtSvc, nil), "")
				assertReachedHandler(t, rr)
				return
			}
			role := op.roles[0]

			rr := doRequest(router, op.method, path, adminToken(t, jwtSvc, []string{role}), "")
			assertReachedHandler(t, rr)

			var others []string
			for c := range all {
				if c != role {
					others = append(others, c)
				}
			}
			rr = doRequest(router, op.method, path, adminToken(t, jwtSvc, others), "")
			if rr.Code != http.StatusForbidden || errorMessage(t, rr) != "Insufficient permissions" {
				t.Errorf("%s %s with every capability but %s: %d %s; the spec says it needs %s",
					op.method, op.path, role, rr.Code, rr.Body.String(), role)
			}
		})
	}
}
