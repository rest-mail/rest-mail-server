package testguard

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

const modulePath = "github.com/rest-mail/rest-mail-server"

// boundaryFile records, for each internal package that crosses an app
// boundary, every consumer allowed to link it.
const boundaryFile = "testdata/internal_boundary.txt"

var updateBoundary = flag.Bool("update-boundary", false, "rewrite "+boundaryFile+" from the packages the apps link today")

// The packages under internal/ are shared by very different programs: the API
// server, the console, the three protocol gateways and the maintenance tools.
// Some of them are server internals that every server binary needs; others are
// client building blocks that belong under pkg/ (#2217). This guard keeps the
// split from drifting back.
//
// A consumer is a binary under cmd/ or a public package under pkg/. The test
// fails when an internal package gains a consumer that the boundary file does
// not list, if that package is linked by two or more binaries or by any pkg/
// package. A package that only one binary links is that binary's own business
// and needs no entry. A consumer that stops linking a package is fine.
//
// When a new link is intended, rewrite the file and say why in the PR:
//
//	go test ./internal/testguard -run TestInternalPackageBoundary -update-boundary
func TestInternalPackageBoundary(t *testing.T) {
	root := repoRoot(t)
	links, err := consumerLinks(root)
	if err != nil {
		t.Fatal(err)
	}
	actual := guardedLinks(links)
	path := filepath.Join(root, "internal", "testguard", boundaryFile)

	if *updateBoundary {
		if err := writeBoundary(path, actual); err != nil {
			t.Fatal(err)
		}
		return
	}

	allowed, err := readBoundary(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range boundaryViolations(actual, allowed) {
		t.Error(problem)
	}
}

// The comparison must flag a new consumer and a newly shared package, and must
// accept a consumer that went away.
func TestBoundaryViolations(t *testing.T) {
	allowed := map[string][]string{
		"internal/db":   {"cmd/api", "cmd/seed"},
		"internal/mail": {"cmd/api", "cmd/console", "pkg/mailimport"},
	}
	actual := map[string][]string{
		"internal/db":   {"cmd/api", "cmd/console", "cmd/seed"},
		"internal/mail": {"cmd/api", "pkg/mailimport"},
		"internal/auth": {"cmd/api", "cmd/console"},
	}
	got := boundaryViolations(actual, allowed)
	want := []string{
		"cmd/api links internal/auth, which the boundary file does not allow",
		"cmd/console links internal/auth, which the boundary file does not allow",
		"cmd/console links internal/db, which the boundary file does not allow",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// A package one binary links is not guarded; one that two binaries link, or
// that a pkg/ package links, is.
func TestGuardedLinks(t *testing.T) {
	links := map[string][]string{
		"cmd/api":        {"internal/db", "internal/mail", "internal/acme"},
		"cmd/seed":       {"internal/db"},
		"pkg/mailimport": {"internal/mail"},
	}
	got := guardedLinks(links)
	want := map[string][]string{
		"internal/db":   {"cmd/api", "cmd/seed"},
		"internal/mail": {"cmd/api", "pkg/mailimport"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for pkg, consumers := range want {
		if !slices.Equal(got[pkg], consumers) {
			t.Fatalf("%s: got %q, want %q", pkg, got[pkg], consumers)
		}
	}
}

// consumerLinks maps each binary under cmd/ and each package under pkg/ to the
// internal packages it links, directly or through other packages. Test files
// are not counted. The platform is fixed to the one the servers ship on, so the
// answer is the same on every developer machine.
func consumerLinks(root string) (map[string][]string, error) {
	cmd := exec.Command("go", "list", "-f", `{{.ImportPath}} {{join .Deps " "}}`, "./cmd/...", "./pkg/...")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "GOFLAGS=")
	cmd.Stderr = os.Stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w", err)
	}
	links := map[string][]string{}
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	scanner.Buffer(nil, 1<<24)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		consumer := strings.TrimPrefix(fields[0], modulePath+"/")
		if strings.HasPrefix(consumer, "cmd/") {
			// A binary is its main package; helpers under it are not consumers.
			if strings.Count(consumer, "/") != 1 {
				continue
			}
		}
		for _, dep := range fields[1:] {
			if pkg, ok := strings.CutPrefix(dep, modulePath+"/"); ok && strings.HasPrefix(pkg, "internal/") {
				links[consumer] = append(links[consumer], pkg)
			}
		}
	}
	return links, scanner.Err()
}

// guardedLinks inverts consumer links to package consumers and keeps the
// packages that cross a boundary: linked by two or more binaries or by a pkg/
// package.
func guardedLinks(links map[string][]string) map[string][]string {
	consumers := map[string][]string{}
	for consumer, packages := range links {
		for _, pkg := range packages {
			consumers[pkg] = append(consumers[pkg], consumer)
		}
	}
	guarded := map[string][]string{}
	for pkg, list := range consumers {
		binaries := 0
		public := false
		for _, consumer := range list {
			if strings.HasPrefix(consumer, "cmd/") {
				binaries++
			} else {
				public = true
			}
		}
		if binaries >= 2 || public {
			sort.Strings(list)
			guarded[pkg] = list
		}
	}
	return guarded
}

// boundaryViolations lists each consumer that links a guarded package without
// the boundary file allowing it, sorted.
func boundaryViolations(actual, allowed map[string][]string) []string {
	var problems []string
	for pkg, consumers := range actual {
		for _, consumer := range consumers {
			if !slices.Contains(allowed[pkg], consumer) {
				problems = append(problems, fmt.Sprintf("%s links %s, which the boundary file does not allow", consumer, pkg))
			}
		}
	}
	sort.Strings(problems)
	return problems
}

func readBoundary(path string) (map[string][]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	allowed := map[string][]string{}
	for number, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("%s:%d: want \"<internal package> <consumer>,<consumer>...\"", path, number+1)
		}
		allowed[fields[0]] = strings.Split(fields[1], ",")
	}
	return allowed, nil
}

func writeBoundary(path string, guarded map[string][]string) error {
	packages := make([]string, 0, len(guarded))
	for pkg := range guarded {
		packages = append(packages, pkg)
	}
	sort.Strings(packages)
	var b strings.Builder
	b.WriteString("# Internal packages that cross an app boundary, and every consumer allowed to\n")
	b.WriteString("# link them (#2217). TestInternalPackageBoundary fails on a link not listed here.\n")
	b.WriteString("# Rewrite with:\n")
	b.WriteString("#   go test ./internal/testguard -run TestInternalPackageBoundary -update-boundary\n")
	for _, pkg := range packages {
		fmt.Fprintf(&b, "%s %s\n", pkg, strings.Join(guarded[pkg], ","))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
