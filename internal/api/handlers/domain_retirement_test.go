package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/bootstrap"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

func TestDomainRetirementKeepsItsEmptyPostmasterForRestore(test *testing.T) {
	for _, held := range []string{"empty", "mail", "trashed_mail", "password", "app_password", "api_key", "client_secret", "other_account", "alias", "manual_postmaster"} {
		test.Run(held, func(test *testing.T) {
			db := openPostmasterDB(test)
			if err := db.AutoMigrate(&models.CustomFilter{}, &models.DomainSenderRule{}, &models.DKIMRotationKey{}, &models.MTASTSPolicy{}, &models.DomainRelay{}, &models.Suppression{}); err != nil {
				test.Fatal(err)
			}
			if err := bootstrap.EnsureRBAC(db); err != nil {
				test.Fatal(err)
			}
			router := domainRoutes(NewDomainHandler(db, nil))
			response := postmasterRequest(test, router, nil, http.MethodPost, "/api/v1/admin/domains", `{"name":"retire.test"}`)
			if response.Code != 201 {
				test.Fatalf("create: %d %s", response.Code, response.Body)
			}
			var domain models.Domain
			if err := db.Where("name = ?", "retire.test").First(&domain).Error; err != nil {
				test.Fatal(err)
			}
			var account models.Mailbox
			if err := db.Where("domain_id = ?", domain.ID).First(&account).Error; err != nil {
				test.Fatal(err)
			}
			admin := models.Mailbox{DomainID: domain.ID, Address: "admin@other.test", LocalPart: "admin", Active: true}
			if err := db.Create(&admin).Error; err != nil {
				test.Fatal(err)
			}
			if err := db.Model(&admin).Update("domain_id", 0).Error; err != nil {
				test.Fatal(err)
			}
			var owner models.Role
			if err := db.Where("name = ?", models.RoleOwner).First(&owner).Error; err != nil {
				test.Fatal(err)
			}
			if err := db.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: admin.ID, RoleID: owner.ID, Scope: grants.All, Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem}).Error; err != nil {
				test.Fatal(err)
			}
			claims := freshTwoFactorClaims(admin.ID, admin.Address)
			switch held {
			case "mail", "trashed_mail":
				if err := db.Create(&models.Message{MailboxID: account.ID, Folder: "INBOX", RawMessage: "Subject: retained\r\n\r\nmail", IsDeleted: held == "trashed_mail"}).Error; err != nil {
					test.Fatal(err)
				}
			case "password", "app_password", "api_key", "client_secret":
				if err := db.Create(&models.Credential{AccountID: account.ID, Kind: held, PublicID: "retained", Name: "retained", SecretHash: "retained"}).Error; err != nil {
					test.Fatal(err)
				}
			case "other_account":
				if err := db.Create(&models.Mailbox{DomainID: domain.ID, Address: "user@retire.test", LocalPart: "user"}).Error; err != nil {
					test.Fatal(err)
				}
			case "alias":
				if err := db.Create(&models.Alias{DomainID: domain.ID, SourceAddress: "alias@retire.test", DestinationAddress: admin.Address, Active: true}).Error; err != nil {
					test.Fatal(err)
				}
			case "manual_postmaster":
				if err := db.Model(&account).Update("provisioned_postmaster", false).Error; err != nil {
					test.Fatal(err)
				}
			}
			if held == "empty" {
				response = postmasterRequest(test, mailboxRoutes(NewMailboxHandler(db, nil, nil)), claims, http.MethodDelete, fmt.Sprintf("/api/v1/admin/mailboxes/%d", account.ID), "")
				if response.Code != 409 || errorCodeOf(response) != "postmaster_required" {
					test.Fatalf("standalone delete: %d %s", response.Code, response.Body)
				}
			}
			path := fmt.Sprintf("/api/v1/admin/domains/%d", domain.ID)
			response = postmasterRequest(test, router, claims, http.MethodDelete, path, "")
			if held != "empty" {
				code := "domain_not_empty"
				if response.Code != 409 || errorCodeOf(response) != code {
					test.Fatalf("guard: %d %s", response.Code, response.Body)
				}
				var count int64
				if err := db.Model(&models.DomainRetirement{}).Count(&count).Error; err != nil || count != 0 {
					test.Fatalf("refusal archived state: %d %v", count, err)
				}
				if err := db.First(&models.Mailbox{}, account.ID).Error; err != nil {
					test.Fatal(err)
				}
				return
			}
			if response.Code != 204 {
				test.Fatalf("delete: %d %s", response.Code, response.Body)
			}
			response = postmasterRequest(test, router, claims, http.MethodGet, path, "")
			if response.Code != 404 {
				test.Fatalf("retired domain visible: %d %s", response.Code, response.Body)
			}
			var count int64
			if err := db.Model(&models.Mailbox{}).Where("id = ?", account.ID).Count(&count).Error; err != nil || count != 0 {
				test.Fatalf("retired postmaster visible: %d %v", count, err)
			}
			var archived models.DomainRetirement
			if err := db.First(&archived, "domain_id = ?", domain.ID).Error; err != nil {
				test.Fatal(err)
			}
			var snapshot map[string]json.RawMessage
			if err := json.Unmarshal(archived.State, &snapshot); err != nil || len(snapshot["mailboxes"]) <= 2 {
				test.Fatalf("postmaster not preserved: %s %v", archived.State, err)
			}
			response = postmasterRequest(test, router, claims, http.MethodPost, path+"/restore", "")
			if response.Code != 200 {
				test.Fatalf("restore: %d %s", response.Code, response.Body)
			}
			var restored models.Mailbox
			if err := db.First(&restored, account.ID).Error; err != nil || !reflect.DeepEqual(restored, account) {
				test.Fatalf("postmaster identity lost: %+v %v", restored, err)
			}
			if configured, err := postmasterConfigured(db, domain); err != nil || !configured {
				test.Fatalf("restored postmaster cannot receive: %t %v", configured, err)
			}
			var usage models.QuotaUsage
			if err := db.First(&usage, "mailbox_id = ?", account.ID).Error; err != nil || usage.MessageCount != 0 || usage.TotalBytes() != 0 {
				test.Fatalf("postmaster quota not restored: %+v %v", usage, err)
			}
			if err := db.Model(&models.Pipeline{}).Where("domain_id = ?", domain.ID).Count(&count).Error; err != nil || count != 2 {
				test.Fatalf("default pipelines not restored: %d %v", count, err)
			}
			if err := db.Model(&models.DomainRetirement{}).Count(&count).Error; err != nil || count != 0 {
				test.Fatalf("restore left archive: %d %v", count, err)
			}
		})
	}
}
