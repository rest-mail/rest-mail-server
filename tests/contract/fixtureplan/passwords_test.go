package fixtureplan

import (
	"os"
	"regexp"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/bootstrap"
)

func TestOwnDataPasswordsMeetAccountPolicy(t *testing.T) {
	source, err := os.ReadFile("../hooks.py")
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		name    string
		address string
	}{
		{"SIGN_IN_PASSWORD", "contract-sign-in-1794243600000000000@restmail.test"},
		{"RECOVERABLE_PASSWORD", "recoverable-1794243600000000000@restmail.test"},
		{"RECOVERED_PASSWORD", "recoverable-1794243600000000000@restmail.test"},
		{"OWNER_RESET_PASSWORD", "owner-reset-1794243600000000000@restmail.test"},
		{"LINKED_PASSWORD", "linked-1794243600000000000@restmail.test"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			pattern := regexp.MustCompile(`(?m)^` + fixture.name + ` = "([^"]+)"$`)
			match := pattern.FindSubmatch(source)
			if len(match) != 2 {
				t.Fatalf("missing fixture password %s", fixture.name)
			}
			if problem := bootstrap.ChosenPasswordProblem(string(match[1]), fixture.address); problem != "" {
				t.Fatalf("fixture account creation is refused: %s", problem)
			}
		})
	}
}
