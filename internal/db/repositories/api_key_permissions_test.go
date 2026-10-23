package repositories

import (
	"errors"
	"reflect"
	"testing"

	"github.com/restmail/restmail/internal/auth"
)

func TestNormalizeAPIKeyPermissions(t *testing.T) {
	got, err := NormalizeAPIKeyPermissions([]auth.ScopedPermission{
		{Capability: "mailboxes:write", Scope: " Domain:A.com "},
		{Capability: "mailboxes:write", Scope: "domain:a.com"},
		{Capability: "mail:read", Scope: "mailbox:Bob@a.com"},
		{Capability: "mail:read", Scope: "*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []auth.ScopedPermission{
		{Capability: "mailboxes:write", Scope: "domain:a.com"},
		{Capability: "mail:read", Scope: "mailbox:bob@a.com"},
		{Capability: "mail:read", Scope: "*"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	if got, err := NormalizeAPIKeyPermissions(nil); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("no permissions: got %#v, %v; want an empty list", got, err)
	}

	for _, bad := range []auth.ScopedPermission{
		{Capability: "", Scope: "*"},
		{Capability: "*", Scope: "domain:a.com"},
		{Capability: "mail:read", Scope: ""},
		{Capability: "mail:read", Scope: "a.com"},
		{Capability: "mail:read", Scope: "mailbox:nobody"},
	} {
		if _, err := NormalizeAPIKeyPermissions([]auth.ScopedPermission{bad}); !errors.Is(err, ErrBadAPIKeyPermission) {
			t.Errorf("%+v: err = %v, want ErrBadAPIKeyPermission", bad, err)
		}
	}
}
