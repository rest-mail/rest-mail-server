package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The MTA-STS and relay saves read their row and then write it (#1164). These
// proofs run two requests at once on separate PostgreSQL connections. A hook
// on the policy row's read holds the first request there until the second
// has read too: without a lock both read the same old row, and the second
// writes from it after the first committed. With the domain row locked the
// second request cannot reach its read, so the hold times out, the first
// commits, and the second reads what the first wrote.

// policyRaceHold is how long the first request waits at its read for the
// second. Under the lock the second never arrives, so every proof spends it.
const policyRaceHold = 750 * time.Millisecond

type policyRaceKey struct{}

// policyRacer is one request in a race: read is closed when the request has
// read the policy row, then the request waits for waitFor (or the hold).
type policyRacer struct {
	once    sync.Once
	read    chan struct{}
	waitFor <-chan struct{}
	hold    time.Duration
}

func newPolicyRacer(waitFor <-chan struct{}, hold time.Duration) *policyRacer {
	return &policyRacer{read: make(chan struct{}), waitFor: waitFor, hold: hold}
}

func (p *policyRacer) reached() {
	p.once.Do(func() {
		close(p.read)
		select {
		case <-p.waitFor:
		case <-time.After(p.hold):
		}
	})
}

// policyRaceDB is a PostgreSQL database in a schema of its own, with the
// tables a domain's deletion touches, and a hook on reads of table.
func policyRaceDB(t *testing.T, table string) *gorm.DB {
	t.Helper()
	if os.Getenv("DB_HOST") == "" {
		t.Skip("PostgreSQL policy save race proof runs in CI with DB_HOST; no local database is started")
	}
	base, err := rmdb.Connect(testDBConfig())
	if err != nil {
		t.Fatal("could not connect to the configured PostgreSQL test database")
	}
	basePool, err := base.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = basePool.Close() })
	schema := fmt.Sprintf("policy_race_%d", time.Now().UnixNano())
	if err := base.Exec(`CREATE SCHEMA "` + schema + `"`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Exec(`DROP SCHEMA "` + schema + `" CASCADE`).Error })
	connection, err := pgx.ParseConfig(base.Dialector.(*postgres.Dialector).Config.DSN)
	if err != nil {
		t.Fatal("could not derive isolated test connection")
	}
	connection.RuntimeParams["search_path"] = schema
	pool := stdlib.OpenDB(*connection)
	pool.SetMaxOpenConns(6)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, DisableForeignKeyConstraintWhenMigrating: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateModels(db, &models.Domain{}, &models.Mailbox{}, &models.Alias{}, &models.Role{}, &models.Capability{}, &models.RoleCapability{}, &models.Grant{}, &models.GrantActivation{}, &models.Credential{},
		&models.Pipeline{}, &models.CustomFilter{}, &models.DomainSenderRule{}, &models.Certificate{}, &models.DKIMRotationKey{}, &models.MTASTSPolicy{}, &models.DomainRelay{}, &models.Suppression{}); err != nil {
		t.Fatal(err)
	}
	if err := migrateAuditLog(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Query().After("gorm:query").Register("policy_race_read", func(q *gorm.DB) {
		if q.Statement.Table != table || q.Statement.Context == nil {
			return
		}
		if racer, ok := q.Statement.Context.Value(policyRaceKey{}).(*policyRacer); ok {
			racer.reached()
		}
	}); err != nil {
		t.Fatal(err)
	}
	return db
}

// policyRaceCall runs one request in the background; the recorder is ready
// once done is closed.
func policyRaceCall(t *testing.T, h http.Handler, racer *policyRacer, method, path, body string) (*httptest.ResponseRecorder, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	ctx = context.WithValue(ctx, domainPolicyContextKey{}, true)
	ctx = context.WithValue(ctx, middleware.ClaimsKey, freshTwoFactorClaims(1, "admin@other.test"))
	if racer != nil {
		ctx = context.WithValue(ctx, policyRaceKey{}, racer)
	}
	r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer cancel()
		h.ServeHTTP(rr, r)
	}()
	return rr, done
}

func waitPolicyRace(t *testing.T, what string, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s never happened", what)
	}
}

// racePolicySaves runs save A, and once A has read the policy row, save B.
// B is held at its own read until A has finished.
func racePolicySaves(t *testing.T, h http.Handler, path, bodyA, bodyB string) (a, b *httptest.ResponseRecorder) {
	t.Helper()
	aFinished := make(chan struct{})
	bRacer := newPolicyRacer(aFinished, 10*time.Second)
	aRacer := newPolicyRacer(bRacer.read, policyRaceHold)
	a, aDone := policyRaceCall(t, h, aRacer, http.MethodPut, path, bodyA)
	waitPolicyRace(t, "save A reading the policy", aRacer.read)
	b, bDone := policyRaceCall(t, h, bRacer, http.MethodPut, path, bodyB)
	waitPolicyRace(t, "save A", aDone)
	close(aFinished)
	waitPolicyRace(t, "save B", bDone)
	return a, b
}

func policyRaceDomain(t *testing.T, db *gorm.DB) models.Domain {
	t.Helper()
	domain := models.Domain{Name: fmt.Sprintf("policy-race-%d.test", time.Now().UnixNano()), Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	return domain
}

func TestMTASTSPostgresConcurrentSavesKeepBothFields(t *testing.T) {
	for _, existing := range []bool{true, false} {
		name := map[bool]string{true: "existing policy", false: "first save"}[existing]
		t.Run(name, func(t *testing.T) {
			db := policyRaceDB(t, "mtasts_policies")
			domain := policyRaceDomain(t, db)
			if existing {
				if err := db.Create(&models.MTASTSPolicy{DomainID: domain.ID, Mode: "testing", MXHosts: "old.example.test", MaxAge: 86400, Active: true}).Error; err != nil {
					t.Fatal(err)
				}
			}
			path := fmt.Sprintf("/api/v1/admin/domains/%d/mta-sts", domain.ID)
			a, b := racePolicySaves(t, domainPolicyFaultRouter(db, nil), path, `{"mode":"enforce"}`, `{"mx_hosts":"new.example.test"}`)
			wantA, wantB := 200, 200
			if !existing {
				wantA = 201
			}
			if a.Code != wantA || b.Code != wantB {
				t.Fatalf("saves answered %d %s and %d %s, want %d and %d", a.Code, a.Body, b.Code, b.Body, wantA, wantB)
			}
			var stored []models.MTASTSPolicy
			if err := db.Where("domain_id = ?", domain.ID).Find(&stored).Error; err != nil {
				t.Fatal(err)
			}
			if len(stored) != 1 || stored[0].Mode != "enforce" || stored[0].MXHosts != "new.example.test" {
				t.Fatalf("concurrent saves left %+v, want one policy with both saves' fields", stored)
			}
		})
	}
}

func TestRelayPostgresConcurrentSavesKeepTheNewPassword(t *testing.T) {
	const relayBody = `{"transport":"relay","host":"smtp.example.test","port":465,"tls_mode":"implicit","username":"operator"`
	for _, existing := range []bool{true, false} {
		name := map[bool]string{true: "existing relay", false: "first save"}[existing]
		t.Run(name, func(t *testing.T) {
			db := policyRaceDB(t, "domain_relays")
			domain := policyRaceDomain(t, db)
			if existing {
				old, err := models.EncryptRelayPassword("old-relay-secret", relayTestMasterKey)
				if err != nil {
					t.Fatal(err)
				}
				if err := db.Create(&models.DomainRelay{DomainID: domain.ID, Transport: "relay", Host: "smtp.example.test", Port: 465, TLSMode: "implicit", Username: "operator", PasswordEnc: old}).Error; err != nil {
					t.Fatal(err)
				}
			}
			// B keeps the stored password: under the lock that is A's new one.
			// A first save has none to keep, so B sends the same one there.
			bodyB := relayBody + `}`
			if !existing {
				bodyB = relayBody + `,"password":"new-relay-secret"}`
			}
			path := fmt.Sprintf("/api/v1/admin/domains/%d/relay", domain.ID)
			a, b := racePolicySaves(t, domainPolicyFaultRouter(db, nil), path, relayBody+`,"password":"new-relay-secret"}`, bodyB)
			if a.Code != 200 || b.Code != 200 {
				t.Fatalf("saves answered %d %s and %d %s, want 200 and 200", a.Code, a.Body, b.Code, b.Body)
			}
			var stored []models.DomainRelay
			if err := db.Where("domain_id = ?", domain.ID).Find(&stored).Error; err != nil {
				t.Fatal(err)
			}
			if len(stored) != 1 {
				t.Fatalf("concurrent saves left %d relay rows, want 1", len(stored))
			}
			if password, err := models.LoadRelayPassword(stored[0].PasswordEnc, relayTestMasterKey); err != nil || password != "new-relay-secret" {
				t.Fatal("a concurrent save that kept the password wrote back the stale one")
			}
		})
	}
}

// A save that has read the domain and its policy row while the domain is
// deleted: the deletion either waits for the save and removes what it wrote,
// or the save answers 404. Never an orphan row or a 500.
func TestDomainPolicyPostgresSaveRacingDomainDeletion(t *testing.T) {
	routes := []struct{ name, table, suffix, body string }{
		{"MTA-STS", "mtasts_policies", "mta-sts", `{"mode":"enforce","mx_hosts":"mx.example.test"}`},
		{"relay", "domain_relays", "relay", `{"transport":"relay","host":"smtp.example.test","port":587,"tls_mode":"starttls"}`},
	}
	for _, route := range routes {
		for _, existing := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/existing=%v", route.name, existing), func(t *testing.T) {
				db := policyRaceDB(t, route.table)
				domain := policyRaceDomain(t, db)
				if existing {
					var err error
					if route.table == "mtasts_policies" {
						err = db.Create(&models.MTASTSPolicy{DomainID: domain.ID, Mode: "testing", MXHosts: "old.example.test", MaxAge: 86400, Active: true}).Error
					} else {
						err = db.Create(&models.DomainRelay{DomainID: domain.ID, Transport: "direct"}).Error
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				deleted := make(chan struct{})
				saver := newPolicyRacer(deleted, policyRaceHold)
				save, saveDone := policyRaceCall(t, domainPolicyFaultRouter(db, nil), saver, http.MethodPut,
					fmt.Sprintf("/api/v1/admin/domains/%d/%s", domain.ID, route.suffix), route.body)
				waitPolicyRace(t, "the save reading the policy", saver.read)
				del, delDone := policyRaceCall(t, domainRoutes(NewDomainHandler(db, nil)), nil, http.MethodDelete,
					fmt.Sprintf("/api/v1/admin/domains/%d", domain.ID), "")
				go func() { <-delDone; close(deleted) }()
				waitPolicyRace(t, "the save", saveDone)
				waitPolicyRace(t, "the deletion", delDone)
				if del.Code != 204 {
					t.Fatalf("deletion answered %d %s", del.Code, del.Body)
				}
				if save.Code != 200 && save.Code != 201 && save.Code != 404 {
					t.Fatalf("save racing the deletion answered %d %s, want a success or 404", save.Code, save.Body)
				}
				var left int64
				if err := db.Table(route.table).Where("domain_id = ?", domain.ID).Count(&left).Error; err != nil {
					t.Fatal(err)
				}
				if left != 0 {
					t.Fatalf("%d %s row(s) left for the deleted domain", left, route.table)
				}
			})
		}
	}
}
