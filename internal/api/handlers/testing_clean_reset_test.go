package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"github.com/rest-mail/rest-mail-server/internal/testreset"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func cleanResetFixture(t *testing.T) (*gorm.DB, *TestHandler, models.Mailbox) {
	t.Helper()
	db := openAliasUpdateScratchDB(t, withFullSchema, withRoles)
	domain := models.Domain{Name: "control.test", Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	admin := models.Mailbox{DomainID: domain.ID, Address: "admin@control.test", LocalPart: "admin", Active: true}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	var role models.Role
	if err := db.Where("name = ?", "superadmin").First(&role).Error; err != nil {
		t.Fatal(err)
	}
	grant := models.Grant{HolderKind: "account", HolderID: admin.ID, RoleID: role.ID, Scope: "*", Mode: "active", GrantedByKind: "system", GrantedAt: time.Now()}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	credential := models.Credential{AccountID: admin.ID, Kind: "password", PublicID: "retained-admin", SecretHash: "unchanged", Scope: models.CredentialScope{}}
	if err := db.Create(&credential).Error; err != nil {
		t.Fatal(err)
	}
	return db, NewTestHandler(db, &config.Config{Environment: "test"}), admin
}

func resetFixture(t *testing.T, handler *TestHandler) {
	t.Helper()
	if status, err := handler.resetTestData(httptest.NewRequest("POST", "/api/v1/admin/test/reset", nil), endpoint.Empty{}); err != nil || status.Status != "reset_complete" {
		t.Fatalf("reset status %q: %v", status.Status, err)
	}
	if _, err := handler.seedTestData(httptest.NewRequest("POST", "/api/v1/admin/test/seed", nil), endpoint.Empty{}); err != nil {
		t.Fatal(err)
	}
}

func TestCleanResetStableIdentitiesAndJWT(t *testing.T) {
	db, handler, admin := cleanResetFixture(t)
	resetFixture(t, handler)
	identities := func() map[string][]uint {
		t.Helper()
		out := map[string][]uint{}
		for _, table := range []string{"domains", "mailboxes", "aliases", "credentials", "grants"} {
			var ids []uint
			if err := db.Table(table).Order("id").Pluck("id", &ids).Error; err != nil {
				t.Fatal(err)
			}
			out[table] = ids
		}
		return out
	}
	before := identities()
	var eve models.Mailbox
	if err := db.Where("address = ?", "eve@restmail.test").First(&eve).Error; err != nil {
		t.Fatal(err)
	}
	jwt := auth.NewJWTService("clean-reset-secret", time.Hour, time.Hour)
	tokens, err := jwt.GenerateSession(eve.ID, eve.Address, eve.CredentialEpoch, []string{"pwd"}, false)
	if err != nil {
		t.Fatal(err)
	}
	for cycle := 0; cycle < 3; cycle++ {
		if err := db.Model(&eve).Updates(map[string]any{"active": false, "credential_epoch": 9}).Error; err != nil {
			t.Fatal(err)
		}
		transient := models.Mailbox{DomainID: admin.DomainID, Address: "temporary@control.test", LocalPart: "temporary", Active: true}
		if err := db.Create(&transient).Error; err != nil {
			t.Fatal(err)
		}
		var role models.Role
		db.Where("name = ?", "superadmin").First(&role)
		if _, err := grants.EnsureSystem(db, transient.ID, role.ID, "*", "active"); err != nil {
			t.Fatal(err)
		}
		resetFixture(t, handler)
		if after := identities(); !reflect.DeepEqual(before, after) {
			t.Fatalf("cycle %d changed fixture IDs: before %v, after %v", cycle, before, after)
		}
		claims, err := jwt.ValidateAccessToken(tokens.AccessToken)
		if err != nil {
			t.Fatal(err)
		}
		account, err := sessionAccount(db, claims.AccountID)
		if err != nil || !sessionCurrent(account, claims.CredentialEpoch, time.Now()) {
			t.Fatalf("pre-reset JWT no longer current: %v", err)
		}
		var restored models.Mailbox
		if err := db.First(&restored, claims.AccountID).Error; err != nil || restored.Address != eve.Address {
			t.Fatalf("JWT changed account identity: %s, %v", restored.Address, err)
		}
	}
}

func TestCleanResetEveryTable(t *testing.T) {
	db, handler, admin := cleanResetFixture(t)
	resetFixture(t, handler)
	if err := db.Exec(`CREATE TABLE future_reset_probe (id bigserial PRIMARY KEY, value text NOT NULL)`).Error; err != nil {
		t.Fatal(err)
	}
	tables, err := db.Migrator().GetTables()
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		if reason, retained := resetRetainedTables[table]; retained {
			if reason == "" {
				t.Fatalf("retained table %s has no reason", table)
			}
			continue
		}
		var columns []struct {
			ColumnName, DataType, UdtName string
			ColumnDefault                 *string
			IsNullable                    string
		}
		if err := db.Raw(`SELECT column_name, data_type, udt_name, column_default, is_nullable FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = ? ORDER BY ordinal_position`, table).Scan(&columns).Error; err != nil {
			t.Fatal(err)
		}
		var names, values []string
		for _, column := range columns {
			if column.ColumnDefault != nil || column.IsNullable == "YES" {
				continue
			}
			names = append(names, resetIdentifier(column.ColumnName))
			value := "'probe'"
			if column.UdtName == "inet" {
				value = "'127.0.0.1'"
			}
			switch column.DataType {
			case "integer", "bigint", "smallint", "numeric", "double precision":
				value = fmt.Sprint(admin.DomainID)
			case "boolean":
				value = "false"
			case "timestamp with time zone", "timestamp without time zone", "date":
				value = "CURRENT_TIMESTAMP"
			case "jsonb", "json":
				value = "'{}'"
			case "ARRAY":
				value = "'{}'"
			}
			values = append(values, value)
			if table == "dnsbl_listing_events" && column.ColumnName == "kind" {
				values[len(values)-1] = "'listed'"
			}
			if table == "dkim_rotation_keys" && column.ColumnName == "slot" {
				values[len(values)-1] = "'staged'"
			}
		}
		statement := "INSERT INTO " + resetIdentifier(table) + " DEFAULT VALUES"
		if len(names) > 0 {
			statement = "INSERT INTO " + resetIdentifier(table) + " (" + strings.Join(names, ",") + ") VALUES (" + strings.Join(values, ",") + ")"
		}
		if table == "quota_usage" {
			// The fixture's own reset gave the admin a row: give it usage to lose.
			statement += " ON CONFLICT (mailbox_id) DO UPDATE SET message_count = 3, body_bytes = 9"
		}
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("populate reset table %s: %v", table, err)
		}
	}
	if err := db.Transaction(cleanResetData); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		if reason, retained := resetRetainedTables[table]; retained {
			t.Logf("retained %s: %s", table, reason)
			continue
		}
		if table == "quota_usage" {
			continue // the retained admin's, at nothing: checked below
		}
		var count int64
		if err := db.Table(table).Count(&count).Error; err != nil || count != 0 {
			t.Errorf("reset left %d rows in %s: %v", count, table, err)
		}
	}
	var usage []models.QuotaUsage
	if err := db.Find(&usage).Error; err != nil {
		t.Fatal(err)
	}
	if len(usage) != 1 || usage[0].MailboxID != admin.ID || usage[0].MessageCount != 0 || usage[0].TotalBytes() != 0 {
		t.Errorf("retained usage = %+v, want the admin's alone, at nothing", usage)
	}
	for table, expected := range map[string]int64{"mailboxes": 1, "domains": 1, "credentials": 1} {
		var count int64
		if err := db.Table(table).Count(&count).Error; err != nil || count != expected {
			t.Errorf("retained %s rows = %d, want %d: %v", table, count, expected, err)
		}
	}
}

func TestCleanResetMemoryAndStreams(t *testing.T) {
	db, handler, admin := cleanResetFixture(t)
	sessions := NewSessionCache(db, time.Hour)
	authHandler := NewAuthHandler(db, auth.NewJWTService("reset-secret", time.Hour, time.Hour), "", sessions)
	authHandler.SetRecoverFailures(middleware.NewFailureBudget(middleware.RateLimitConfig{RPS: 0.001, Burst: 1}))
	recovery := NewRecoveryEmailHandler(db, authHandler, "https://example.test")
	broker := newSSEBroker(1)
	handler.ResetRuntime(broker, sessions, authHandler, recovery)
	throttle, reset := middleware.ResettableRateLimit(middleware.RateLimitConfig{RPS: 0.001, Burst: 1})
	handler.OnReset(reset)
	request := func() int {
		response := httptest.NewRecorder()
		throttle(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })).ServeHTTP(response, httptest.NewRequest("GET", "/", nil))
		return response.Code
	}
	if request() != 204 || request() != 429 {
		t.Fatal("rate limit was not exhausted")
	}
	authHandler.recoverFailures.Charge("eve")
	for cycle := 0; cycle < 5; cycle++ {
		recovery.requests.Charge("eve")
	}
	oldRelease, ok := broker.acquireStream(admin.ID)
	if !ok {
		t.Fatal("no initial stream slot")
	}
	old := broker.Subscribe(admin.ID)
	for cycle := 0; cycle < 70; cycle++ {
		broker.Publish(context.Background(), admin.ID, SSEEvent{Type: "old"})
	}
	if _, ok := broker.acquireStream(admin.ID); ok {
		t.Fatal("stream limit was not exhausted")
	}
	if _, err := sessions.Current(admin.ID, 0); err != nil {
		t.Fatal(err)
	}
	resetFixture(t, handler)
	if request() != 204 {
		t.Fatal("rate limit survived reset")
	}
	if ok, _ := authHandler.recoverFailures.Available("eve"); !ok {
		t.Fatal("recovery budget survived reset")
	}
	if ok, _ := recovery.requests.Available("eve"); !ok {
		t.Fatal("recovery email budget survived reset")
	}
	if len(sessions.entries) != 0 {
		t.Fatal("session cache survived reset")
	}
	if _, open := <-old; open {
		t.Fatal("old event stream survived reset")
	}
	newRelease, ok := broker.acquireStream(admin.ID)
	if !ok {
		t.Fatal("stream cap survived reset")
	}
	defer newRelease()
	oldRelease()
	broker.Unsubscribe(admin.ID, old)
	if _, ok := broker.acquireStream(admin.ID); ok {
		t.Fatal("old stream release stole the new stream's slot")
	}
	replay := broker.SubscribeWithReplay(admin.ID, 1)
	defer broker.Unsubscribe(admin.ID, replay)
	if len(replay) != 0 {
		t.Fatal("replay ring survived reset")
	}
	broker.Publish(context.Background(), admin.ID, SSEEvent{Type: "new"})
	if event := <-replay; event.ID != 1 || event.Event.Type != "new" {
		t.Fatalf("counter survived reset: %+v", event)
	}
}

func TestCleanResetProductionGuard(t *testing.T) {
	for _, environment := range []string{"production", "prod", " Prod-eu "} {
		handler := NewTestHandler(nil, &config.Config{Environment: environment})
		called := false
		handler.OnReset(func() { called = true })
		if len(handler.resetState) != 0 {
			t.Fatal("production registered a reset callback")
		}
		handler.resetState = []func(){func() { called = true }}
		if _, err := handler.resetTestData(httptest.NewRequest("POST", "/api/v1/admin/test/reset", nil), endpoint.Empty{}); err == nil || called {
			t.Fatal("production reached reset state or database")
		}
	}
}

func TestCleanResetDrainsAndTimesOut(t *testing.T) {
	db, handler, _ := cleanResetFixture(t)
	claimed, finish, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var deliveryErr error
	go func() {
		deliveryErr = testreset.Delivery(db, func(*gorm.DB) { close(claimed); <-finish })
		close(done)
	}()
	finishDelivery := sync.OnceFunc(func() {
		close(finish)
		<-done
		if deliveryErr != nil {
			t.Error(deliveryErr)
		}
	})
	defer finishDelivery()
	select {
	case <-claimed:
	case <-done:
		t.Fatalf("delivery did not acquire the barrier: %v", deliveryErr)
	}
	timeoutDB, err := gorm.Open(postgres.Open(db.Dialector.(*postgres.Dialector).Config.DSN+" lock_timeout=50ms"), &gorm.Config{Logger: db.Logger})
	if err != nil {
		t.Fatal(err)
	}
	timeoutPool, err := timeoutDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = timeoutPool.Close() }()
	called := false
	err = testreset.Reset(t.Context(), timeoutDB, func(*gorm.DB) error { called = true; return nil })
	var timeoutErr *pgconn.PgError
	if !errors.As(err, &timeoutErr) || timeoutErr.Code != "55P03" || called || !strings.Contains(err.Error(), "reset not performed") {
		t.Fatalf("drain hid timeout: %v, called=%v", err, called)
	}
	resetDone := make(chan struct{})
	var resetErr error
	go func() {
		_, resetErr = handler.resetTestData(httptest.NewRequest("POST", "/api/v1/admin/test/reset", nil), endpoint.Empty{})
		close(resetDone)
	}()
	defer func() {
		finishDelivery()
		<-resetDone
		if resetErr != nil {
			t.Error(resetErr)
		}
	}()
	for {
		var waiting bool
		if err := db.WithContext(t.Context()).Raw(`SELECT EXISTS (
			SELECT 1 FROM pg_locks
			WHERE locktype = 'advisory' AND NOT granted
			AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
		)`).Scan(&waiting).Error; err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-resetDone:
			t.Fatalf("reset returned while delivery was held: %v", resetErr)
		default:
		}
	}
	finishDelivery()
}
