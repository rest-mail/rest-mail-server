package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

func TestContractFuzzSendRecipientDomainValidation(t *testing.T) {
	for _, field := range []string{"to", "cc", "bcc"} {
		for _, test := range []struct{ name, address string }{
			{"invalid punycode", "user@xn--a.example"},
			{"overlong domain label", "user@" + strings.Repeat("a", 64) + ".example"},
			{"missing domain", "user@"},
			{"missing local part", "@example.test"},
			{"invalid local part", "two words@example.test"},
		} {
			t.Run(field+"/"+test.name, func(t *testing.T) {
				body := map[string]any{"from": "sender@example.test", "to": []string{"valid@example.test"}}
				body[field] = []string{"valid@example.test", test.address}
				encoded, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				recorder := httptest.NewRecorder()
				composeRoutes(NewMessageHandler(nil, nil, nil, "", nil)).ServeHTTP(recorder,
					authzRequest(http.MethodPost, "/api/v1/messages/send", 1, nil, string(encoded)))
				want := parseShapeJSON(t, `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"`+field+`":"invalid recipient address in `+field+`"}}}}`)
				if recorder.Code != http.StatusUnprocessableEntity {
					t.Fatalf("got %d %s, want 422", recorder.Code, recorder.Body)
				}
				assertDocumentedBody(t, body, recorder, want)
			})
		}
	}
}

func TestContractFuzzShapesAreRefusedBeforeDatabaseAccess(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		operation, name, method, path, body string
		routes                              http.Handler
	}{
		{"bulkRetryQueue", "negative_id", "POST", "/api/v1/admin/queue/bulk-retry", `{"ids":[-1]}`, queueRoutes(NewQueueHandler(nil))},
		{"bulkBounceQueue", "negative_id", "POST", "/api/v1/admin/queue/bulk-bounce", `{"ids":[-1]}`, queueRoutes(NewQueueHandler(nil))},
		{"bulkDeleteQueue", "negative_id", "DELETE", "/api/v1/admin/queue/bulk-delete", `{"ids":[-1]}`, queueRoutes(NewQueueHandler(nil))},
		{"setDomainRelay", "control_username", "PUT", "/api/v1/admin/domains/1/relay", `{"transport":"direct","username":"\n"}`, policyRoutes(NewRelayHandler(nil, ""))},
		{"setDomainRelay", "control_password", "PUT", "/api/v1/admin/domains/1/relay", `{"transport":"direct","password":"\n"}`, policyRoutes(NewRelayHandler(nil, ""))},
		{"updateMessage", "folder_parent", "PATCH", "/api/v1/messages/1", `{"folder":".."}`, messageRoutes(NewMessageHandler(nil, nil, nil, "", nil))},
	} {
		t.Run(test.operation+"/"+test.name, func(t *testing.T) {
			pair, found := pairs[test.operation][test.name]
			if strings.HasPrefix(test.operation, "bulk") {
				recorder := httptest.NewRecorder()
				test.routes.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, strings.NewReader(test.body)))
				if recorder.Code != http.StatusUnprocessableEntity {
					t.Fatalf("status %d, want 422: %s", recorder.Code, recorder.Body)
				}
				want := parseShapeJSON(t, `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"ids[0]":"must be a non-negative integer"}}}}`)
				assertDocumentedBody(t, parseShapeJSON(t, test.body), recorder, want)
				return
			}
			if !found || !pair.Request.Invalid || pair.Response.Status != http.StatusUnprocessableEntity {
				t.Fatal("missing invalid request/422 pair")
			}
			examplepairs.Proven(t, test.operation, test.name)
			var requestBody, wantRequestBody, wantResponse interface{}
			for _, value := range []struct {
				data   []byte
				target *interface{}
			}{{pair.Request.Body, &requestBody}, {[]byte(test.body), &wantRequestBody}, {pair.Response.Body, &wantResponse}} {
				if err := json.Unmarshal(value.data, value.target); err != nil {
					t.Fatal(err)
				}
			}
			if !reflect.DeepEqual(requestBody, wantRequestBody) {
				t.Fatalf("request changed: %s", pair.Request.Body)
			}
			recorder := httptest.NewRecorder()
			test.routes.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, bytes.NewReader(pair.Request.Body)))
			if recorder.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status %d, want 422: %s", recorder.Code, recorder.Body)
			}
			assertDocumentedBody(t, requestBody, recorder, wantResponse)
		})
	}
}

func TestContractFuzzRelayNullRequiredFields(t *testing.T) {
	for _, test := range []struct{ body, field string }{
		{`{"transport":"relay","host":"smtp.example.test","port":null,"tls_mode":"starttls"}`, "port"},
		{`{"transport":"relay","host":"smtp.example.test","port":587,"tls_mode":null}`, "tls_mode"},
	} {
		t.Run(test.field, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			policyRoutes(NewRelayHandler(nil, "")).ServeHTTP(recorder,
				httptest.NewRequest(http.MethodPut, "/api/v1/admin/domains/1/relay", strings.NewReader(test.body)))
			if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), `"`+test.field+`":`) {
				t.Fatalf("got %d %s, want 422 identifying %s", recorder.Code, recorder.Body, test.field)
			}
		})
	}
}
