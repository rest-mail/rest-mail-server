package api

import (
	"slices"
	"testing"
)

func TestOwnerDecidedProbeSchemas(t *testing.T) {
	document := generatedDocument(t)
	bodies := map[string]map[string]interface{}{}
	for _, operation := range operationsOf(document) {
		if !slices.Contains([]string{"linkAccount", "acceptInvitation", "createAppPassword", "receiveTlsReport"}, operation.id) {
			continue
		}
		body := operation.operation["requestBody"].(map[string]interface{})
		media := "application/json"
		if operation.id == "receiveTlsReport" {
			media = "application/tlsrpt+json"
		}
		content := body["content"].(map[string]interface{})[media].(map[string]interface{})
		bodies[operation.id] = resolved(document, content["schema"]).(map[string]interface{})
	}
	for _, operation := range []string{"linkAccount", "acceptInvitation", "createAppPassword"} {
		for _, field := range map[string][]string{
			"linkAccount": {"address", "password"}, "acceptInvitation": {"token", "password"}, "createAppPassword": {"name", "password"},
		}[operation] {
			if !slices.Contains(bodies[operation]["required"].([]interface{}), interface{}(field)) {
				t.Errorf("%s does not require %s", operation, field)
			}
		}
	}
	property := func(operation, field string) map[string]interface{} {
		return bodies[operation]["properties"].(map[string]interface{})[field].(map[string]interface{})
	}
	if property("linkAccount", "address")["format"] != "email" {
		t.Error("linkAccount does not state the email shape")
	}
	if property("acceptInvitation", "password")["minLength"] != float64(12) || property("acceptInvitation", "password")["maxLength"] != float64(72) {
		t.Error("acceptInvitation does not bound chosen passwords")
	}
	if property("acceptInvitation", "token")["minLength"] != float64(1) || property("createAppPassword", "password")["minLength"] != float64(1) {
		t.Error("required proofs can still be empty")
	}
	policies := property("receiveTlsReport", "policies")
	policyEntry := resolved(document, policies["items"]).(map[string]interface{})
	if required, _ := policyEntry["required"].([]interface{}); slices.Contains(required, interface{}("policy")) {
		t.Error("external reports now require policy")
	}
	policy := resolved(document, policyEntry["properties"].(map[string]interface{})["policy"]).(map[string]interface{})
	if required, _ := policy["required"].([]interface{}); slices.Contains(required, interface{}("policy-domain")) {
		t.Error("external reports now require policy-domain")
	}
}
