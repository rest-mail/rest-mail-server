package server

import (
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

func TestBuildFlags_NoFlags(t *testing.T) {
	if result := buildFlags(imap.Message{}); result != "" {
		t.Errorf("got %q, want empty", result)
	}
}

func TestBuildFlags_Seen(t *testing.T) {
	if result := buildFlags(imap.Message{Seen: true}); result != `\Seen` {
		t.Errorf("got %q, want %q", result, `\Seen`)
	}
}

func TestBuildFlags_Flagged(t *testing.T) {
	if result := buildFlags(imap.Message{Flagged: true}); result != `\Flagged` {
		t.Errorf("got %q, want %q", result, `\Flagged`)
	}
}

func TestBuildFlags_Draft(t *testing.T) {
	if result := buildFlags(imap.Message{Draft: true}); result != `\Draft` {
		t.Errorf("got %q, want %q", result, `\Draft`)
	}
}

func TestBuildFlags_AllFlags(t *testing.T) {
	result := buildFlags(imap.Message{Seen: true, Flagged: true, Draft: true})
	if !strings.Contains(result, `\Seen`) {
		t.Errorf("result %q missing \\Seen", result)
	}
	if !strings.Contains(result, `\Flagged`) {
		t.Errorf("result %q missing \\Flagged", result)
	}
	if !strings.Contains(result, `\Draft`) {
		t.Errorf("result %q missing \\Draft", result)
	}
}
