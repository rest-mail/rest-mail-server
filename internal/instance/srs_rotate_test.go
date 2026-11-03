package instance

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func secretsValue(t *testing.T, secrets []byte, name string) string {
	t.Helper()
	match := regexp.MustCompile(`(?m)^` + name + `=(.*)$`).FindSubmatch(secrets)
	if match == nil {
		return ""
	}
	return string(match[1])
}

// A new instance is scaffolded with an SRS secret of its own, and no previous one.
func TestScaffoldGeneratesAnSRSSecret(t *testing.T) {
	res, err := Scaffold("srs.test", t.TempDir(), "host")
	if err != nil {
		t.Fatal(err)
	}
	if got := secretsValue(t, res.Secrets, "RESTMAIL_SRS_SECRET"); !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(got) {
		t.Fatalf("RESTMAIL_SRS_SECRET = %q, want 64 hex characters", got)
	}
	if bytes.Contains(res.Secrets, []byte("RESTMAIL_SRS_PREVIOUS_SECRET")) {
		t.Fatal("a new instance has a previous secret")
	}
}

// A rotation moves the current secret to previous and generates a new one, dropping
// the old previous, and leaves every other line alone.
func TestRotateSRSSecret(t *testing.T) {
	secrets := []byte("# a comment\nRESTMAIL_DB_PASS=fake-db\nRESTMAIL_SRS_PREVIOUS_SECRET=fake-oldest\nRESTMAIL_SRS_SECRET=fake-current\nRESTMAIL_JWT_SECRET=fake-jwt\n")
	rotated, err := RotateSRSSecret(secrets)
	if err != nil {
		t.Fatal(err)
	}
	if got := secretsValue(t, rotated, "RESTMAIL_SRS_PREVIOUS_SECRET"); got != "fake-current" {
		t.Errorf("previous = %q, want the old current secret", got)
	}
	current := secretsValue(t, rotated, "RESTMAIL_SRS_SECRET")
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(current) {
		t.Errorf("current = %q, want 64 hex characters", current)
	}
	if bytes.Contains(rotated, []byte("fake-oldest")) {
		t.Error("the old previous secret was kept")
	}
	for _, line := range []string{"# a comment", "RESTMAIL_DB_PASS=fake-db", "RESTMAIL_JWT_SECRET=fake-jwt"} {
		if !bytes.Contains(rotated, []byte(line+"\n")) {
			t.Errorf("lost %q:\n%s", line, rotated)
		}
	}
	if n := strings.Count(string(rotated), "RESTMAIL_SRS_SECRET="); n != 1 {
		t.Errorf("%d current secrets, want 1", n)
	}
}

// An instance scaffolded before SRS gets its first secret the same way, with no
// previous one, since no address was signed before it.
func TestRotateSRSSecretFirstTime(t *testing.T) {
	rotated, err := RotateSRSSecret([]byte("RESTMAIL_JWT_SECRET=fake-jwt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := secretsValue(t, rotated, "RESTMAIL_SRS_SECRET"); got == "" {
		t.Fatalf("no current secret:\n%s", rotated)
	}
	if bytes.Contains(rotated, []byte("RESTMAIL_SRS_PREVIOUS_SECRET")) {
		t.Fatalf("a previous secret from nowhere:\n%s", rotated)
	}
	if !bytes.Contains(rotated, []byte("RESTMAIL_JWT_SECRET=fake-jwt\n")) {
		t.Fatalf("lost the other secret:\n%s", rotated)
	}
}
