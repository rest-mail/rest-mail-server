package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/gateway/tlsutil"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"gorm.io/gorm"
)

// An address at a mail provider can be written down here in several ways: as where
// an alias forwards, as a recovery address, as a contact. None of them may make the
// provider's domain local (#2109 AC01). If one did, this server would answer for
// strangers at that provider, and could take mail meant for them.
//
// External accounts do not exist yet, and how one is stored is still undecided, so
// this test covers the ways an address can be added today and decides nothing about
// that model. It runs on the real router and a real database, and reads the outcome
// from each place the server asks whether a domain or address is its own.
const (
	externalProviderDomain  = "provider.example"
	externalProviderAddress = "me@" + externalProviderDomain
	externalProviderOther   = "stranger@" + externalProviderDomain
)

// localAnswers is what the server says about an address and its domain.
type localAnswers struct {
	GatewayExists, RestmailExists bool
	GatewayMailboxID              uint
	DeliveryStatus                int
	DeliveryCode                  string
	DiscoveryStatus               int
	AutoconfigStatus              int
	Hosted, HostedForTLS          bool
}

func askWhetherLocal(t *testing.T, router http.Handler, db *gorm.DB, address, domain string) localAnswers {
	t.Helper()
	var answers localAnswers
	query := url.QueryEscape(address)

	gateway := doRequest(router, http.MethodGet, "/api/mailboxes?address="+query, "", "")
	var checked struct {
		Data struct {
			Exists    bool `json:"exists"`
			MailboxID uint `json:"mailbox_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(gateway.Body.Bytes(), &checked); gateway.Code != http.StatusOK || err != nil {
		t.Fatalf("gateway check of %s: %d %s", address, gateway.Code, gateway.Body)
	}
	answers.GatewayExists, answers.GatewayMailboxID = checked.Data.Exists, checked.Data.MailboxID

	restmail := doRequest(router, http.MethodGet, "/restmail/mailboxes?address="+query, "", "")
	var peer struct {
		Data struct {
			Exists bool `json:"exists"`
		} `json:"data"`
	}
	if err := json.Unmarshal(restmail.Body.Bytes(), &peer); restmail.Code != http.StatusOK || err != nil {
		t.Fatalf("RESTMAIL check of %s: %d %s", address, restmail.Code, restmail.Body)
	}
	answers.RestmailExists = peer.Data.Exists

	message := fmt.Sprintf(`{"from":"sender@remote.test","to":[%q],"subject":"For a provider","body_text":"Hello."}`, address)
	delivery := doRequest(router, http.MethodPost, "/restmail/messages", "", message)
	var refusal struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(delivery.Body.Bytes(), &refusal)
	answers.DeliveryStatus, answers.DeliveryCode = delivery.Code, refusal.Error.Code

	answers.DiscoveryStatus = doRequest(router, http.MethodGet, "/.well-known/restmail?domain="+url.QueryEscape(domain), "", "").Code
	answers.AutoconfigStatus = doRequest(router, http.MethodGet, "/.well-known/autoconfig/mail/config-v1.1.xml?emailaddress="+query, "", "").Code

	var err error
	if answers.Hosted, err = repositories.IsHostedDomain(db, domain); err != nil {
		t.Fatal(err)
	}
	if answers.HostedForTLS, err = tlsutil.HostedInDatabase(db)(domain); err != nil {
		t.Fatal(err)
	}
	return answers
}

func countRows(t *testing.T, db *gorm.DB, query string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := db.Raw(query, args...).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}

func TestAddingAnExternalAddressLeavesItsProviderDomainForeign(t *testing.T) {
	f := newGrantsFixture(t)
	f.router = NewRouter(f.db, auth.NewJWTService("grants-access-secret", 15*time.Minute, 24*time.Hour), &config.Config{
		CORSAllowedOrigins: []string{"http://localhost:3000"}, Environment: "test", TOTP2FAEnabled: true,
		MasterKey: accessMasterKey, RecoveryEmailBaseURL: "https://mail.access.test", GatewayHostname: "mx.access.test",
	}, nil)
	if err := grants.EnsureDefaults(f.db, &f.ma); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&models.Pipeline{DomainID: f.a.ID, Direction: "inbound", Filters: json.RawMessage(`[]`), Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	owner := f.admins["owner"]
	token := f.token(t, "owner")

	foreign := localAnswers{DeliveryStatus: http.StatusUnprocessableEntity, DeliveryCode: "undeliverable",
		DiscoveryStatus: http.StatusNotFound, AutoconfigStatus: http.StatusNotFound}
	assertForeign := func(t *testing.T, when string) {
		t.Helper()
		for _, address := range []string{externalProviderAddress, externalProviderOther} {
			if got := askWhetherLocal(t, f.router, f.db, address, externalProviderDomain); got != foreign {
				t.Errorf("%s, %s:\n got %+v\nwant %+v", when, address, got, foreign)
			}
		}
	}

	// The probes can tell: a mailbox on a domain hosted here answers yes to each.
	t.Run("a hosted address answers as local", func(t *testing.T) {
		got := askWhetherLocal(t, f.router, f.db, f.ma.Address, f.a.Name)
		want := localAnswers{GatewayExists: true, RestmailExists: true, GatewayMailboxID: f.ma.ID,
			DeliveryStatus: http.StatusCreated, DiscoveryStatus: http.StatusOK, AutoconfigStatus: http.StatusOK,
			Hosted: true, HostedForTLS: true}
		if got != want {
			t.Fatalf("got %+v\nwant %+v", got, want)
		}
	})

	assertForeign(t, "before any address was added")
	messages := countRows(t, f.db, `SELECT count(*) FROM messages`)

	account := strconv.FormatUint(uint64(owner.user.ID), 10)
	for _, add := range []struct{ name, path, body string }{
		{"alias destination", "/api/v1/admin/aliases", fmt.Sprintf(`{"source_address":"forward@%s","destination_address":%q}`, f.a.Name, externalProviderAddress)},
		{"recovery address", "/api/v1/auth/recovery-email", fmt.Sprintf(`{"email":%q,"current_password":%q}`, externalProviderAddress, accessPassword)},
		{"contact", "/api/v1/accounts/" + account + "/contacts", fmt.Sprintf(`{"email":%q}`, externalProviderAddress)},
		{"blocked sender", "/api/v1/accounts/" + account + "/contacts/block", fmt.Sprintf(`{"email":%q}`, externalProviderOther)},
	} {
		t.Run("add as "+add.name, func(t *testing.T) {
			response := doRequest(f.router, http.MethodPost, add.path, token, add.body)
			if response.Code < 200 || response.Code > 299 {
				t.Fatalf("%d %s", response.Code, response.Body)
			}
			assertForeign(t, "after adding it as "+add.name)
		})
	}

	// The alias forwards to the provider. It gives the address no mailbox here.
	t.Run("an alias to the address stays a forward", func(t *testing.T) {
		recipient, err := repositories.ResolveRecipient(f.db, "forward@"+f.a.Name, false)
		if err != nil {
			t.Fatal(err)
		}
		if !recipient.Alias || recipient.Mailbox.ID != 0 || recipient.Address != externalProviderAddress {
			t.Fatalf("resolved to %+v, want a forward to %s and no mailbox", recipient, externalProviderAddress)
		}
	})

	// Nor can anyone take the address, or the domain, by naming it.
	t.Run("the address cannot be claimed", func(t *testing.T) {
		for _, claim := range []struct {
			name, path, body string
			status           int
			code             string
		}{
			{"as an account", "/api/v1/accounts", fmt.Sprintf(`{"address":%q}`, externalProviderAddress), http.StatusUnprocessableEntity, "validation_failed"},
			{"as an alias source", "/api/v1/admin/aliases", fmt.Sprintf(`{"source_address":%q,"destination_address":%q}`, externalProviderOther, f.ma.Address), http.StatusNotFound, "domain_not_found"},
			{"by linking it", "/api/v1/accounts/link", fmt.Sprintf(`{"address":%q,"password":%q}`, externalProviderAddress, accessPassword), http.StatusForbidden, "invalid_credentials"},
		} {
			response := doRequest(f.router, http.MethodPost, claim.path, token, claim.body)
			var refusal struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			_ = json.Unmarshal(response.Body.Bytes(), &refusal)
			if response.Code != claim.status || refusal.Error.Code != claim.code {
				t.Errorf("%s: %d %s, want %d %s", claim.name, response.Code, response.Body, claim.status, claim.code)
			}
		}
		assertForeign(t, "after the refused claims")
	})

	// What the database holds, read without going through the code under test.
	t.Run("nothing local was stored for the provider", func(t *testing.T) {
		like := "%@" + externalProviderDomain
		for name, count := range map[string]int64{
			"domains":       countRows(t, f.db, `SELECT count(*) FROM domains WHERE name = ?`, externalProviderDomain),
			"mailboxes":     countRows(t, f.db, `SELECT count(*) FROM mailboxes WHERE address LIKE ?`, like),
			"alias sources": countRows(t, f.db, `SELECT count(*) FROM aliases WHERE source_address LIKE ?`, like),
			"new messages":  countRows(t, f.db, `SELECT count(*) FROM messages`) - messages,
		} {
			if count != 0 {
				t.Errorf("%s: %d rows, want none", name, count)
			}
		}
		if forwards := countRows(t, f.db, `SELECT count(*) FROM aliases WHERE destination_address = ?`, externalProviderAddress); forwards != 1 {
			t.Errorf("alias destinations: %d rows, want the 1 added", forwards)
		}
	})
}
