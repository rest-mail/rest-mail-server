package handlers

// Issue #201: the `duplicate` filter forks only to a webhook; it has no
// queue_recipient. A pipeline asking for one is rejected at admin-pipeline SAVE
// time (fail-fast, HTTP 400) rather than stored and silently ignored. A
// webhook-only duplicate filter stays valid.

import (
	"encoding/json"
	"strings"
	"testing"
)

const duplicateQueueFilters = `[{"name":"duplicate","type":"action","enabled":true,` +
	`"config":{"queue_recipient":"copy@example.com"}}]`

const duplicateWebhookFilters = `[{"name":"duplicate","type":"action","enabled":true,` +
	`"config":{"webhook_url":"https://hooks.example.com/mail"}}]`

func TestValidateDuplicatePipelineFilters_QueueRecipientRejected(t *testing.T) {
	err := validateDuplicatePipelineFilters(json.RawMessage(duplicateQueueFilters))
	if err == nil {
		t.Fatal("expected a duplicate queue_recipient config to be rejected, got nil")
	}
	if !strings.Contains(err.Error(), "queue_recipient") {
		t.Errorf("error should name queue_recipient: %q", err.Error())
	}
}

func TestValidateDuplicatePipelineFilters_WebhookAccepted(t *testing.T) {
	if err := validateDuplicatePipelineFilters(json.RawMessage(duplicateWebhookFilters)); err != nil {
		t.Errorf("webhook-only duplicate filter must stay valid, got %v", err)
	}
}

func TestValidateDuplicatePipelineFilters_NothingToValidateAccepted(t *testing.T) {
	cases := map[string]string{
		"nil":            "",
		"empty array":    `[]`,
		"no duplicate":   `[{"name":"header_validate","type":"action","enabled":true,"config":{}}]`,
		"malformed":      `{"not":"an array"}`,
		"disabled queue": `[{"name":"duplicate","type":"action","enabled":false,"config":{"queue_recipient":"copy@example.com"}}]`,
	}
	// A disabled block with queue_recipient is still rejected (it would no-op the
	// moment it is enabled) — assert that one separately below.
	delete(cases, "disabled queue")
	for name, raw := range cases {
		if err := validateDuplicatePipelineFilters(json.RawMessage(raw)); err != nil {
			t.Errorf("%s: expected acceptance, got %v", name, err)
		}
	}

	disabled := `[{"name":"duplicate","type":"action","enabled":false,"config":{"queue_recipient":"copy@example.com"}}]`
	if err := validateDuplicatePipelineFilters(json.RawMessage(disabled)); err == nil {
		t.Error("a disabled duplicate block with queue_recipient should still be rejected")
	}
}
