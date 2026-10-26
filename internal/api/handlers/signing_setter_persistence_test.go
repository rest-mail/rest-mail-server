package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

const signingPersistenceMaster = "public-test-signing-persistence-master"

var signingPersistenceNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func signingPersistenceFixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../../tests/contract/fixtures/dkim-key.pem")
	if err != nil {
		t.Fatal("read public signing fixture")
	}
	return string(b)
}

// Use a dedicated scratch database; never write to the shared test database.
func TestSigningSetterKeyPersistence(t *testing.T) {
	db := openCustomFilterCreateScratchDB(t)
	if err := db.AutoMigrate(&models.Domain{}, &models.DKIMRotationKey{}); err != nil {
		t.Fatal(err)
	}
	db.NowFunc = func() time.Time { return signingPersistenceNow }
	key := signingPersistenceFixture(t)
	stored, err := models.EncryptDKIMPrivateKey(key, signingPersistenceMaster)
	if err != nil {
		t.Fatal("encrypt public fixture")
	}
	old := signingPersistenceNow.Add(-24 * time.Hour)
	for _, name := range []string{"install", "same_key", "legacy_age", "update_failure", "cancelled", "concurrent"} {
		t.Run(name, func(t *testing.T) {
			d := models.Domain{Name: strings.ReplaceAll(name, "_", "-") + ".test", ServerType: "traditional", DKIMSelector: "old", DKIMPrivateKey: stored, DKIMCreatedAt: &old, DKIMRotationIntervalDays: 180}
			if name == "install" {
				d.DKIMPrivateKey = ""
				d.DKIMCreatedAt = nil
			}
			if name == "legacy_age" {
				d.DKIMCreatedAt = nil
			}
			if err := db.Create(&d).Error; err != nil {
				t.Fatal(err)
			}
			shadow := models.DKIMRotationKey{DomainID: d.ID, Slot: models.DKIMRotationStaged, Selector: "staged", PrivateKey: stored, TTLSeconds: 60}
			if err := db.Create(&shadow).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.First(&shadow, shadow.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.First(&d, d.ID).Error; err != nil {
				t.Fatal(err)
			}
			original := d
			body, err := json.Marshal(map[string]string{"selector": "contract", "private_key": key})
			if err != nil {
				t.Fatal("encode public fixture")
			}
			req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/admin/dkim/%d", d.ID), bytes.NewReader(body))
			ctx, cancel := context.WithCancel(req.Context())
			defer cancel()
			req = req.WithContext(ctx)
			if err := db.Callback().Query().After("gorm:after_query").Register("signing-key-cancel", func(tx *gorm.DB) {
				if name == "cancelled" && tx.Statement.Table == "domains" {
					cancel()
				}
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Update().Before("gorm:update").Register("signing-key-fault", func(tx *gorm.DB) {
				if tx.Statement.Table != "domains" {
					return
				}
				if name == "update_failure" {
					tx.AddError(errors.New("private update failure"))
				}
				if name == "concurrent" {
					tx.AddError(tx.Session(&gorm.Session{NewDB: true}).Exec("DELETE FROM domains WHERE id = ?", d.ID).Error)
				}
			}); err != nil {
				t.Fatal(err)
			}
			rr := httptest.NewRecorder()
			dkimRoutes(NewDKIMHandler(db, signingPersistenceMaster)).ServeHTTP(rr, req)
			if err := db.Callback().Query().Remove("signing-key-cancel"); err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Update().Remove("signing-key-fault"); err != nil {
				t.Fatal(err)
			}
			want := 200
			if name == "update_failure" || name == "cancelled" {
				want = 500
			}
			if name == "concurrent" {
				want = 404
			}
			if rr.Code != want {
				t.Fatalf("status%d want%d", rr.Code, want)
			}
			if strings.Contains(rr.Body.String(), key) || strings.Contains(rr.Body.String(), models.DKIMKeyCipherPrefix) || strings.Contains(rr.Body.String(), "private update") {
				t.Fatal("private signing material exposed")
			}
			var got models.Domain
			err = db.First(&got, d.ID).Error
			if name == "concurrent" {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					t.Fatal("concurrently removed domain reappeared")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var retained models.DKIMRotationKey
			if err := db.First(&retained, shadow.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(shadow, retained) || got.DKIMRotationIntervalDays != 180 || got.Name != original.Name {
				t.Fatal("unrelated signing configuration changed")
			}
			if want != 200 {
				if got.DKIMPrivateKey != original.DKIMPrivateKey || got.DKIMSelector != original.DKIMSelector || !reflect.DeepEqual(got.DKIMCreatedAt, original.DKIMCreatedAt) {
					t.Fatal("failed request persisted a key change")
				}
				return
			}
			plain, err := models.LoadDKIMPrivateKey(got.DKIMPrivateKey, signingPersistenceMaster)
			if err != nil || plain != key || !strings.HasPrefix(got.DKIMPrivateKey, models.DKIMKeyCipherPrefix) || got.DKIMSelector != "contract" {
				t.Fatal("installed encrypted key did not round-trip")
			}
			var response struct {
				Data dkimEntry `json:"data"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(response.Data.CreatedAt, got.DKIMCreatedAt) {
				t.Fatal("response timestamp differs from durable timestamp")
			}
			switch name {
			case "install":
				if got.DKIMCreatedAt == nil || !got.DKIMCreatedAt.Equal(signingPersistenceNow) || response.Data.AgeSeconds == nil || *response.Data.AgeSeconds != 0 {
					t.Fatal("new key age is not installation age")
				}
			case "same_key":
				if got.DKIMCreatedAt == nil || !got.DKIMCreatedAt.Equal(old) || response.Data.AgeSeconds == nil || *response.Data.AgeSeconds != 86400 {
					t.Fatal("resave reset key age")
				}
			case "legacy_age":
				if got.DKIMCreatedAt != nil || response.Data.AgeSeconds != nil {
					t.Fatal("resave invented legacy key age")
				}
			}
		})
	}
}

func TestSigningSetterRotationPersistence(t *testing.T) {
	db := openCustomFilterCreateScratchDB(t)
	if err := db.AutoMigrate(&models.Domain{}, &models.DKIMRotationKey{}); err != nil {
		t.Fatal(err)
	}
	key := signingPersistenceFixture(t)
	stored, err := models.EncryptDKIMPrivateKey(key, signingPersistenceMaster)
	if err != nil {
		t.Fatal("encrypt public fixture")
	}
	old := signingPersistenceNow.Add(-24 * time.Hour)
	for _, tc := range []struct {
		name         string
		days, status int
	}{{"configured", 90, 200}, {"disabled", 0, 200}, {"maximum", 3650, 200}, {"update_failure", 90, 500}, {"cancelled", 90, 500}, {"status_failure", 90, 500}} {
		t.Run(tc.name, func(t *testing.T) {
			d := models.Domain{Name: strings.ReplaceAll(tc.name, "_", "-") + ".test", ServerType: "traditional", DKIMSelector: "current", DKIMPrivateKey: stored, DKIMCreatedAt: &old, DKIMRotationIntervalDays: 180}
			if err := db.Create(&d).Error; err != nil {
				t.Fatal(err)
			}
			shadow := models.DKIMRotationKey{DomainID: d.ID, Slot: models.DKIMRotationStaged, Selector: "staged", PrivateKey: stored, TTLSeconds: 60}
			if err := db.Create(&shadow).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.First(&shadow, shadow.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.First(&d, d.ID).Error; err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/admin/dkim/%d/rotation", d.ID), strings.NewReader(fmt.Sprintf(`{"rotation_interval_days":%d}`, tc.days)))
			ctx, cancel := context.WithCancel(req.Context())
			defer cancel()
			req = req.WithContext(ctx)
			if err := db.Callback().Query().After("gorm:after_query").Register("signing-rotation-fault", func(tx *gorm.DB) {
				if tc.name == "cancelled" && tx.Statement.Table == "domains" {
					cancel()
				}
				if tc.name == "status_failure" && tx.Statement.Table == "dkim_rotation_keys" {
					tx.AddError(errors.New("private rotation status failure"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Update().Before("gorm:update").Register("signing-rotation-update", func(tx *gorm.DB) {
				if tc.name == "update_failure" && tx.Statement.Table == "domains" {
					tx.AddError(errors.New("private rotation update failure"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			rr := httptest.NewRecorder()
			dkimRoutes(NewDKIMHandler(db, signingPersistenceMaster)).ServeHTTP(rr, req)
			if err := db.Callback().Query().Remove("signing-rotation-fault"); err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Update().Remove("signing-rotation-update"); err != nil {
				t.Fatal(err)
			}
			if rr.Code != tc.status {
				t.Fatalf("status%d want%d", rr.Code, tc.status)
			}
			if strings.Contains(rr.Body.String(), key) || strings.Contains(rr.Body.String(), "private rotation") {
				t.Fatal("private signing material exposed")
			}
			var got models.Domain
			if err := db.First(&got, d.ID).Error; err != nil {
				t.Fatal(err)
			}
			wantDays := tc.days
			if tc.name == "update_failure" || tc.name == "cancelled" {
				wantDays = 180
			}
			// Status is read after the update; a status-read failure does not roll it back.
			if got.DKIMRotationIntervalDays != wantDays || got.DKIMPrivateKey != d.DKIMPrivateKey || got.DKIMSelector != d.DKIMSelector || !reflect.DeepEqual(got.DKIMCreatedAt, d.DKIMCreatedAt) {
				t.Fatal("rotation update changed signing material or persisted incorrect policy")
			}
			var retained models.DKIMRotationKey
			if err := db.First(&retained, shadow.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(retained, shadow) {
				t.Fatal("rotation setter changed staged key")
			}
			if tc.status == 200 {
				var response struct {
					Data dkimRotationSettings `json:"data"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if response.Data.DomainID != d.ID || response.Data.RotationIntervalDays != tc.days || response.Data.State != "awaiting_publication" || response.Data.Staged == nil || response.Data.Staged.Selector != "staged" || response.Data.Staged.RecordValue == "" {
					t.Fatal("rotation response does not describe durable configuration")
				}
			}
		})
	}
}
