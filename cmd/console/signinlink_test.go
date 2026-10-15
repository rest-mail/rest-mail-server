package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"strings"
	"testing"
)

const signInPassword = "kX9-mQ2_rT7vL4wN8pZ1aB3cD5eF7gH9"

// signIn runs `restmail sign-in-link` on a terminal columns wide (0: not a
// terminal at all), with password on stdin.
func signIn(t *testing.T, columns int, password string, args ...string) (int, string, string) {
	t.Helper()
	saved := terminalColumns
	terminalColumns = func(io.Writer) int { return columns }
	t.Cleanup(func() { terminalColumns = saved })
	var out, errOut bytes.Buffer
	status := runSignInLink(append([]string{"--server", "https://mail.example.com"}, args...),
		strings.NewReader(password), &out, &errOut)
	return status, out.String(), errOut.String()
}

// drewQR says whether out holds a QR code's block characters.
func drewQR(out string) bool { return strings.ContainsAny(out, "▀▄█") }

// lineValue reads "  Name:  value" out of the text.
func lineValue(t *testing.T, out, name string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), name+":"); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatalf("no %s line in:\n%s", name, out)
	return ""
}

// The text is there whether or not a QR code is: nothing can tell whether a
// drawn one can be scanned.
func TestSignInLink_TheTextIsAlwaysThere(t *testing.T) {
	for name, columns := range map[string]int{"a wide terminal": 120, "a narrow one": 30, "no terminal": 0} {
		t.Run(name, func(t *testing.T) {
			status, out, errOut := signIn(t, columns, signInPassword)
			if status != exitOK {
				t.Fatalf("status %d: %s", status, errOut)
			}
			if got := lineValue(t, out, "Password"); got != signInPassword {
				t.Errorf("Password: %q", got)
			}
			if got := lineValue(t, out, "Username"); got != "admin" {
				t.Errorf("Username: %q", got)
			}
			if got := lineValue(t, out, "Server"); got != "https://mail.example.com" {
				t.Errorf("Server: %q", got)
			}
			link, err := url.Parse(lineValue(t, out, "Link"))
			if err != nil {
				t.Fatal(err)
			}
			carried, _ := url.ParseQuery(link.EscapedFragment())
			if link.Host != "rest-mail.com" || link.Path != "/setup" || carried.Get("password") != signInPassword ||
				carried.Get("server") != "https://mail.example.com" || carried.Get("username") != "admin" {
				t.Errorf("Link: %v", link)
			}
		})
	}
}

// A QR code where it fits, and none where it would wrap into noise or land in
// a file.
func TestSignInLink_TheQRCodeOnlyWhereItFits(t *testing.T) {
	if _, out, _ := signIn(t, 120, signInPassword); !drewQR(out) {
		t.Errorf("a wide terminal got no QR code:\n%s", out)
	}
	if _, out, _ := signIn(t, 30, signInPassword); drewQR(out) {
		t.Errorf("a 30-column terminal got a QR code:\n%s", out)
	}
	if _, out, _ := signIn(t, 0, signInPassword); drewQR(out) {
		t.Errorf("a pipe got a QR code:\n%s", out)
	}
	if _, out, _ := signIn(t, 120, signInPassword, "--no-qr"); drewQR(out) {
		t.Errorf("--no-qr drew one anyway:\n%s", out)
	}
}

// What `printf '%s'` sends, and what `echo` sends.
func TestSignInLink_ThePasswordComesOnStdin(t *testing.T) {
	_, out, _ := signIn(t, 0, signInPassword+"\n", "--username", "owner")
	if got := lineValue(t, out, "Password"); got != signInPassword {
		t.Errorf("a trailing newline was kept: %q", got)
	}
	if got := lineValue(t, out, "Username"); got != "owner" {
		t.Errorf("Username: %q", got)
	}
}

func TestSignInLink_JSONKeepsTheSecretUnlessAsked(t *testing.T) {
	_, out, _ := signIn(t, 0, signInPassword, "--json")
	if strings.Contains(out, signInPassword) || !strings.Contains(out, `"link_redacted":true`) {
		t.Errorf("--json without --show-secret printed %s", out)
	}
	_, out, _ = signIn(t, 0, signInPassword, "--json", "--show-secret")
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil || got["password"] != signInPassword ||
		!strings.HasPrefix(got["link"].(string), "https://rest-mail.com/setup#server=") {
		t.Errorf("--json --show-secret printed %s", out)
	}
}

func TestSignInLink_ExitStatuses(t *testing.T) {
	cases := []struct {
		name     string
		password string
		args     []string
		code     string
	}{
		{"no password on stdin", "", nil, "no_password"},
		{"never on the command line", signInPassword, []string{"--password", signInPassword}, "bad_flag"},
		{"no username", signInPassword, []string{"--username", " "}, "bad_flag"},
		{"plain http to another host", signInPassword, []string{"--server", "http://mail.example.com"}, "bad_flag"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, out, errOut := signIn(t, 0, c.password, append(c.args, "--json")...)
			if status != exitUsage || out != "" {
				t.Errorf("status %d, stdout %q; want %d and nothing", status, out, exitUsage)
			}
			var e struct{ Error struct{ Code string } }
			if json.Unmarshal([]byte(errOut), &e) != nil || e.Error.Code != c.code {
				t.Errorf("stderr = %q, want code %s", errOut, c.code)
			}
		})
	}
}
