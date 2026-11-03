package api

import (
	"encoding/json"
	"errors"
	"testing"
	"testing/fstest"

	oapifly "github.com/antimatter-studios/go-oapifly"
	restmail "github.com/rest-mail/rest-mail-server"
)

// ACME's routes exist only when ACME is configured, so the default description never shows
// them, and an area file that left them out still passed TestStrictAPIAreas (#1747). Take
// one operation's example pairs away in the configuration that registers it: strict mode,
// not the inventory report, has to refuse that. The refusal comes from area 631 naming the
// operation, so this fails when either ID is removed.
func TestStrictModeProtectsTheACMEChallengeOperations(t *testing.T) {
	raw, err := restmail.Examples.ReadFile("openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"acmeChallenge", "headAcmeChallenge"} {
		t.Run(operation, func(t *testing.T) {
			var pairs map[string]json.RawMessage
			if err := json.Unmarshal(raw, &pairs); err != nil {
				t.Fatal(err)
			}
			if _, ok := pairs[operation]; !ok {
				t.Fatalf("openapi-example.json has no pairs for %s", operation)
			}
			delete(pairs, operation)
			without, err := json.Marshal(pairs)
			if err != nil {
				t.Fatal(err)
			}
			examples := fstest.MapFS{"openapi-example.json": {Data: without}}
			for _, routers := range gapRouters(t) {
				var strict *oapifly.StrictError
				if err := describeWith(routers.Registry, "acme strict", examples).Check(); !errors.As(err, &strict) {
					t.Fatalf("strict mode passed without %s's pairs: %v", operation, err)
				} else if !warnsAbout(strict.Warnings, operation) {
					t.Errorf("strict mode says nothing about %s: %v", operation, strict.Warnings)
				}
			}
		})
	}
}

func warnsAbout(warnings []oapifly.Warning, operation string) bool {
	for _, w := range warnings {
		if w.OperationID == operation {
			return true
		}
	}
	return false
}
