package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSingleInstanceIDIsPerExecutablePath(t *testing.T) {
	installed := singleInstanceID("/Users/someone/Applications/RESTMail.app/Contents/MacOS/RESTMail")
	if installed != singleInstanceID("/Users/someone/Applications/RESTMail.app/Contents/MacOS/RESTMail") {
		t.Fatal("the same executable must always take the same lock")
	}
	if installed == singleInstanceID("/checkout/desktop/bin/RESTMail.app/Contents/MacOS/RESTMail") {
		t.Fatal("a development build must not share the installed app's lock")
	}
	if !strings.HasPrefix(installed, "com.restmail.desktop.") {
		t.Fatalf("unexpected lock name %q", installed)
	}
}

func TestSingleInstanceIDFollowsSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "RESTMail")
	if err := os.WriteFile(target, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if singleInstanceID(link) != singleInstanceID(target) {
		t.Fatal("a symlink to the app must take the app's own lock")
	}
}
