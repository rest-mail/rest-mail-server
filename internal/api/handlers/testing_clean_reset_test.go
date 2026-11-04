package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/bootstrap"
	"github.com/rest-mail/rest-mail-server/internal/config"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"github.com/rest-mail/rest-mail-server/internal/testreset"
	"gorm.io/gorm"
)

func cleanResetFixture(t *testing.T) (*gorm.DB, *TestHandler, models.Mailbox) {
	t.Helper()
	db := openAliasUpdateScratchDB(t)
	if err := rmdb.AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.EnsureRBAC(db); err != nil {
		t.Fatal(err)
	}
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
	// The retained domain gets a key and settings of its own, as a test would
	// give it: a re-seeded domain must not inherit them (#1385).
	if err := db.Model(&models.Domain{}).Where("id = ?", admin.DomainID).Updates(map[string]any{
		"dkim_selector": "before-reset", "dkim_private_key": "dkim:v1:key", "dkim_created_at": time.Now(),
		"dkim_rotation_interval_days": 30, "sending_policy": models.SendingPolicyBulk,
		"dmarc_policy": models.DMARCPolicyReject, "default_quota_bytes": 5,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(cleanResetData); err != nil {
		t.Fatal(err)
	}
	var domain models.Domain
	if err := db.First(&domain, admin.DomainID).Error; err != nil {
		t.Fatal(err)
	}
	if domain.DKIMSelector != "" || domain.DKIMPrivateKey != "" || domain.DKIMCreatedAt != nil || domain.DKIMRotationIntervalDays != 0 ||
		domain.SendingPolicy != "" || domain.DMARCPolicy != models.DMARCPolicyNone || domain.DefaultQuotaBytes != 1073741824 {
		t.Errorf("retained domain kept its settings: %+v", domain)
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

// Every column of a domain the reset keeps is either set back to a new
// domain's value or kept for a stated reason, so a setting added later cannot
// outlive a reset unnoticed.
func TestCleanResetEveryDomainColumn(t *testing.T) {
	db, _, _ := cleanResetFixture(t)
	var columns []string
	if err := db.Raw(`SELECT column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'domains'`).Scan(&columns).Error; err != nil {
		t.Fatal(err)
	}
	if len(columns) == 0 {
		t.Fatal("no domain columns")
	}
	for _, column := range columns {
		_, reset := resetDomainSettings[column]
		reason, kept := resetDomainKept[column]
		switch {
		case reset && kept:
			t.Errorf("domain column %s is both reset and kept", column)
		case kept && reason == "":
			t.Errorf("kept domain column %s has no reason", column)
		case !reset && !kept:
			t.Errorf("domain column %s is neither reset nor kept", column)
		}
	}
	for column := range resetDomainSettings {
		if !slices.Contains(columns, column) {
			t.Errorf("reset domain column %s does not exist", column)
		}
	}
}

func TestCleanResetEveryMailboxColumn(t *testing.T) {
	db, handler, admin := cleanResetFixture(t)
	if err := db.Model(&admin).Updates(map[string]any{
		"quiet_hours_enabled": true, "quiet_hours_start": "22:00", "quiet_hours_end": "07:00",
		"quiet_hours_time_zone": "UTC", "contact_reply_time_enabled": true, "quota_bytes": 5,
	}).Error; err != nil {
		t.Fatal(err)
	}
	resetFixture(t, handler)
	var kept models.Mailbox
	if err := db.First(&kept, admin.ID).Error; err != nil {
		t.Fatal(err)
	}
	if kept.QuietHoursEnabled || kept.QuietHoursStart != "" || kept.QuietHoursEnd != "" ||
		kept.QuietHoursTimeZone != "" || kept.ContactReplyTimeEnabled || kept.QuotaBytes != 1073741824 {
		t.Errorf("the retained administrator kept its settings across the reset: %+v", kept)
	}
	var columns []string
	if err := db.Raw(`SELECT column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'mailboxes'`).Scan(&columns).Error; err != nil {
		t.Fatal(err)
	}
	if len(columns) == 0 {
		t.Fatal("no mailbox columns")
	}
	for _, column := range columns {
		_, reset := resetMailboxSettings[column]
		reason, kept := resetMailboxKept[column]
		switch {
		case reset && kept:
			t.Errorf("mailbox column %s is both reset and kept", column)
		case kept && reason == "":
			t.Errorf("kept mailbox column %s has no reason", column)
		case !reset && !kept:
			t.Errorf("mailbox column %s is neither reset nor kept", column)
		}
	}
	for column := range resetMailboxSettings {
		if !slices.Contains(columns, column) {
			t.Errorf("reset mailbox column %s does not exist", column)
		}
	}
}

// A retained administrator keeps only the credentials it signs in with: one of
// every other kind, made before the reset, is gone after it, so a kind added
// later is cleared unless it is kept for a stated reason.
func TestCleanResetEveryCredentialKind(t *testing.T) {
	db, _, admin := cleanResetFixture(t)
	for kind, reason := range resetCredentialKindsKept {
		if !slices.Contains(models.CredentialKinds, kind) {
			t.Errorf("kept credential kind %s does not exist", kind)
		}
		if reason == "" {
			t.Errorf("kept credential kind %s has no reason", kind)
		}
	}
	if err := db.Where("account_id = ?", admin.ID).Delete(&models.Credential{}).Error; err != nil {
		t.Fatal(err)
	}
	for _, kind := range models.CredentialKinds {
		credential := models.Credential{AccountID: admin.ID, Kind: kind, PublicID: "before-reset-" + kind, Scope: models.CredentialScope{}}
		if err := db.Create(&credential).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Transaction(cleanResetData); err != nil {
		t.Fatal(err)
	}
	var kept []string
	if err := db.Model(&models.Credential{}).Where("account_id = ?", admin.ID).Order("kind").Pluck("kind", &kept).Error; err != nil {
		t.Fatal(err)
	}
	var want []string
	for kind := range resetCredentialKindsKept {
		want = append(want, kind)
	}
	slices.Sort(want)
	if !slices.Equal(kept, want) {
		t.Errorf("credentials kept across the reset = %v, want %v", kept, want)
	}
}

func TestCleanResetServerCeiling(t *testing.T) {
	db, handler, admin := cleanResetFixture(t)
	ceiling := func() []string {
		t.Helper()
		names, overridden, err := grants.ServerCeiling(db, time.Now())
		if err != nil || overridden {
			t.Fatalf("read the ceiling: overridden %v, %v", overridden, err)
		}
		return names
	}
	initial := ceiling()
	if len(initial) == 0 || slices.Contains(initial, grants.Wildcard) {
		t.Fatalf("a new installation's ceiling is %v", initial)
	}
	for _, narrowed := range [][]string{{"mail:read", "mail:send"}, {}} {
		entry := models.AuditEntry{ActorKind: models.AuditActorAccount, ActorID: &admin.ID}
		if err := db.Transaction(func(tx *gorm.DB) error {
			_, err := grants.SetServerCeiling(tx, narrowed, entry)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if got := ceiling(); !slices.Equal(got, narrowed) {
			t.Fatalf("narrowed ceiling %v, want %v", got, narrowed)
		}
		resetFixture(t, handler)
		if got := ceiling(); !slices.Equal(got, initial) {
			t.Fatalf("after a reset the ceiling narrowed to %v is %v, want every capability %v", narrowed, got, initial)
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
		broker.Publish(admin.ID, SSEEvent{Type: "old"})
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
	broker.Publish(admin.ID, SSEEvent{Type: "new"})
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
	claimed, finish, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() { done <- testreset.Delivery(db, func(*gorm.DB) { close(claimed); <-finish }) }()
	<-claimed
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	called := false
	err := testreset.Reset(ctx, db, func(*gorm.DB) error { called = true; return nil })
	if err == nil || called || !strings.Contains(err.Error(), "reset not performed") {
		t.Fatalf("drain hid timeout: %v, called=%v", err, called)
	}
	resetDone := make(chan error, 1)
	go func() {
		_, err := handler.resetTestData(httptest.NewRequest("POST", "/api/v1/admin/test/reset", nil), endpoint.Empty{})
		resetDone <- err
	}()
	select {
	case err := <-resetDone:
		t.Fatalf("reset returned while delivery was held: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-resetDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reset did not drain completed delivery")
	}
}
