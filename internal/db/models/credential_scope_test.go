package models

import (
	"reflect"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/auth"
)

func TestCredentialScope_PermissionsRoundTrip(t *testing.T) {
	in := CredentialScope{
		MailboxID: 3,
		Permissions: []auth.ScopedPermission{
			{Capability: "mailboxes:write", Scope: "domain:a.com"},
			{Capability: "mail:read", Scope: "mailbox:bob@a.com"},
		},
	}
	v, err := in.Value()
	if err != nil {
		t.Fatal(err)
	}
	s := v.(string)
	if !strings.Contains(s, `"permissions":[{"capability":"mailboxes:write","scope":"domain:a.com"}`) {
		t.Fatalf("stored scope %s", s)
	}
	var out CredentialScope
	if err := out.Scan([]byte(s)); err != nil {
		t.Fatal(err)
	}
	in.Capabilities = []string{}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip: got %+v, want %+v", out, in)
	}
}

func TestCredentialScope_AppPasswordScopeUnchanged(t *testing.T) {
	in := CredentialScope{MailboxID: 3, Capabilities: []string{CredentialCapRead, CredentialCapWrite, CredentialCapSend}}
	v, err := in.Value()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(v.(string), "permissions") {
		t.Fatalf("an app password's scope gained permissions: %s", v)
	}
	var out CredentialScope
	if err := out.Scan(v); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) || out.Permissions != nil {
		t.Fatalf("round trip: got %+v, want %+v", out, in)
	}
}
