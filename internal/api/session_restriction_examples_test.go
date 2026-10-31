package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/examplepairs"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

// TestSessionRestrictionExamples proves the sessions_restricted pairs (#324)
// through the production router on a scratch database: once the stored server
// ceiling no longer holds mailbox:session, an account that is no administrator
// signs in to neither the API nor a mail protocol, with the right password and
// with a session it already holds.
func TestSessionRestrictionExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	f := newCredentialFixture(t)
	session := f.session // signed in before the server changes

	names, overridden, err := grants.ServerCeiling(f.db, time.Now())
	if err != nil || overridden {
		t.Fatalf("server ceiling: %v (overridden %v)", err, overridden)
	}
	var keep []string
	for _, name := range names {
		if name != models.CapMailboxSession {
			keep = append(keep, name)
		}
	}
	if len(keep) == len(names) {
		t.Fatalf("the default server ceiling lacks %s: %v", models.CapMailboxSession, names)
	}
	if err := f.db.Transaction(func(tx *gorm.DB) error {
		_, err := grants.SetServerCeiling(tx, keep, models.AuditEntry{ActorKind: models.AuditActorSystem, ActorName: "system"})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		op, path, authorization, body string
	}{
		{"login", "/api/v1/auth/login", "", fmt.Sprintf(`{"email":%q,"password":%q,"client":"native"}`, f.address, f.password)},
		{"protocolSignIn", "/api/v1/auth/protocol-sign-in", session, `{"protocol":"imap"}`},
	} {
		t.Run(c.op, func(t *testing.T) {
			pair, ok := pairs[c.op]["sessions_restricted"]
			if !ok {
				t.Fatalf("%s has no sessions_restricted pair", c.op)
			}
			examplepairs.Proven(t, c.op, "sessions_restricted")
			rr := f.do(http.MethodPost, c.path, c.authorization, c.body)
			if rr.Code != pair.Response.Status {
				t.Fatalf("got %d %s, want %d", rr.Code, rr.Body.String(), pair.Response.Status)
			}
			assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
		})
	}
}
