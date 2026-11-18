package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/config"
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

// Finding f697ff90d28a3f37 on the contract-fuzz board (#2051): {"services":[""]} was
// schema-valid and refused. The description's pattern is now the handler's own rule.
func TestContractFuzzProbeServicePatternIsTheHandlersRule(t *testing.T) {
	pattern := regexp.MustCompile(ProbeServicePattern)
	values := []string{"", " ", "ftp", "db ", " db", "db\n", "smtp_", "smtp_submission2", "pop", "pop33"}
	for name := range probeableServices {
		values = append(values, name, strings.ToUpper(name), strings.ToUpper(name[:1])+name[1:])
	}
	for _, value := range values {
		if got, want := pattern.MatchString(value), probeableServices[strings.ToLower(value)]; got != want {
			t.Errorf("%q: description accepts %t, handler accepts %t", value, got, want)
		}
	}
	recorder := httptest.NewRecorder()
	testRoutes(NewTestHandler(nil, &config.Config{Environment: "test"})).ServeHTTP(recorder,
		httptest.NewRequest(http.MethodPost, "/api/v1/admin/test/probe", strings.NewReader(`{"services":[""]}`)))
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), `"services"`) {
		t.Fatalf("got %d %s, want 422 naming services", recorder.Code, recorder.Body)
	}
}

// Finding 6389af425b8edcb4 (#2051): the same schema-valid body is refused or accepted by
// whether a password is stored, which is what the username's description says.
func TestContractFuzzRelayCredentialsCountTheStoredPassword(t *testing.T) {
	srv, _, id := relayTestServer(t, relayTestMasterKey)
	const relay = `"transport":"relay","host":"a","port":1,"tls_mode":"starttls"`
	for _, step := range []struct {
		name, body string
		status     int
	}{
		{"username with nothing stored", `{` + relay + `,"username":"a"}`, http.StatusUnprocessableEntity},
		{"both sent", `{` + relay + `,"username":"a","password":"p"}`, http.StatusOK},
		{"username with a stored password", `{` + relay + `,"username":"a"}`, http.StatusOK},
		{"no username with a stored password", `{` + relay + `}`, http.StatusUnprocessableEntity},
		{"no username, password cleared", `{` + relay + `,"password":""}`, http.StatusOK},
	} {
		code, body := relayCall(t, srv, http.MethodPut, id, step.body)
		if code != step.status {
			t.Fatalf("%s: got %d %s, want %d", step.name, code, body, step.status)
		}
		if code == http.StatusUnprocessableEntity && !strings.Contains(body, `"password"`) {
			t.Fatalf("%s: refusal does not name password: %s", step.name, body)
		}
	}
}

// Finding 5972296353a77f68 (#2051): a recipient that names no mailbox is the documented 422
// undeliverable, and the fuzz body's empty one is such a recipient.
func TestContractFuzzRESTMAILEmptyRecipientIsUndeliverable(t *testing.T) {
	h := NewRestmailHandler(openPlusAddrTestDB(t), nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{})
	body, err := json.Marshal(map[string]any{
		"date":        "Sat, 17 Oct 2026 12:00:00 +0000",
		"raw_message": []byte("Message-ID: <contract-probe@example.test>\r\nFrom: Original <old@example.test>\r\nSubject: imported\r\n\r\nhistory\r\n"),
		"to":          []string{""},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/restmail/messages", bytes.NewReader(body))
	request.RemoteAddr = "203.0.113.9:5000"
	recorder := httptest.NewRecorder()
	restmailRoutes(h).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), `"undeliverable"`) {
		t.Fatalf("got %d %s, want 422 undeliverable", recorder.Code, recorder.Body)
	}
}
