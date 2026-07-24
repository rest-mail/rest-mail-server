// Package seed builds the dev fixture data (test domain + mailboxes + aliases)
// that `cmd/seed` persists. The fixture is parameterized by domain so any
// instance can be seeded, not just the primary domain. RBAC/role seeding is
// domain-agnostic and stays in cmd/seed.
package seed

import "github.com/restmail/restmail/internal/db/models"

// Fixture is the domain-scoped test data for one instance. DomainID on the
// mailboxes/aliases is left zero — cmd/seed fills it after the domain row is
// created.
type Fixture struct {
	Domain           models.Domain
	Mailboxes        []models.Mailbox
	Aliases          []models.Alias
	WebmailAddresses []string // mailbox addresses that also get a webmail account
}

const quotaBytes = 1073741824 // 1 GiB

// BuildFixture returns the fixture for domain, with the given already-hashed
// password on every mailbox. Pure — no DB, no hashing — so it is unit-testable.
func BuildFixture(domain, passwordHash string) Fixture {
	addr := func(local string) string { return local + "@" + domain }
	return Fixture{
		Domain: models.Domain{Name: domain, ServerType: "restmail", Active: true, DefaultQuotaBytes: quotaBytes},
		Mailboxes: []models.Mailbox{
			{LocalPart: "eve", Address: addr("eve"), Password: passwordHash, DisplayName: "Eve Wilson", QuotaBytes: quotaBytes, Active: true},
			{LocalPart: "frank", Address: addr("frank"), Password: passwordHash, DisplayName: "Frank Miller", QuotaBytes: quotaBytes, Active: true},
			{LocalPart: "postmaster", Address: addr("postmaster"), Password: passwordHash, DisplayName: "Postmaster", QuotaBytes: quotaBytes, Active: true},
		},
		Aliases: []models.Alias{
			{SourceAddress: addr("info"), DestinationAddress: addr("eve"), Active: true},
			{SourceAddress: addr("admin"), DestinationAddress: addr("eve"), Active: true},
		},
		WebmailAddresses: []string{addr("eve"), addr("frank")},
	}
}
