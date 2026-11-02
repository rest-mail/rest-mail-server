package dkimrotate_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/dkimrotate"
)

func TestRevokedRequiresAnExplicitEmptyPublicKey(t *testing.T) {
	for _, test := range []struct {
		record string
		want   bool
	}{
		{"v=DKIM1; p=", true},
		{"v=DKIM1; k=ed25519; p= ", true},
		{"p=", true},
		{"", false},
		{"v=DKIM1; k=rsa", false},
		{"v=DKIM1; p=still-published", false},
		{"v=DKIM2; p=", false},
		{"v=DKIM1; p=; p=", false},
	} {
		t.Run(test.record, func(t *testing.T) {
			lookup := func(_ context.Context, name string) ([]string, error) {
				if name != "old._domainkey.xn--bcher-kva.example" {
					t.Fatalf("queried %s", name)
				}
				return []string{test.record}, nil
			}
			err := dkimrotate.Revoked(t.Context(), lookup, "bücher.example", "old")
			if (err == nil) != test.want || (!test.want && !errors.Is(err, dkimrotate.ErrNotPublished)) {
				t.Fatalf("Revoked(%q) = %v, want published=%t", test.record, err, test.want)
			}
		})
	}
	if err := dkimrotate.Revoked(t.Context(), nil, "example.com", "old"); err == nil {
		t.Fatal("a missing resolver was accepted")
	}
	lookup := func(context.Context, string) ([]string, error) { return nil, errors.New("resolver unavailable") }
	if err := dkimrotate.Revoked(t.Context(), lookup, "example.com", "old"); !errors.Is(err, dkimrotate.ErrNotPublished) {
		t.Fatalf("resolver failure = %v", err)
	}
}

func TestRotationRecordRevocationDoesNotRequirePrivateMaterial(t *testing.T) {
	now := time.Now()
	key := models.DKIMRotationKey{Slot: models.DKIMRotationPrevious, PrivateKey: "unreadable", RevokedAt: &now}
	if record, err := dkimrotate.RotationRecord(key, ""); err != nil || record != "v=DKIM1; p=" {
		t.Fatalf("revoked record = %q, %v", record, err)
	}
	key.RevokedAt = nil
	if _, err := dkimrotate.RotationRecord(key, ""); err == nil {
		t.Fatal("an unreadable, unrevoked key was accepted")
	}
}
