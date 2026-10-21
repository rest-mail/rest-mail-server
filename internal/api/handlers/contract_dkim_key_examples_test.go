package handlers

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/db/models"
	yaml "go.yaml.in/yaml/v2"
)

const dkimKeyContractPath = "/api/v1/admin/dkim/{id}"

// Read PUT locally so this slice does not change the shared POST reader.
func dkimKeyContractExamples(t *testing.T) (map[string]interface{}, map[string]map[string]interface{}) {
	t.Helper()
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]struct {
			Put struct {
				OperationID string `yaml:"operationId"`
				RequestBody struct {
					Content map[string]namedExamples `yaml:"content"`
				} `yaml:"requestBody"`
				Responses map[string]struct {
					Content map[string]namedExamples `yaml:"content"`
				} `yaml:"responses"`
			} `yaml:"put"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatal(err)
	}
	op := doc.Paths[dkimKeyContractPath].Put
	if op.OperationID != "setDKIMKey" {
		t.Fatalf("operation is %q", op.OperationID)
	}
	requests := map[string]interface{}{}
	responses := map[string]map[string]interface{}{}
	for name, example := range op.RequestBody.Content["application/json"].Examples {
		requests[name] = jsonValue(t, example.Value)
	}
	for status, response := range op.Responses {
		for name, example := range response.Content["application/json"].Examples {
			if responses[status] == nil {
				responses[status] = map[string]interface{}{}
			}
			responses[status][name] = jsonValue(t, example.Value)
		}
	}
	return requests, responses
}

func TestDKIMKeyContract_ExamplePairs(t *testing.T) {
	requests, responses := dkimKeyContractExamples(t)
	got := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		got[status] = names(examples)
	}
	want := map[string][]string{
		"request": {"body_array", "installed", "private_key_invalid", "private_key_missing", "selector_invalid", "selector_missing"},
		"200":     {"installed"},
		"400":     {"body_array", "private_key_missing", "selector_missing"},
		"422":     {"private_key_invalid", "selector_invalid"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pairs = %v, want %v", got, want)
	}
	// Pin the requests too: changing a placeholder or repairing a refusal in
	// the spec must not silently leave this test exercising a different case.
	bodies := map[string]interface{}{
		"installed":           map[string]interface{}{"selector": "contract", "private_key": "fixture-signing-key"},
		"body_array":          []interface{}{},
		"selector_missing":    map[string]interface{}{"private_key": "fixture-signing-key"},
		"private_key_missing": map[string]interface{}{"selector": "contract"},
		"selector_invalid":    map[string]interface{}{"selector": "invalid_selector", "private_key": "fixture-signing-key"},
		"private_key_invalid": map[string]interface{}{"selector": "contract", "private_key": "not a key"},
	}
	if !reflect.DeepEqual(requests, bodies) {
		t.Fatalf("requests = %s, want %s", mustJSON(requests), mustJSON(bodies))
	}
}

func TestDKIMKeyContract_DocumentedExamples(t *testing.T) {
	db, err := connectTestDB()
	if err != nil {
		t.Skipf("no test database: %v", err)
	}
	if err := db.AutoMigrate(&models.Domain{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	installedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	domain := models.Domain{
		Name:         fmt.Sprintf("contract-dkim-%d.test", time.Now().UnixNano()),
		DKIMSelector: "old", DKIMPrivateKey: "old-private-key", DKIMCreatedAt: &installedAt,
	}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	// Reload to compare the complete persisted row, including database defaults.
	if err := tx.First(&domain, domain.ID).Error; err != nil {
		t.Fatal(err)
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	key := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	requests, responses := dkimKeyContractExamples(t)
	send := func(name string) *httptest.ResponseRecorder {
		t.Helper()
		body := requests[name]
		if object, ok := body.(map[string]interface{}); ok {
			copy := map[string]interface{}{}
			for field, value := range object {
				copy[field] = value
			}
			if copy["private_key"] == "fixture-signing-key" {
				copy["private_key"] = key
			}
			body = copy
		}
		request := httptest.NewRequest(http.MethodPut, dkimKeyContractPath, bytes.NewReader(mustJSON(body)))
		route := chi.NewRouteContext()
		route.URLParams.Add("id", fmt.Sprint(domain.ID))
		request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
		rr := httptest.NewRecorder()
		NewDKIMHandler(tx, dkimTestMasterKey).SetKey(rr, request)
		return rr
	}
	for _, status := range []string{"400", "422"} {
		for name, want := range responses[status] {
			t.Run(name, func(t *testing.T) {
				rr := send(name)
				if fmt.Sprint(rr.Code) != status {
					t.Fatalf("status %d, want %s: %s", rr.Code, status, rr.Body)
				}
				var got interface{}
				if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("response %s, want %s", rr.Body, mustJSON(want))
				}
				var after models.Domain
				if err := tx.First(&after, domain.ID).Error; err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(after, domain) {
					t.Fatal("refused request changed the stored domain")
				}
			})
		}
	}
	rr := send("installed")
	if rr.Code != http.StatusOK {
		t.Fatalf("installed status %d: %s", rr.Code, rr.Body)
	}
	var response struct {
		Data dkimEntry `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.DomainID != domain.ID || response.Data.Domain != domain.Name || response.Data.Selector != "contract" || !response.Data.HasKey || response.Data.CreatedAt == nil || response.Data.AgeSeconds == nil {
		t.Fatalf("installed response: %+v", response.Data)
	}
	var after models.Domain
	if err := tx.First(&after, domain.ID).Error; err != nil {
		t.Fatal(err)
	}
	stored, err := models.LoadDKIMPrivateKey(after.DKIMPrivateKey, dkimTestMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	if stored != key || after.DKIMSelector != "contract" || after.DKIMCreatedAt == nil || !after.DKIMCreatedAt.After(installedAt) {
		t.Fatal("successful request did not persist the new signing key")
	}
}
