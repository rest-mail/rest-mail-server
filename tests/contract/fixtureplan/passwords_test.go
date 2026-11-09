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
	for _, name := range []string{"SIGN_IN_PASSWORD", "RECOVERABLE_PASSWORD", "RECOVERED_PASSWORD"} {
		t.Run(name, func(t *testing.T) {
			pattern := regexp.MustCompile(`(?m)^` + name + ` = "([^"]+)"$`)
			match := pattern.FindSubmatch(source)
			if len(match) != 2 {
				t.Fatalf("missing fixture password %s", name)
			}
			if problem := bootstrap.ChosenPasswordProblem(string(match[1]), "contract-sign-in-1794243600000000000@restmail.test"); problem != "" {
				t.Fatalf("fixture account creation is refused: %s", problem)
			}
		})
	}
}
