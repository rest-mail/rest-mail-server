package bootstrap

import (
	"errors"
	"os"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v2"
)

// TestChosenPasswordProblem is the one rule every way of choosing a password
// asks (#1672): twelve characters are not enough on their own. A password
// zxcvbn can guess, or one built from the account's own address, is refused
// with a reason that never repeats it.
func TestChosenPasswordProblem(t *testing.T) {
	const address = "alice.smith@example.com"
	for _, c := range []struct {
		password string
		want     error
	}{
		{"short", ErrPasswordTooShort},
		// Twelve characters and more, and all common or patterned.
		{"Summer2026!!", ErrPasswordGuessable},
		{"password123456", ErrPasswordGuessable},
		{"qwertyuiop123", ErrPasswordGuessable},
		{"123456789012", ErrPasswordGuessable},
		{"aaaaaaaaaaaa", ErrPasswordGuessable},
		{"letmein-letmein", ErrPasswordGuessable},
		// Strong apart from the account's own address in it.
		{"Tr1cky-Orbit-Lantern-alice.smith@example.com", ErrPasswordGuessable},
		{"Tr1cky-Orbit-Lantern-ALICE.SMITH", ErrPasswordGuessable},
		{"Tr1cky-Orbit-Lantern-smith", ErrPasswordGuessable},
		{"Tr1cky-Orbit-Lantern-example.com", ErrPasswordGuessable},
		{"Tr1cky-Orbit-Lantern-Example", ErrPasswordGuessable},
		// Strong.
		{"Tr1cky-Orbit-Lantern-42", nil},
		{"correct horse battery staple", nil},
		{"zebra-copper-violin-17", nil},
		{"Qv7#mZ2!pL9k", nil},
		{"秘密の鍵は誰にも教えないでね", nil},
	} {
		got := CheckChosenPassword(c.password, address)
		if !errors.Is(got, c.want) && got != c.want {
			t.Errorf("CheckChosenPassword(%q) = %v, want %v", c.password, got, c.want)
		}
		problem := ChosenPasswordProblem(c.password, address)
		if (problem == "") != (c.want == nil) {
			t.Errorf("ChosenPasswordProblem(%q) = %q, want a problem: %v", c.password, problem, c.want != nil)
		}
		if strings.Contains(problem, c.password) || (got != nil && strings.Contains(got.Error(), c.password)) {
			t.Errorf("the refusal of %q repeats it: %q", c.password, problem)
		}
		if c.want == ErrPasswordGuessable && !strings.Contains(problem, "too easy to guess") {
			t.Errorf("ChosenPasswordProblem(%q) = %q, want it to say the password is too easy to guess", c.password, problem)
		}
	}
}

// A local part or domain label under three characters is only scored, not
// refused outright: "jo" would otherwise refuse every password with "jo" in it.
func TestChosenPasswordProblemShortOwnNames(t *testing.T) {
	if err := CheckChosenPassword("Tr1cky-Orbit-Lantern-jo-42", "jo@ab.io"); err != nil {
		t.Errorf("a strong password with a two-letter local part in it: %v", err)
	}
	if err := CheckChosenPassword("Tr1cky-Orbit-Lantern-42", ""); err != nil {
		t.Errorf("no address: %v", err)
	}
}

// An enormous password is scored by its first characters, so a long one is
// checked quickly and still accepted.
func TestChosenPasswordProblemLongPassword(t *testing.T) {
	long := "Tr1cky-Orbit-Lantern-42 " + strings.Repeat("unrelated words go on ", 200)
	if err := CheckChosenPassword(long, "alice@example.com"); err != nil {
		t.Errorf("a long strong password: %v", err)
	}
}

// TestContractValidPinsPassTheRule holds the password vertrag.yml gives the
// contract run's valid probes to this rule, for the addresses those probes use. A
// value the rule refuses would fail every one of them with a 422.
func TestContractValidPinsPassTheRule(t *testing.T) {
	raw, err := os.ReadFile("../../vertrag.yml")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Fuzz struct {
			PinValid map[string]map[string]interface{} `yaml:"pin-valid"`
		} `yaml:"fuzz"`
	}
	if err := yaml.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"createAccount", "updateMailbox"} {
		password, ok := config.Fuzz.PinValid[operation]["password"].(string)
		if !ok {
			t.Fatalf("vertrag.yml holds no password for %s", operation)
		}
		for _, address := range []string{"someone@example.com", "new-account@restmail.test"} {
			if problem := ChosenPasswordProblem(password, address); problem != "" {
				t.Errorf("%s for %s: the password %s", operation, address, problem)
			}
		}
	}
}
