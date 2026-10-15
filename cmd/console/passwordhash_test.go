package main

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func passwordHash(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	status := runPasswordHash(args, strings.NewReader(stdin), &out, &errOut)
	return status, out.String(), errOut.String()
}

// What it prints is a hash the password checks against, at the server's cost.
func TestPasswordHash_ThePasswordChecksAgainstIt(t *testing.T) {
	status, out, errOut := passwordHash(t, signInPassword+"\n")
	if status != exitOK {
		t.Fatalf("status %d: %s", status, errOut)
	}
	hash := strings.TrimSpace(out)
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(signInPassword)) != nil {
		t.Errorf("%q does not check against the password", hash)
	}
	if cost, _ := bcrypt.Cost([]byte(hash)); cost != passwordHashCost {
		t.Errorf("cost %d, want %d", cost, passwordHashCost)
	}
	if strings.Contains(out, signInPassword) {
		t.Error("the password itself was printed")
	}
}

func TestPasswordHash_Refuses(t *testing.T) {
	for name, c := range map[string]struct {
		stdin string
		args  []string
	}{
		"nothing on stdin":           {"", nil},
		"shorter than the server's":  {"fifteen-chars!!", nil},
		"a password as an argument":  {"", []string{signInPassword}},
		"a flag that does not exist": {signInPassword, []string{"--password", signInPassword}},
	} {
		if status, out, _ := passwordHash(t, c.stdin, c.args...); status != exitUsage || out != "" {
			t.Errorf("%s: status %d, stdout %q; want %d and nothing", name, status, out, exitUsage)
		}
	}
}
