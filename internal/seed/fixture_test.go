package seed

import "testing"

func TestBuildFixtureUsesDomain(t *testing.T) {
	fx := BuildFixture("mail4.test", "hash123")

	if fx.Domain.Name != "mail4.test" {
		t.Errorf("domain = %q, want mail4.test", fx.Domain.Name)
	}
	if len(fx.Mailboxes) != 3 || len(fx.Aliases) != 2 || len(fx.WebmailAddresses) != 2 {
		t.Fatalf("unexpected counts: %d mailboxes, %d aliases, %d webmail", len(fx.Mailboxes), len(fx.Aliases), len(fx.WebmailAddresses))
	}

	wantAddrs := map[string]bool{"eve@mail4.test": false, "frank@mail4.test": false, "postmaster@mail4.test": false}
	for _, mb := range fx.Mailboxes {
		if _, ok := wantAddrs[mb.Address]; !ok {
			t.Errorf("unexpected mailbox address %q", mb.Address)
		}
		wantAddrs[mb.Address] = true
		if mb.Password != "hash123" {
			t.Errorf("mailbox %q password = %q, want hash123", mb.Address, mb.Password)
		}
		if mb.DomainID != 0 {
			t.Errorf("mailbox %q DomainID = %d, want 0 (filled after domain create)", mb.Address, mb.DomainID)
		}
	}
	for a, seen := range wantAddrs {
		if !seen {
			t.Errorf("missing mailbox %q", a)
		}
	}

	for _, al := range fx.Aliases {
		if al.DestinationAddress != "eve@mail4.test" {
			t.Errorf("alias %q -> %q, want dest eve@mail4.test", al.SourceAddress, al.DestinationAddress)
		}
	}
}

func TestBuildFixtureNoCrossDomainLeak(t *testing.T) {
	fx := BuildFixture("acme.example", "h")
	for _, mb := range fx.Mailboxes {
		if got := mb.Address; got[len(got)-len("acme.example"):] != "acme.example" {
			t.Errorf("mailbox %q not under acme.example", got)
		}
	}
}
