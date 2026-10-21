package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/acme"
)

// A second invocation must refuse even a reusable pair: the first issuer may
// currently be between the key and certificate renames.
func TestRenewACME_RejectsConcurrentIssuer(t *testing.T) {
	dir := t.TempDir()
	const primary = "mail.example.test"
	cert, key := renewalTestPair(t, time.Now().Add(-time.Hour), time.Now().Add(90*24*time.Hour), primary)
	if err := writeServingPair(dir, primary, cert, key); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".acme-issue.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	for _, window := range []time.Duration{0, acme.RenewalWindow} {
		err := renewACME(context.Background(), dir, "invalid listen address", acme.IssueConfig{
			Domains: []string{primary}, Directory: "http://127.0.0.1:1/directory",
		}, window)
		if err == nil || !strings.Contains(err.Error(), "another ACME issuer") {
			t.Fatalf("renew-before=%s: want concurrent issuer error, got %v", window, err)
		}
	}
	for suffix, want := range map[string]string{".crt": cert, ".key": key} {
		got, err := os.ReadFile(filepath.Join(dir, primary+suffix))
		if err != nil || string(got) != want {
			t.Fatalf("rejected issuer changed %s: %v", suffix, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 3 {
		t.Fatalf("rejected issuer wrote account or temporary files: %v (%v)", entries, err)
	}
}

// Test helper exits without closing its lock to exercise OS process cleanup.
func TestACMELockProcess(t *testing.T) {
	dir := os.Getenv("RESTMAIL_TEST_ACME_LOCK_DIR")
	if dir == "" {
		return
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".acme-issue.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if os.Getenv("RESTMAIL_TEST_ACME_LOCK_BUSY") == "1" && err == syscall.EWOULDBLOCK {
			os.Exit(0)
		}
		t.Fatal(err)
	}
	if os.Getenv("RESTMAIL_TEST_ACME_LOCK_BUSY") == "1" {
		t.Fatal("other process acquired an already-held lock")
	}
	os.Exit(0)
}

func runACMELockProcess(t *testing.T, dir string, busy bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestACMELockProcess$")
	cmd.Env = append(os.Environ(), "RESTMAIL_TEST_ACME_LOCK_DIR="+dir, "RESTMAIL_TEST_ACME_LOCK_BUSY=0")
	if busy {
		cmd.Env = append(cmd.Env, "RESTMAIL_TEST_ACME_LOCK_BUSY=1")
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("lock subprocess: %v\n%s", err, out)
	}
}

func TestACMELock_ProcessesAndExit(t *testing.T) {
	dir := t.TempDir()
	lock, err := lockACMEIssuer(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	info, err := lock.Stat()
	if err != nil || info.Mode().Perm() != 0600 || info.Size() != 0 {
		t.Fatalf("want empty private lock file: %v (%v)", info, err)
	}
	runACMELockProcess(t, dir, true)
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	// A process that exits without closing its lock must not leave a stale
	// lock. Reacquiring uses the same persistent inode rather than deleting it.
	runACMELockProcess(t, dir, false)
	next, err := lockACMEIssuer(dir)
	if err != nil {
		t.Fatalf("lock not released on process exit: %v", err)
	}
	defer func() { _ = next.Close() }()
	nextInfo, err := next.Stat()
	if err != nil || !os.SameFile(info, nextInfo) {
		t.Fatalf("lock inode was replaced: %v (%v)", nextInfo, err)
	}
}

func TestRenewACME_ReleasesLockAfterFailure(t *testing.T) {
	dir := t.TempDir()
	if err := renewACME(context.Background(), dir, "invalid listen address", acme.IssueConfig{
		Domains: []string{"mail.example.test"},
	}, 0); err == nil || !strings.Contains(err.Error(), "listen for HTTP-01") {
		t.Fatalf("want listener failure after acquiring lock: %v", err)
	}
	lock, err := lockACMEIssuer(dir)
	if err != nil {
		t.Fatalf("lock not released after failed issuance: %v", err)
	}
	_ = lock.Close()
}

func TestRenewACME_LockErrorStopsIssuance(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".acme-issue.lock"), 0700); err != nil {
		t.Fatal(err)
	}
	err := renewACME(context.Background(), dir, "invalid listen address", acme.IssueConfig{
		Domains: []string{"mail.example.test"},
	}, 0)
	if err == nil || !strings.Contains(err.Error(), "open ACME issuer lock") {
		t.Fatalf("want lock error before opening listener or contacting CA: %v", err)
	}
}
