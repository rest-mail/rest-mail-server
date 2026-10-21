package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/restmail/restmail/internal/auth"
)

// The two refused bodies reach ProtocolSignIn with a valid Basic principal,
// just as the contract hook supplies one, and are refused before account lookup.
func TestProtocolSignIn_DocumentedRefusals(t *testing.T) {
	requests, responses := postExamples(t, "/api/v1/auth/protocol-sign-in", "protocolSignIn")
	wantPairs := map[string][]string{
		"request": {"invalid_body", "protocol_missing", "signed_in"},
		"200":     {"signed_in"},
		"400":     {"invalid_body"},
		"422":     {"protocol_missing"},
	}
	gotPairs := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		gotPairs[status] = names(examples)
	}
	if !reflect.DeepEqual(gotPairs, wantPairs) {
		t.Fatalf("protocolSignIn's named examples are %v, want %v", gotPairs, wantPairs)
	}
	if string(mustJSON(requests["invalid_body"])) != "[]" || string(mustJSON(requests["protocol_missing"])) != "{}" {
		t.Fatalf("refused requests are %s and %s, want [] and {}", mustJSON(requests["invalid_body"]), mustJSON(requests["protocol_missing"]))
	}

	for status, examples := range responses {
		if status != "400" && status != "422" {
			continue
		}
		for name, want := range examples {
			t.Run(name, func(t *testing.T) {
				body := mustJSON(requests[name])
				req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/protocol-sign-in", bytes.NewReader(body))
				req = withClaims(req, &auth.Claims{AccountID: 1, CredentialID: 1})
				rr := httptest.NewRecorder()
				NewCredentialHandler(nil).ProtocolSignIn(rr, req)
				if rr.Code != map[string]int{"400": http.StatusBadRequest, "422": http.StatusUnprocessableEntity}[status] {
					t.Fatalf("%s answered %d; body %s", body, rr.Code, rr.Body)
				}
				var got interface{}
				if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("%s answered %s, spec documents %s", body, rr.Body, mustJSON(want))
				}
			})
		}
	}
}
