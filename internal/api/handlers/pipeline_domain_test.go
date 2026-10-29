package handlers

import (
	"errors"
	"net/http"
	"testing"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// A failed domain lookup is the server's fault, not the caller's: it must not
// be reported as a refusal, which would tell an administrator they lack rights
// they actually hold.
func TestCheckPipelineDomainSeparatesLookupFailureFromRefusal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scope  string
		lookup func(*gorm.DB)
		status int
	}{
		{"own domain", "domain:a.test", func(tx *gorm.DB) { tx.Statement.Dest.(*models.Domain).Name = "a.test" }, 0},
		{"other domain", "domain:b.test", func(tx *gorm.DB) { tx.Statement.Dest.(*models.Domain).Name = "a.test" }, http.StatusForbidden},
		{"mailbox only", "mailbox:x@a.test", func(tx *gorm.DB) { tx.Statement.Dest.(*models.Domain).Name = "a.test" }, http.StatusForbidden},
		{"no such domain", "domain:a.test", func(tx *gorm.DB) { _ = tx.AddError(gorm.ErrRecordNotFound) }, http.StatusForbidden},
		{"lookup failed", "domain:a.test", func(tx *gorm.DB) { _ = tx.AddError(errors.New("connection reset")) }, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=127.0.0.1 user=unused dbname=unused sslmode=disable"}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Query().Replace("gorm:query", tc.lookup); err != nil {
				t.Fatal(err)
			}
			h := &PipelineHandler{db: db}
			var got error
			scopedRequest([]string{tc.scope}, "pipelines:write", func(w http.ResponseWriter, r *http.Request) {
				got = h.checkPipelineDomain(r, "pipelines:write", 1)
			}, "/")
			if tc.status == 0 {
				if got != nil {
					t.Fatalf("got %v, want allowed", got)
				}
				return
			}
			var refusal *endpoint.Error
			if !errors.As(got, &refusal) || refusal.Status != tc.status {
				t.Fatalf("got %v, want status %d", got, tc.status)
			}
			if tc.status == http.StatusForbidden && refusal.Message != "Insufficient permissions" {
				t.Fatalf("refusal message %q", refusal.Message)
			}
		})
	}
}
