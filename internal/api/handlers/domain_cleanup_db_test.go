package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/auth"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDomainCleanupPostgresLifecycleRetentionAndRollback(t *testing.T) {
	if os.Getenv("DB_HOST") == "" {
		t.Skip("PostgreSQL domain lifecycle proof runs in CI with DB_HOST; no local database is started")
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
	schema := fmt.Sprintf("domain_cleanup_%d", time.Now().UnixNano())
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
	pool.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, DisableForeignKeyConstraintWhenMigrating: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	liveModels := []any{&models.Pipeline{}, &models.CustomFilter{}, &models.DomainSenderRule{}, &models.Certificate{}, &models.DKIMRotationKey{}, &models.MTASTSPolicy{}, &models.DomainRelay{}, &models.Suppression{}}
	all := append([]any{&models.Domain{}, &models.Mailbox{}, &models.Alias{}, &models.Role{}, &models.Capability{}, &models.RoleCapability{}, &models.Grant{}, &models.GrantActivation{}, &models.Credential{}, &models.TLSReport{}, &models.MessageTrace{}, &models.DMARCAggregateRecord{}}, liveModels...)
	if err := db.AutoMigrate(all...); err != nil {
		t.Fatal(err)
	}
	if err := rmdb.MigrateAuditLog(db); err != nil {
		t.Fatal(err)
	}
	cache := grants.NewCache(db, time.Hour)
	h := NewDomainHandler(db, nil, cache)
	router := domainRoutes(h)
	call := func(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(context.WithValue(ctx, middleware.ClaimsKey, &auth.Claims{AccountID: 1, Address: "admin@other.test"}))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	create := func(name string) models.Domain {
		w := call(t.Context(), http.MethodPost, "/api/v1/admin/domains", `{"name":"`+name+`"}`)
		if w.Code != 201 {
			t.Fatalf("API create: %d %s", w.Code, w.Body)
		}
		var reply struct {
			Data Domain `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
		var row models.Domain
		if err := db.First(&row, reply.Data.ID).Error; err != nil {
			t.Fatal(err)
		}
		return row
	}
	other := create("other.test")
	target := create("example.test")
	admin := models.Mailbox{DomainID: other.ID, LocalPart: "admin", Address: "admin@other.test", Active: true}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	reader := models.Mailbox{DomainID: other.ID, LocalPart: "reader", Address: "reader@other.test", Active: true}
	if err := db.Create(&reader).Error; err != nil {
		t.Fatal(err)
	}
	role := models.Role{Name: "domain-reader"}
	if err := db.Create(&role).Error; err != nil {
		t.Fatal(err)
	}
	capability := models.Capability{Name: "domains:read"}
	if err := db.Create(&capability).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.RoleCapability{RoleID: role.ID, CapabilityID: capability.ID}).Error; err != nil {
		t.Fatal(err)
	}
	seedGrant := func(scope, mode string) models.Grant {
		g := models.Grant{HolderKind: models.GrantHolderAccount, HolderID: reader.ID, RoleID: role.ID, Scope: scope, Mode: mode, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}
		if err := db.Create(&g).Error; err != nil {
			t.Fatal(err)
		}
		return g
	}
	seedLive := func(d models.Domain) {
		key := models.DKIMRotationKey{DomainID: d.ID, Slot: models.DKIMRotationStaged, Selector: "future", TTLSeconds: 300}
		if err := key.SetPrivateKey("private fixture material", relayTestMasterKey); err != nil {
			t.Fatal(err)
		}
		password, err := models.EncryptRelayPassword("relay fixture password", relayTestMasterKey)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range []any{&models.CustomFilter{DomainID: d.ID, Name: "custom", FilterType: "transform", Direction: "inbound", Config: json.RawMessage(`{}`), Enabled: true}, &models.DomainSenderRule{DomainID: d.ID, Pattern: "sender@example.net", ListType: "allow"}, &models.Certificate{DomainID: d.ID, CertPEM: "fixture", KeyPEM: "encrypted fixture", NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}, &key, &models.MTASTSPolicy{DomainID: d.ID, Mode: "testing", MXHosts: "mx.example.test"}, &models.DomainRelay{DomainID: d.ID, Transport: "relay", Host: "relay.example.test", Port: 465, TLSMode: "tls", PasswordEnc: password}, &models.Suppression{DomainID: d.ID, Address: "bad@example.net", Reason: "bounce", ExpiresAt: time.Now().Add(time.Hour)}} {
			if err := db.Create(row).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	seedLive(target)
	seedLive(other)
	globalCert := models.Certificate{DomainID: 0, CertPEM: "global", KeyPEM: "global fixture", NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	if err := db.Create(&globalCert).Error; err != nil {
		t.Fatal(err)
	}
	seedGrant(grants.Domain(target.Name), models.GrantActive)
	eligible := seedGrant(grants.Mailbox("former@"+target.Name), models.GrantEligible)
	if err := db.Create(&models.GrantActivation{GrantID: eligible.ID, ActivatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), Reason: "fixture"}).Error; err != nil {
		t.Fatal(err)
	}
	foreign := seedGrant(grants.Domain(other.Name), models.GrantActive)
	oldReport := models.TLSReport{DomainID: target.ID, PolicyDomain: target.Name, ReportingOrg: "history", PolicyType: "sts", StartDate: time.Now().Add(-time.Hour), EndDate: time.Now(), ReceivedAt: time.Now(), RawReport: `{"historical":true}`}
	if err := db.Create(&oldReport).Error; err != nil {
		t.Fatal(err)
	}
	var pipeline models.Pipeline
	if err := db.Where("domain_id = ?", target.ID).First(&pipeline).Error; err != nil {
		t.Fatal(err)
	}
	trace := models.MessageTrace{PipelineID: pipeline.ID, Direction: "inbound", MailFrom: "sender@example.net", RcptTo: "former@" + target.Name, Outcome: "delivered"}
	if err := db.Create(&trace).Error; err != nil {
		t.Fatal(err)
	}
	aggregate := models.DMARCAggregateRecord{Domain: target.Name, HeaderFrom: target.Name, SourceIP: "192.0.2.1", Disposition: "none"}
	if err := db.Create(&aggregate).Error; err != nil {
		t.Fatal(err)
	}
	seedAudit := models.AuditEntry{ActorKind: models.AuditActorSystem, ActorName: "system", Action: "fixture.history", Outcome: models.AuditOutcomeSuccess}
	if err := audit.Append(db, &seedAudit); err != nil {
		t.Fatal(err)
	}
	held, err := cache.Held(reader.ID)
	if err != nil || !held.Allows("domains:read", grants.Domain(target.Name)) {
		t.Fatalf("warm permissions: %v", err)
	}
	w := call(t.Context(), "DELETE", fmt.Sprintf("/api/v1/admin/domains/%d", target.ID), "")
	if w.Code != 204 {
		t.Fatalf("API delete: %d %s", w.Code, w.Body)
	}
	for _, model := range liveModels {
		var n int64
		if err := db.Model(model).Where("domain_id = ?", target.ID).Count(&n).Error; err != nil || n != 0 {
			t.Fatalf("target live state remains (%T): count=%d err=%v", model, n, err)
		}
		if err := db.Model(model).Where("domain_id = ?", other.ID).Count(&n).Error; err != nil || n == 0 {
			t.Fatalf("other live state lost (%T): count=%d err=%v", model, n, err)
		}
	}
	for _, model := range []any{&globalCert, &oldReport, &trace, &aggregate, &seedAudit} {
		if err := db.First(model).Error; err != nil {
			t.Fatalf("historical/global row lost (%T): %v", model, err)
		}
	}
	var historical models.TLSReport
	if err := db.First(&historical, oldReport.ID).Error; err != nil || historical.DomainID != target.ID || historical.RawReport != oldReport.RawReport {
		t.Fatal("historical report identity/content changed")
	}
	var remaining []models.Grant
	if err := db.Find(&remaining).Error; err != nil || len(remaining) != 1 || remaining[0].ID != foreign.ID {
		t.Fatalf("grant isolation: rows=%d err=%v", len(remaining), err)
	}
	var activations int64
	if err := db.Model(&models.GrantActivation{}).Count(&activations).Error; err != nil || activations != 0 {
		t.Fatalf("eligible activation remains: %d %v", activations, err)
	}
	var auditRows []models.AuditEntry
	if err := db.Order("seq").Find(&auditRows).Error; err != nil {
		t.Fatal(err)
	}
	if result := audit.Verify(auditRows); !result.OK {
		t.Fatalf("audit chain: %+v", result)
	}
	var removed, deactivated int
	for _, entry := range auditRows {
		if entry.Action == grants.ActionDelete {
			removed++
		}
		if entry.Action == grants.ActionDeactivate {
			deactivated++
		}
	}
	if removed != 2 || deactivated != 1 {
		t.Fatalf("grant audit actions: deleted=%d deactivated=%d", removed, deactivated)
	}
	replacement := create(target.Name)
	if replacement.ID == target.ID {
		t.Fatal("domain ID reused")
	}
	held, err = cache.Held(reader.ID)
	if err != nil || held.Allows("domains:read", grants.Domain(replacement.Name)) {
		t.Fatal("recreated domain inherited cached grant")
	}
	for _, mode := range []string{"cleanup_failure", "cancel", "account", "alias"} {
		t.Run(mode, func(t *testing.T) {
			d := create(strings.ReplaceAll(mode, "_", "-") + ".test")
			seedLive(d)
			g := seedGrant(grants.Domain(d.Name), models.GrantActive)
			before := int64(0)
			if err := db.Model(&models.AuditEntry{}).Count(&before).Error; err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			status := 500
			if mode == "cleanup_failure" {
				if err := db.Exec(`CREATE FUNCTION refuse_domain_cleanup() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture cleanup failure'; END; $$`).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Exec(`CREATE TRIGGER refuse_domain_cleanup BEFORE DELETE ON domain_relays FOR EACH ROW EXECUTE FUNCTION refuse_domain_cleanup()`).Error; err != nil {
					t.Fatal(err)
				}
				defer func() {
					db.Exec(`DROP TRIGGER refuse_domain_cleanup ON domain_relays`)
					db.Exec(`DROP FUNCTION refuse_domain_cleanup()`)
				}()
			}
			if mode == "cancel" {
				name := "fixture:cancel_cleanup"
				if err := db.Callback().Delete().Before("gorm:delete").Register(name, func(tx *gorm.DB) {
					if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "mtasts_policies" {
						cancel()
					}
				}); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = db.Callback().Delete().Remove(name) }()
			}
			if mode == "account" {
				status = 409
				if err := db.Create(&models.Mailbox{DomainID: d.ID, LocalPart: "held", Address: "held@" + d.Name}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if mode == "alias" {
				status = 409
				if err := db.Create(&models.Alias{DomainID: d.ID, SourceAddress: "alias@" + d.Name, DestinationAddress: admin.Address}).Error; err != nil {
					t.Fatal(err)
				}
			}
			w := call(ctx, "DELETE", fmt.Sprintf("/api/v1/admin/domains/%d", d.ID), "")
			if w.Code != status {
				t.Fatalf("delete refusal: %d %s", w.Code, w.Body)
			}
			if err := db.First(&models.Domain{}, d.ID).Error; err != nil {
				t.Fatal("domain escaped rollback")
			}
			if err := db.First(&models.Grant{}, g.ID).Error; err != nil {
				t.Fatal("grant escaped rollback")
			}
			for _, model := range liveModels {
				var n int64
				if err := db.Model(model).Where("domain_id = ?", d.ID).Count(&n).Error; err != nil || n == 0 {
					t.Fatalf("live state escaped rollback (%T): %d %v", model, n, err)
				}
			}
			var after int64
			if err := db.Model(&models.AuditEntry{}).Count(&after).Error; err != nil || after != before {
				t.Fatalf("audit escaped rollback: before=%d after=%d err=%v", before, after, err)
			}
		})
	}
}
