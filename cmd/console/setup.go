package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/mdp/qrterminal/v4"
)

// `restmail setup` and `restmail reset-admin` reach into the server's own
// container (kubectl exec, or docker exec) and run its operator command there,
// so they work only for someone who controls the deployment: that is the proof
// of ownership. What comes back is a single-use link for the app, shown as a QR
// code to scan.
//
// Written for agents as much as people. Every input is a flag, nothing prompts,
// --json gives one object on stdout and errors as one object on stderr, and the
// exit status says what kind of failure it was.

// Exit statuses of the setup commands, shared with the operator command they run.
const (
	exitOK             = 0
	exitFailed         = 1 // anything unexpected
	exitUsage          = 2 // a flag that does not exist, or is missing
	exitAlreadyClaimed = 3 // setup asked for, but the server has an admin
	exitNoSuchAdmin    = 4 // reset-admin named an account that does not exist
	exitUnreachable    = 5 // kubectl or docker missing, or the server not found
)

// runner runs a program and returns what it printed and its exit status; err
// is set only when it could not be run at all. Replaced in tests.
type runner func(ctx context.Context, name string, args ...string) (stdout, stderr []byte, status int, err error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, []byte, int, error) {
	var out, errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return out.Bytes(), errOut.Bytes(), exitErr.ExitCode(), nil
	}
	if err != nil {
		return nil, nil, -1, err
	}
	return out.Bytes(), errOut.Bytes(), 0, nil
}

// target says how to reach the server's container.
type target struct {
	docker     string // container name; empty means Kubernetes
	namespace  string
	context    string
	kubeconfig string
}

func (t target) kubectl(args ...string) []string {
	var base []string
	if t.kubeconfig != "" {
		base = append(base, "--kubeconfig", t.kubeconfig)
	}
	if t.context != "" {
		base = append(base, "--context", t.context)
	}
	return append(append(base, "-n", t.namespace), args...)
}

const setupUsage = `restmail setup — claim a freshly installed rest-mail server

  restmail setup [--namespace rest-mail] [--context NAME] [--kubeconfig PATH]
  restmail setup --docker CONTAINER --server https://mail.example.com

Mints a single-use link (valid 15 minutes) and shows it as a QR code: scan it
with the rest-mail app to become the server's first admin. Needs the access
you installed with: kubectl exec on the API's pod, or docker exec on its
container.

  --json         one JSON object on stdout; errors as one on stderr
  --show-secret  include the link itself in --json output (it grants admin)
  --server URL   the server's public address (default: from the ingress)
  --lifetime D   how long the link works, e.g. 30m (default 15m, at most 24h)

Exit status: 0 ok, 2 usage, 3 already claimed (use reset-admin), 5 server
not reachable, 1 anything else.
`

const resetUsage = `restmail reset-admin — let an admin back in when every other way is lost

  restmail reset-admin --username NAME [--namespace rest-mail] [--context NAME]
  restmail reset-admin --username NAME --docker CONTAINER --server URL

Mints a single-use link for that admin. Whoever redeems it chooses a new
password and enrolls a new authenticator, and every session the account had
ends: anyone who held the old credentials is locked out. This is also how a
server set up by someone else, or by an agent, is handed over.

Flags and exit statuses as for restmail setup; 4 means no such admin.
`

// runSetupCommand runs `restmail setup` or `restmail reset-admin`.
func runSetupCommand(ctx context.Context, command string, args []string, run runner, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var t target
	fs.StringVar(&t.namespace, "namespace", "rest-mail", "Kubernetes namespace")
	fs.StringVar(&t.context, "context", "", "kubectl context")
	fs.StringVar(&t.kubeconfig, "kubeconfig", "", "kubeconfig file")
	fs.StringVar(&t.docker, "docker", "", "reach the server with docker exec on this container instead")
	server := fs.String("server", "", "the server's public address")
	username := fs.String("username", "", "the admin to reset (reset-admin)")
	lifetime := fs.Duration("lifetime", 15*time.Minute, "how long the link works")
	asJSON := fs.Bool("json", false, "JSON output")
	showSecret := fs.Bool("show-secret", false, "include the link in --json output")
	help := fs.Bool("help", false, "show help")

	usage := setupUsage
	if command == "reset-admin" {
		usage = resetUsage
	}
	o := output{json: *asJSON, stdout: stdout, stderr: stderr}
	if err := fs.Parse(args); err != nil {
		o.json = containsFlag(args, "json")
		return o.fail(exitUsage, "bad_flag", err.Error())
	}
	o.json = *asJSON
	if *help {
		_, _ = io.WriteString(stdout, usage)
		return exitOK
	}
	if command == "reset-admin" && *username == "" {
		return o.fail(exitUsage, "bad_flag", "--username is required")
	}
	if t.docker != "" && *server == "" {
		return o.fail(exitUsage, "bad_flag", "--server is required with --docker: there is no ingress to read it from")
	}

	// Where the server is, for the link. From its ingress unless told.
	serverURL := *server
	if serverURL == "" {
		host, code, err := ingressHost(ctx, run, t)
		if err != nil {
			return o.fail(code, "unreachable", err.Error())
		}
		serverURL = "https://" + host
	}
	parsed, err := url.Parse(serverURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return o.fail(exitUsage, "bad_flag", fmt.Sprintf("--server must be an https:// address, got %q", serverURL))
	}

	// Mint inside the container.
	purpose := "setup"
	mintArgs := []string{"restmail-api", "mint-token", "--purpose", purpose, "--lifetime", lifetime.String()}
	if command == "reset-admin" {
		purpose = "reset-admin"
		mintArgs = []string{"restmail-api", "mint-token", "--purpose", purpose, "--username", *username, "--lifetime", lifetime.String()}
	}
	out, errOut, status, err := execInServer(ctx, run, t, mintArgs)
	if err != nil {
		return o.fail(exitUnreachable, "unreachable", err.Error())
	}
	if status != 0 {
		// The operator command's own error, passed through with its status.
		var e struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if json.Unmarshal(lastLine(errOut), &e) == nil && e.Error.Code != "" {
			if e.Error.Code == "already_claimed" && command == "setup" {
				e.Error.Message += " (restmail reset-admin --username NAME)"
			}
			return o.fail(status, e.Error.Code, e.Error.Message)
		}
		return o.fail(exitUnreachable, "unreachable", fmt.Sprintf("running the operator command failed (status %d): %s",
			status, strings.TrimSpace(string(errOut))))
	}
	var minted struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &minted); err != nil || minted.Token == "" {
		return o.fail(exitFailed, "bad_response", fmt.Sprintf("the server's operator command answered %q", strings.TrimSpace(string(out))))
	}

	link := appLink(purpose, serverURL, minted.Token)
	if o.json {
		result := map[string]any{
			"purpose":    purpose,
			"server":     serverURL,
			"expires_at": minted.ExpiresAt,
		}
		if *showSecret {
			result["link"] = link
		} else {
			result["link_redacted"] = true
		}
		_ = json.NewEncoder(stdout).Encode(result)
		return exitOK
	}

	what := "become " + parsed.Host + "'s first admin"
	if purpose == "reset-admin" {
		what = "choose a new password and authenticator for " + *username
	}
	fmt.Fprintf(stdout, "Scan with the rest-mail app to %s.\n\n", what)
	qrterminal.GenerateWithConfig(link, qrterminal.Config{
		Level: qrterminal.M, Writer: stdout, HalfBlocks: true, QuietZone: 2,
	})
	fmt.Fprintf(stdout, "\nOr open: %s\n", link)
	fmt.Fprintf(stdout, "Works once, until %s. Anyone holding it can %s.\n", minted.ExpiresAt, what)
	return exitOK
}

// appLink is what the app opens: the server, and the token to redeem there.
func appLink(purpose, server, token string) string {
	path := "setup"
	if purpose == "reset-admin" {
		path = "reset"
	}
	q := url.Values{"server": {server}, "token": {token}}
	return "restmail://" + path + "?" + q.Encode()
}

// execInServer runs args inside the server's API container.
func execInServer(ctx context.Context, run runner, t target, args []string) ([]byte, []byte, int, error) {
	if t.docker != "" {
		return run(ctx, "docker", append([]string{"exec", t.docker}, args...)...)
	}
	name, err := apiDeployment(ctx, run, t)
	if err != nil {
		return nil, nil, 0, err
	}
	return run(ctx, "kubectl", t.kubectl(append([]string{"exec", name, "-c", "api", "--"}, args...)...)...)
}

// apiDeployment finds the API's deployment by the chart's component label, so
// it works whatever the release was called.
func apiDeployment(ctx context.Context, run runner, t target) (string, error) {
	out, errOut, status, err := run(ctx, "kubectl", t.kubectl("get", "deployment",
		"-l", "app.kubernetes.io/component=api", "-o", "name")...)
	if err != nil {
		return "", fmt.Errorf("kubectl is not available: %w", err)
	}
	if status != 0 {
		return "", fmt.Errorf("kubectl could not list deployments in %q: %s", t.namespace, strings.TrimSpace(string(errOut)))
	}
	names := strings.Fields(string(out))
	if len(names) == 0 {
		return "", fmt.Errorf("no rest-mail API deployment in namespace %q", t.namespace)
	}
	return names[0], nil
}

// ingressHost reads the server's public host from its ingress.
func ingressHost(ctx context.Context, run runner, t target) (string, int, error) {
	out, errOut, status, err := run(ctx, "kubectl", t.kubectl("get", "ingress",
		"-o", "jsonpath={.items[0].spec.rules[0].host}")...)
	if err != nil {
		return "", exitUnreachable, fmt.Errorf("kubectl is not available: %w", err)
	}
	host := strings.TrimSpace(string(out))
	if status != 0 || host == "" {
		return "", exitUnreachable, fmt.Errorf("could not read the server's address from an ingress in %q (pass --server): %s",
			t.namespace, strings.TrimSpace(string(errOut)))
	}
	return host, exitOK, nil
}

func lastLine(b []byte) []byte {
	lines := bytes.Split(bytes.TrimSpace(b), []byte("\n"))
	return lines[len(lines)-1]
}

func containsFlag(args []string, name string) bool {
	for _, a := range args {
		if a == "-"+name || a == "--"+name || strings.HasPrefix(a, "--"+name+"=") {
			return true
		}
	}
	return false
}

// output writes results and errors as text for people or JSON for programs.
type output struct {
	json           bool
	stdout, stderr io.Writer
}

func (o output) fail(status int, code, message string) int {
	if o.json {
		_ = json.NewEncoder(o.stderr).Encode(map[string]any{
			"error": map[string]string{"code": code, "message": message},
		})
	} else {
		fmt.Fprintf(o.stderr, "restmail: %s\n", message)
	}
	return status
}
