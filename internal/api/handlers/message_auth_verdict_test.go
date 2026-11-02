package handlers

import (
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

func TestMessageOfAnswersTheStoredVerdicts(t *testing.T) {
	results := []pipeline.AuthResult{
		{Method: pipeline.AuthDKIM, Result: "pass", Property: pipeline.PropHeaderD, Domain: "example.com"},
		{Method: pipeline.AuthDMARC, Result: "fail", Property: pipeline.PropHeaderFrom, Domain: "example.org"},
	}
	got := messageOf(models.Message{RawMessage: pipeline.AuthResultsHeader(results) + "From: a@example.org\r\n\r\nbody\r\n"})
	if got.AuthVerdict != pipeline.AuthVerdictMismatch {
		t.Fatalf("verdict = %q, want %q", got.AuthVerdict, pipeline.AuthVerdictMismatch)
	}
	if len(got.AuthResults) != 2 || got.AuthResults[0].Domain != "example.com" || got.AuthResults[1].Property != pipeline.PropHeaderFrom {
		t.Fatalf("results = %+v", got.AuthResults)
	}
}

func TestMessageOfOmitsVerdictsNoDeliveryReached(t *testing.T) {
	got := messageOf(models.Message{RawMessage: "From: a@example.org\r\nAuthentication-Results: restmail; dkim=pass\r\n\r\nbody\r\n"})
	if got.AuthVerdict != "" || got.AuthResults != nil {
		t.Fatalf("got verdict %q and results %+v", got.AuthVerdict, got.AuthResults)
	}
}
