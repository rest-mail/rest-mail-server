package outbound

import (
	"context"
	"errors"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestSigningDomainLookup(t *testing.T) {
	const raw = "From: alice@example.test\r\nSubject: Hello\r\n\r\nMessage body\r\n"
	const domain = "example.test"
	lookupFailure := errors.New("signing domain store unavailable")
	for _, scenario := range []struct {
		name        string
		lookupError error
		wantError   error
	}{
		{name: "unhosted domain", lookupError: gorm.ErrRecordNotFound},
		{name: "failed lookup", lookupError: lookupFailure, wantError: lookupFailure},
		{name: "cancelled lookup", wantError: context.Canceled},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario.wantError == context.Canceled {
				cancel()
			}
			db, err := gorm.Open(postgres.Open("host=unused.invalid"), &gorm.Config{DisableAutomaticPing: true, DryRun: true})
			if err != nil {
				t.Fatal(err)
			}
			pool, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = pool.Close() })
			if err := db.Callback().Query().Before("gorm:query").Register("test:signing_lookup", func(query *gorm.DB) {
				if err := query.Statement.Context.Err(); err != nil {
					_ = query.AddError(err)
				} else if scenario.lookupError != nil {
					_ = query.AddError(scenario.lookupError)
				}
			}); err != nil {
				t.Fatal(err)
			}
			for _, operation := range []struct {
				name string
				run  func() (string, error)
			}{
				{name: "sign", run: func() (string, error) { return SignDKIM(ctx, db, "", domain, raw) }},
				{name: "prepare", run: func() (string, error) { return Prepare(ctx, db, "", domain, raw) }},
				{name: "seal", run: func() (string, error) { return SealARC(ctx, db, "", domain, raw, "", "none") }},
			} {
				t.Run(operation.name, func(t *testing.T) {
					got, err := operation.run()
					if scenario.wantError != nil {
						if got != "" || !errors.Is(err, scenario.wantError) {
							t.Fatalf("got message %q and error %v; want no message and %v", got, err, scenario.wantError)
						}
					} else if err != nil || got != raw {
						t.Fatalf("got message %q and error %v; want unchanged message and no error", got, err)
					}
				})
			}
		})
	}
}
