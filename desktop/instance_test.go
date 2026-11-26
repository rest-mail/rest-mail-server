package main

import "testing"

func TestOneCopyRunsWhereverItIsLaunchedFrom(t *testing.T) {
	if singleInstanceID("") != "com.restmail.desktop" {
		t.Fatal("every normal launch must take the same lock, whatever its location")
	}
}

func TestOnlyANamedTestCopyRunsSeparately(t *testing.T) {
	if singleInstanceID("polish") == singleInstanceID("") {
		t.Fatal("a named test copy must take its own lock")
	}
	for _, invalid := range []string{" ", "../x", "UPPER", "with space", "a-very-long-name-that-goes-past-thirty-two-chars"} {
		if singleInstanceID(invalid) != singleInstanceID("") {
			t.Fatalf("an invalid instance name %q must not escape the single-copy lock", invalid)
		}
	}
}

func TestATestCopySaysSoInItsTitle(t *testing.T) {
	if got := windowTitle("REST Mail 0.1", "polish"); got != "REST Mail 0.1 — test copy polish" {
		t.Fatalf("title %q", got)
	}
	if got := windowTitle("REST Mail 0.1", ""); got != "REST Mail 0.1" {
		t.Fatalf("title %q", got)
	}
}
