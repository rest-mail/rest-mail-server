package main

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/mailimport"
)

func TestImportIMAPStartTLSRouting(t *testing.T) {
	for _, tc := range []struct {
		name, source, address string
		startTLS              bool
	}{
		{"default implicit", "imaps://old.example.test", "old.example.test:993", false},
		{"explicit implicit", "imaps://old.example.test:1993", "old.example.test:1993", false},
		{"default upgrade", "imap://old.example.test", "old.example.test:143", true},
		{"explicit upgrade", "imap://old.example.test:1143", "old.example.test:1143", true},
		{"IPv6 upgrade", "imap://[::1]:1143", "[::1]:1143", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"--source", tc.source, "--source-user", "source-user", "--account-id", "12", "--dry-run", "--json"}
			if tc.startTLS {
				args = append(args, "--starttls")
			}
			called := false
			reader := func(_ context.Context, options mailimport.IMAPOptions, limit int64, _ mailimport.Visit) error {
				called = true
				if options.StartTLS != tc.startTLS || options.Address != tc.address || options.Username != "source-user" || options.Password != "fixture-source-password" || limit != 10<<20 {
					t.Fatal("source transport, credentials or limit changed")
				}
				return nil
			}
			var out, stderr bytes.Buffer
			code := runImportIMAPSource(context.Background(), args, "", "fixture-source-password", http.DefaultClient, &out, &stderr, reader)
			if code != exitOK || !called || stderr.Len() != 0 || !strings.Contains(out.String(), `"dry_run":true`) {
				t.Fatalf("source routing: code=%d called=%v out=%s err=%s", code, called, &out, &stderr)
			}
		})
	}
}
