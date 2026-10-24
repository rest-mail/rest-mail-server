package main

import (
	"fmt"
	"testing"

	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
)

// The SRV records name the ports the client listeners are configured on, read
// from the same settings the gateways bind (#712), so a listener moved to
// another port moves its record with it.
func TestDNSRecords_SRVFollowTheListeners(t *testing.T) {
	cfg := &config.Config{SMTPPortSubmissionTLS: 2465, IMAPTLSPort: 2993, POP3TLSPort: 2995}
	entry := recordsForDomain(models.Domain{Name: "example.com"}, "mail.example.com", "", "", clientPorts(cfg))
	got := map[string]string{}
	for _, r := range entry.Records {
		if r.Type == "SRV" {
			got[r.Name] = fmt.Sprintf("%d %d %d %s", r.Priority, r.Weight, r.Port, r.Value)
		}
	}
	want := map[string]string{
		"_submissions._tcp.example.com": "0 1 2465 mail.example.com",
		"_imaps._tcp.example.com":       "0 1 2993 mail.example.com",
		"_pop3s._tcp.example.com":       "0 1 2995 mail.example.com",
		"_submission._tcp.example.com":  "0 0 0 .",
		"_imap._tcp.example.com":        "0 0 0 .",
		"_pop3._tcp.example.com":        "0 0 0 .",
	}
	if len(got) != len(want) {
		t.Fatalf("SRV records %v, want %v", got, want)
	}
	for name, value := range want {
		if got[name] != value {
			t.Errorf("%s = %q, want %q", name, got[name], value)
		}
	}
}
