package api

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server"
	acme "github.com/rest-mail/rest-mail-server/internal/acme"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/handlers"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/version"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/go-chi/chi/v5"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestDescriptionRegistrationAndWireCoverage(t *testing.T) {
	for _, name := range []string{"RESTMAIL_APNS_KEY_FILE", "RESTMAIL_APNS_KEY_ID", "RESTMAIL_APNS_TEAM_ID", "RESTMAIL_APNS_TOPIC", "RESTMAIL_WEBPUSH_VAPID_KEY_FILE"} {
		t.Setenv(name, "")
	}
	for _, server := range []struct {
		name, environment string
		acme, push, mtls  bool
	}{
		{"production/defaults", "production", false, false, false},
		{"production/acme", "production", true, false, false},
		{"production/push", "production", false, true, false},
		{"production/mtls", "production", false, false, true},
		{"test/defaults", "test", false, false, false},
		{"test/all_options", "test", true, true, true},
	} {
		t.Run(server.name, func(t *testing.T) {
			cfg := &config.Config{Environment: server.environment, InternalMTLSEnabled: server.mtls}
			var client *acme.Client
			if server.push {
				configureDescriptionPush(t)
			}
			if server.acme {
				client = acme.NewClient(acme.ClientConfig{})
			}
			jwt := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
			routers := NewRouters(newFailingGormDB(t), jwt, cfg, nil, client)
			t.Cleanup(routers.Close)
			mounts := mountedDescriptionRoutes(t, routers)
			registered := make(map[string]string)
			for _, op := range routers.Registry.Endpoints() {
				registered[strings.ToUpper(op.Method)+" "+op.Path] = op.OperationID
			}
			for _, route := range routers.Registry.Routes() {
				if _, ok := registered[route]; !ok {
					t.Errorf("typed route %s has no operation metadata", route)
				}
			}
			live, body := liveDescription(t, routers.Public)
			documented := make(map[string]string)
			ids := make(map[string]string)
			for _, op := range operationsOf(live) {
				switch op.method {
				case "get", "put", "post", "delete", "options", "head", "patch", "trace":
				default:
					continue
				}
				route := strings.ToUpper(op.method) + " " + op.path
				if op.id == "" {
					t.Errorf("documented operation %s has no operationId", route)
				} else if other, seen := ids[op.id]; seen {
					t.Errorf("operationId %s names both %s and %s", op.id, other, route)
				}
				ids[op.id], documented[route] = route, op.id
			}
			if problems := descriptionRouteDifferences(mounts, registered, documented); len(problems) != 0 {
				t.Fatal(strings.Join(problems, "\n"))
			}
			for _, op := range []struct {
				id, listener string
				present      bool
			}{
				{"getHealth", "public", true}, {"verifyDelivery", "public", server.environment == "test"},
				{"acmeChallenge", "public", server.acme}, {"headAcmeChallenge", "public", server.acme},
				{"listPushDevices", "public", server.push}, {"registerPushDevice", "public", server.push}, {"removePushDevice", "public", server.push},
				{"checkMailboxAddress", gatewayDescriptionListener(server.mtls), true}, {"deliverMessage", gatewayDescriptionListener(server.mtls), true},
				{"openGatewaySession", gatewayDescriptionListener(server.mtls), true}, {"closeGatewaySession", gatewayDescriptionListener(server.mtls), true},
			} {
				route, found := ids[op.id]
				if found != op.present || found && mounts[route] != op.listener {
					t.Errorf("%s: present=%t listener=%q, want present=%t on %s", op.id, found, mounts[route], op.present, op.listener)
				}
			}
			saved, warnings := routers.Description(version.Version)
			if len(warnings) != 0 {
				t.Fatal(warnings)
			}
			savedJSON, err := json.Marshal(saved)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(body, savedJSON) {
				t.Fatal("live description disagrees with generated export")
			}
			t.Logf("%d operations; gateway listener: %s", len(documented), gatewayDescriptionListener(server.mtls))
			if server.name == "production/defaults" {
				t.Run("inventory_rejects_drift_by_name", func(t *testing.T) {
					assertDescriptionDriftRejected(t, mounts, registered, documented)
				})
			}
		})
	}
	t.Run("verifyDelivery_wire", func(t *testing.T) {
		fixture := newGrantsFixture(t)
		owner := fixture.token(t, "owner")
		pairs := readPublishedDescriptionPairs(t)
		const path = "/api/v1/admin/test/verify"
		doc, _ := liveDescription(t, fixture.router)
		compiler := descriptionSchemaCompiler(t, doc)
		operation := doc["paths"].(map[string]any)[path].(map[string]any)["get"].(map[string]any)
		if got := securityOf(doc, operation); got != "[APIKeyAuth[*] | BearerAuth[*] | CookieAuth[*]]" {
			t.Fatalf("verifyDelivery security = %s", got)
		}
		found := publishedDescriptionPair(t, pairs, "verifyDelivery", "found")
		domain, _ := seedDomain(t, fixture.db, "example.com")
		mailbox := models.Mailbox{DomainID: domain.ID, LocalPart: "recipient", Address: "recipient@example.com", Active: true}
		if err := fixture.db.Create(&mailbox).Error; err != nil {
			t.Fatal(err)
		}
		message := models.Message{ID: 51, MailboxID: mailbox.ID, Folder: "INBOX", MsgID: "<verify@example.net>",
			Subject: `50%_\off`, Sender: "sender@example.net", ReceivedAt: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)}
		if err := fixture.db.Create(&message).Error; err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"recipient_blank", "found"} {
			t.Run(name, func(t *testing.T) {
				pair := publishedDescriptionPair(t, pairs, "verifyDelivery", name)
				response := sendDescriptionPair(fixture.router, requirementRoute{http.MethodGet, path}, owner, pair)
				assertDescribedJSON(t, compiler, http.MethodGet, path, response)
				if name == "recipient_blank" {
					assertRequirementResponse(t, pair, response)
					return
				}
				assertStateExampleWire(t, pair.Response.Status, pair.Response.MediaType, response)
				var actual endpoint.Data[handlers.TestDelivery]
				decodeInto(t, response, &actual)
				if actual.Data.ReceivedAt == nil || !actual.Data.ReceivedAt.Equal(message.ReceivedAt) {
					t.Fatalf("received_at = %v, want instant %s", actual.Data.ReceivedAt, message.ReceivedAt)
				}
				instant := actual.Data.ReceivedAt.UTC()
				actual.Data.ReceivedAt = &instant
				body, err := json.Marshal(actual)
				if err != nil {
					t.Fatal(err)
				}
				assertPublicExampleBody(t, pair.Response.Body, body)
			})
		}
		denied := sendDescriptionPair(fixture.router, requirementRoute{http.MethodGet, path}, fixture.token(t, "viewer"), found)
		if denied.Code != http.StatusForbidden || grantErrorCode(denied) != "forbidden" {
			t.Fatalf("verifyDelivery without wildcard: %d %s", denied.Code, denied.Body)
		}
		assertDescribedJSON(t, compiler, http.MethodGet, path, denied)
		const health = "/api/health"
		available := publishedDescriptionPair(t, pairs, "getHealth", "available")
		healthy := sendDescriptionPair(fixture.router, requirementRoute{http.MethodGet, health}, "", available)
		assertRequirementResponse(t, available, healthy)
		assertDescribedJSON(t, compiler, http.MethodGet, health, healthy)
	})
}

func assertDescriptionDriftRejected(t *testing.T, mounts, registered, documented map[string]string) {
	t.Helper()
	mounts, registered, documented = maps.Clone(mounts), maps.Clone(registered), maps.Clone(documented)
	mounts["GET /new"] = "public"
	delete(documented, "GET /api/health")
	delete(mounts, "GET /api/docs")
	delete(registered, "GET /api/docs")
	want := []string{
		"GET /api/docs (swaggerUI) is documented but not registered",
		"GET /api/health (getHealth) is registered but not documented",
		"GET /new is mounted on public without typed registration",
	}
	if got := descriptionRouteDifferences(mounts, registered, documented); !slices.Equal(got, want) {
		t.Fatalf("drift diagnostics = %q, want %q", got, want)
	}
	t.Log(strings.Join(want, "\n"))
}

func configureDescriptionPush(t *testing.T) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "AuthKey.p8")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RESTMAIL_APNS_KEY_FILE", path)
	t.Setenv("RESTMAIL_APNS_KEY_ID", "KEY1234567")
	t.Setenv("RESTMAIL_APNS_TEAM_ID", "TEAM123456")
}

func gatewayDescriptionListener(internal bool) string {
	if internal {
		return "internal"
	}
	return "public"
}

func mountedDescriptionRoutes(t *testing.T, routers *Routers) map[string]string {
	t.Helper()
	mounts := make(map[string]string)
	for listener, handler := range map[string]http.Handler{"public": routers.Public, "internal": routers.Internal} {
		if handler == nil {
			continue
		}
		routes, ok := handler.(chi.Routes)
		if !ok {
			t.Fatalf("%s listener no longer exposes its registration table", listener)
		}
		if err := chi.Walk(routes, func(method, path string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			route := method + " " + path
			if other, duplicate := mounts[route]; duplicate {
				return fmt.Errorf("%s mounted on both %s and %s", route, other, listener)
			}
			mounts[route] = listener
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	return mounts
}

func descriptionRouteDifferences(mounts, registered, documented map[string]string) []string {
	var problems []string
	for route, listener := range mounts {
		if _, ok := registered[route]; !ok {
			problems = append(problems, fmt.Sprintf("%s is mounted on %s without typed registration", route, listener))
		}
	}
	for route, id := range registered {
		if _, ok := mounts[route]; !ok {
			problems = append(problems, fmt.Sprintf("%s (%s) is registered but not mounted", route, id))
		}
		if got, ok := documented[route]; !ok {
			problems = append(problems, fmt.Sprintf("%s (%s) is registered but not documented", route, id))
		} else if got != id {
			problems = append(problems, fmt.Sprintf("%s is documented as %s, registered as %s", route, got, id))
		}
	}
	for route, id := range documented {
		if _, ok := registered[route]; !ok {
			problems = append(problems, fmt.Sprintf("%s (%s) is documented but not registered", route, id))
		}
	}
	slices.Sort(problems)
	return problems
}

const liveDescriptionSchemaURL = "https://rest-mail.test/live-description.json"

func liveDescription(t *testing.T, handler http.Handler) (map[string]any, []byte) {
	t.Helper()
	response := doRequest(handler, http.MethodGet, "/api/docs/openapi.json", "", "")
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("fetch live description: %d %s", response.Code, response.Body)
	}
	var doc map[string]any
	decoder := json.NewDecoder(bytes.NewReader(response.Body.Bytes()))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	return doc, response.Body.Bytes()
}

func descriptionSchemaCompiler(t *testing.T, doc map[string]any) *jsonschema.Compiler {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft7)
	if err := compiler.AddResource(liveDescriptionSchemaURL, doc); err != nil {
		t.Fatal(err)
	}
	return compiler
}

func assertDescribedJSON(t *testing.T, compiler *jsonschema.Compiler, method, path string, response *httptest.ResponseRecorder) {
	t.Helper()
	pointer := "/paths/" + pointerPart(path) + "/" + strings.ToLower(method) +
		"/responses/" + strconv.Itoa(response.Code) + "/content/" + pointerPart(response.Header().Get("Content-Type")) + "/schema"
	schema, err := compiler.Compile(liveDescriptionSchemaURL + "#" + pointer)
	if err != nil {
		t.Fatalf("missing response schema for %s %s status %d: %v", method, path, response.Code, err)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(response.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(value); err != nil {
		t.Fatalf("%s %s status %d does not match its generated schema: %v", method, path, response.Code, err)
	}
}

func readPublishedDescriptionPairs(t *testing.T) oapifly.ExampleFile {
	t.Helper()
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	return pairs
}

func publishedDescriptionPair(t *testing.T, pairs oapifly.ExampleFile, operation, name string) oapifly.ExamplePair {
	t.Helper()
	pair, ok := pairs[operation][name]
	if !ok {
		t.Fatalf("published example missing: %s/%s", operation, name)
	}
	return pair
}

func sendDescriptionPair(handler http.Handler, route requirementRoute, token string, pair oapifly.ExamplePair) *httptest.ResponseRecorder {
	req := requirementRequest(route, pair)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if pair.Request.MediaType != "" {
		req.Header.Set("Content-Type", pair.Request.MediaType)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}
