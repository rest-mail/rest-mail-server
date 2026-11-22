package api

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

type conventionDecision struct {
	When, Answer, Source string
	Checks               []string
}

type conventionMatrix struct {
	Decided   []conventionDecision
	Undecided []string
}

var conventionChecks = map[string]func(*testing.T){
	"TestRequirementExamplesThroughTheProductionRouter":          TestRequirementExamplesThroughTheProductionRouter,
	"TestWireConventionsRefuseByParseabilityAndVisibility":       TestWireConventionsRefuseByParseabilityAndVisibility,
	"TestAccountRefusals_ANoLinkCallerCannotTellAnAccountExists": TestAccountRefusals_ANoLinkCallerCannotTellAnAccountExists,
	"TestAdminRoutes_MailboxTokenDenied":                         TestAdminRoutes_MailboxTokenDenied,
	"TestMailStateRequirementExamples":                           TestMailStateRequirementExamples,
	"TestNoSuccessListIsNullable":                                TestNoSuccessListIsNullable,
	"TestEveryCreateLeadsToItsLifecycle":                         TestEveryCreateLeadsToItsLifecycle,
	"TestCreatesPersistAndEndAt404":                              TestCreatesPersistAndEndAt404,
}

func readConventionMatrix(t *testing.T) (conventionMatrix, string) {
	t.Helper()
	data, err := os.ReadFile("../../tests/contract/convention-decisions.json")
	if err != nil {
		t.Fatal(err)
	}
	var matrix conventionMatrix
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&matrix); err != nil {
		t.Fatal(err)
	}
	decisions, err := os.ReadFile("../../docs/api-conventions.md")
	if err != nil {
		t.Fatal(err)
	}
	return matrix, string(decisions)
}

func conventionMatrixProblems(matrix conventionMatrix, document string) []string {
	var problems []string
	seen := map[string]bool{}
	for _, decision := range matrix.Decided {
		row := "| " + decision.When + " | " + decision.Answer + " | " + decision.Source + " |"
		if seen[decision.When] || !strings.Contains(document, row) {
			problems = append(problems, "duplicate or unrecorded owner decision: "+decision.When)
		}
		seen[decision.When] = true
		if len(decision.Checks) == 0 {
			problems = append(problems, "decision has no executable check: "+decision.When)
		}
		for _, check := range decision.Checks {
			if conventionChecks[check] == nil {
				problems = append(problems, "unknown convention check: "+check)
			}
		}
	}
	section := strings.Split(strings.Split(document, "## Decided\n")[1], "### Bad input")[0]
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(line, "| ") || strings.HasPrefix(line, "| When |") {
			continue
		}
		when := strings.TrimSpace(strings.Split(line, "|")[1])
		if !seen[when] {
			problems = append(problems, "decided convention missing from matrix: "+when)
		}
	}
	if len(matrix.Undecided) == 0 {
		problems = append(problems, "matrix lost the unresolved owner decisions")
	}
	return problems
}

func TestConventionDecisionMatrix(t *testing.T) {
	matrix, document := readConventionMatrix(t)
	if problems := conventionMatrixProblems(matrix, document); len(problems) != 0 {
		t.Fatal(strings.Join(problems, "\n"))
	}
	exceptions := readConventionExceptions(t)
	for site, reason := range exceptions.NullableSuccessLists {
		if !goodReason(reason) {
			t.Errorf("nullable list exception %s lacks a reason", site)
		}
	}
	for operation, resource := range exceptions.CreateResource {
		if !goodReason(resource.Reason) {
			t.Errorf("resource exception %s lacks a reason", operation)
		}
	}
	for operation, lifecycle := range exceptions.CreateLifecycle {
		for verb, reason := range lifecycle {
			if !goodReason(reason) {
				t.Errorf("lifecycle exception %s/%s lacks a reason", operation, verb)
			}
		}
	}
	referenced := map[string]bool{}
	for _, decision := range matrix.Decided {
		for _, check := range decision.Checks {
			referenced[check] = true
		}
	}
	t.Logf("%d owner decisions reference %d independently discovered guards; %d unresolved topics excluded", len(matrix.Decided), len(referenced), len(matrix.Undecided))
}

func TestConventionDecisionMatrixControls(t *testing.T) {
	matrix, document := readConventionMatrix(t)
	if problems := conventionMatrixProblems(matrix, document); len(problems) != 0 {
		t.Fatal(problems)
	}
	for _, control := range []string{"missing_decision", "unapproved_answer", "missing_check", "unknown_check"} {
		t.Run(control, func(t *testing.T) {
			changed := matrix
			changed.Decided = append([]conventionDecision(nil), matrix.Decided...)
			switch control {
			case "missing_decision":
				changed.Decided = changed.Decided[1:]
			case "unapproved_answer":
				changed.Decided[0].Answer = "422 for malformed JSON"
			case "missing_check":
				changed.Decided[0].Checks = nil
			case "unknown_check":
				changed.Decided[0].Checks = []string{"TestNonexistentConvention"}
			}
			if problems := conventionMatrixProblems(changed, document); len(problems) == 0 {
				t.Fatal("matrix control passed")
			} else {
				t.Logf("control rejected: %s", fmt.Sprint(problems))
			}
		})
	}
}
