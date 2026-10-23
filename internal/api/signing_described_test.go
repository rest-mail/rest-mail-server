package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSigningRoutesDescribeMiddlewareAndPublicResponses(t *testing.T) {
	doc := generatedDocument(t)
	wants := map[string]string{
		"listDKIMKeys": "domains:read", "getDKIMRotation": "domains:read", "setDKIMRotation": "domains:write",
		"setDKIMKey": "domains:write", "deleteDKIMKey": "domains:write", "listCertificates": "domains:read",
		"getCertificate": "domains:read", "createCertificate": "domains:write", "deleteCertificate": "domains:write",
	}
	seen := 0
	for _, op := range operationsOf(doc) {
		capability, ok := wants[op.id]
		if !ok {
			continue
		}
		seen++
		want := "[APIKeyAuth[" + capability + "] | BearerAuth[" + capability + "] | CookieAuth[" + capability + "]]"
		if got := securityOf(doc, op.operation); got != want {
			t.Errorf("%s security %s, want %s", op.id, got, want)
		}
		responses := op.operation["responses"].(map[string]interface{})
		for _, status := range []string{"401", "403", "413", "500", "503"} {
			if responses[status] == nil {
				t.Errorf("%s omits response %s", op.id, status)
			}
		}
		for status, response := range responses {
			if !strings.HasPrefix(status, "2") {
				continue
			}
			resolvedResponse := resolved(doc, response).(map[string]interface{})
			content, _ := resolvedResponse["content"].(map[string]interface{})
			for _, value := range content {
				schema := value.(map[string]interface{})["schema"]
				data, err := json.Marshal(normalSchema(doc, schema, map[string]bool{}))
				if err != nil {
					t.Fatal(err)
				}
				for _, secret := range []string{`"key_pem"`, `"private_key"`, `"dkim_private_key"`} {
					if strings.Contains(string(data), secret) {
						t.Errorf("%s exposes %s in its %s response", op.id, secret, status)
					}
				}
			}
		}
	}
	if seen != len(wants) {
		t.Fatalf("described %d signing operations, want %d", seen, len(wants))
	}
}
