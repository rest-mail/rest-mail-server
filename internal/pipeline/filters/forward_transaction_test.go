package filters

import (
	"context"
	"testing"

	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestForwardFiltersUseContextDatabaseWithoutChangingFactory(t *testing.T) {
	for _, name := range []string{"domain_allowlist", "contact_whitelist", "recipient_check", "sender_verify", "greylist"} {
		t.Run(name, func(t *testing.T) {
			open := func(counter *int) *gorm.DB {
				db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=unused.invalid", PreferSimpleProtocol: true}), &gorm.Config{DisableAutomaticPing: true, SkipDefaultTransaction: true, DryRun: true})
				if err != nil {
					t.Fatal(err)
				}
				pool, err := db.DB()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = pool.Close() })
				if err := db.Callback().Query().Before("gorm:query").Register("forward:database", func(*gorm.DB) { *counter++ }); err != nil {
					t.Fatal(err)
				}
				return db
			}
			var baseReads, transactionReads int
			base, transaction := open(&baseReads), open(&transactionReads)
			factory := map[string]pipeline.FilterFactory{"domain_allowlist": NewDomainAllowlist(base), "contact_whitelist": NewContactWhitelist(base), "recipient_check": NewRecipientCheck(base), "sender_verify": NewSenderVerify(base), "greylist": NewGreylist(base)}[name]
			filter, err := factory([]byte(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			email := &pipeline.EmailJSON{Envelope: pipeline.Envelope{MailFrom: "sender@example.test", RcptTo: []string{"reader@example.test"}, ClientIP: "192.0.2.1"}}
			_, _ = filter.Execute(pipeline.WithDB(context.Background(), transaction), email)
			if transactionReads == 0 || baseReads != 0 {
				t.Fatalf("delivery escaped transaction: transaction=%d base=%d", transactionReads, baseReads)
			}
			_, _ = filter.Execute(context.Background(), email)
			if baseReads == 0 {
				t.Fatal("context execution changed the shared filter's fallback database")
			}
		})
	}
}
