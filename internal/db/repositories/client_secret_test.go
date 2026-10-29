package repositories

import (
	"github.com/restmail/restmail/internal/auth"
	"testing"
)

func TestClientSecretTier(t *testing.T) {
	for _, tc := range []struct {
		cap, scope string
		allowed    bool
	}{
		{"mail:read", "mailbox:robot@example.test", true},
		{"mail:send", "domain:example.test", true},
		{"mail:receive", "mailbox:robot@example.test", true},
		{"pipelines:write", "domain:example.test", true},
		{"mailboxes:aliases:write", "domain:example.test", false},
		{"pipelines:write", "*", false},
		{"pipelines:write", "mailbox:robot@example.test", false},
		{"mailboxes:write", "domain:example.test", false},
		{"mailbox:manage", "mailbox:robot@example.test", false},
		{"users:write", "domain:example.test", false},
		{"agents:create", "domain:example.test", false},
		{"domains:write", "domain:example.test", false},
		{"audit:read", "domain:example.test", false},
		{"retention:write", "domain:example.test", false},
		{"*", "*", false},
	} {
		t.Run(tc.cap+"/"+tc.scope, func(t *testing.T) {
			_, err := NormalizeClientSecretPermissions([]auth.ScopedPermission{{Capability: tc.cap, Scope: tc.scope}})
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed %v, error %v", tc.allowed, err)
			}
			if !tc.allowed {
				if _, _, err := NewCredentialRepository(nil).CreateClientSecret(1, 1, "robot", []auth.ScopedPermission{{Capability: tc.cap, Scope: tc.scope}}, nil); err == nil {
					t.Fatal("creation accepted forbidden tier")
				}
			}
		})
	}
}
