package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

const retiredDomainRefusal = `{"error":{"code":"deferred","message":"The recipient's domain is out of service, retry later"}}`

// retiredMail sends what a mail peer sends, through the production router on PostgreSQL.
type retiredMail struct {
	test   *testing.T
	router http.Handler
}

func (mail retiredMail) send(method, path, body string) *httptest.ResponseRecorder {
	mail.test.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:12345"
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	mail.router.ServeHTTP(response, request)
	return response
}

// wants checks both address checks: the gateway's and the one RESTMAIL peers call.
func (mail retiredMail) wants(address string, status int, exists bool) {
	mail.test.Helper()
	for _, path := range []string{"/api/mailboxes", "/restmail/mailboxes"} {
		response := mail.send(http.MethodGet, path+"?address="+url.QueryEscape(address), "")
		if response.Code != status {
			mail.test.Fatalf("%s %s = %d %s, want %d", path, address, response.Code, response.Body, status)
		}
		if status == http.StatusServiceUnavailable {
			if strings.TrimSpace(response.Body.String()) != retiredDomainRefusal {
				mail.test.Fatalf("%s %s answered %s", path, address, response.Body)
			}
			continue
		}
		var answer struct {
			Data struct {
				Exists bool `json:"exists"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
			mail.test.Fatal(err)
		}
		if answer.Data.Exists != exists {
			mail.test.Fatalf("%s %s exists = %v, want %v", path, address, answer.Data.Exists, exists)
		}
	}
}

func retiredDelivery(to ...string) string {
	encoded, _ := json.Marshal(map[string]any{"from": "someone@ext-contract.test", "to": to, "subject": "Waiting", "body_text": "Hello"})
	return string(encoded)
}

// gatewayDelivery is a whole message, so that the restored domain's inbound pipeline takes it.
func gatewayDelivery(address string) string {
	raw := "From: someone@ext-contract.test\r\nTo: " + address + "\r\nSubject: Waiting\r\n" +
		"Date: Sat, 17 Oct 2026 12:00:00 +0000\r\nMessage-ID: <waiting@ext-contract.test>\r\n\r\nHello\r\n"
	return fmt.Sprintf(`{"address":%q,"sender":"someone@ext-contract.test","subject":"Waiting","raw_message":%q}`,
		address, base64.StdEncoding.EncodeToString([]byte(raw)))
}

func storedMessages(test *testing.T, fixture *grantsFixture, mailboxID uint) int64 {
	test.Helper()
	var count int64
	if err := fixture.db.Model(&models.Message{}).Where("mailbox_id = ?", mailboxID).Count(&count).Error; err != nil {
		test.Fatal(err)
	}
	return count
}

// createLiveDomain creates a domain through the API and puts it in service.
func createLiveDomain(test *testing.T, fixture *grantsFixture, token, name string) models.Domain {
	test.Helper()
	response := doRequest(fixture.router, http.MethodPost, "/api/v1/admin/domains", token, fmt.Sprintf(`{"name":%q}`, name))
	if response.Code != http.StatusCreated {
		test.Fatalf("create %s: %d %s", name, response.Code, response.Body)
	}
	var domain models.Domain
	if err := fixture.db.Where("name = ?", name).First(&domain).Error; err != nil {
		test.Fatal(err)
	}
	if err := fixture.db.Model(&domain).Update("active", true).Error; err != nil {
		test.Fatal(err)
	}
	return domain
}

// The owner's decision on #1870: mail to a soft-deleted domain is deferred, not refused.
// Every way in is walked from a live domain, through retirement, to its restoration.
func TestMailToARetiredDomainWaitsUntilItIsRestored(test *testing.T) {
	useRealGrants(test)
	fixture := newGrantsFixture(test)
	owner := fixture.token(test, "owner")
	mail := retiredMail{test: test, router: fixture.router}
	domain := createLiveDomain(test, fixture, owner, "retired.test")
	postmaster := "postmaster@retired.test"
	var account models.Mailbox
	if err := fixture.db.Where("address = ?", postmaster).First(&account).Error; err != nil {
		test.Fatal(err)
	}
	createLiveDomain(test, fixture, owner, "live.test")
	liveAddress := "postmaster@live.test"
	var liveAccount models.Mailbox
	if err := fixture.db.Where("address = ?", liveAddress).First(&liveAccount).Error; err != nil {
		test.Fatal(err)
	}
	alias := models.Alias{DomainID: fixture.a.ID, SourceAddress: "forward@a.test", DestinationAddress: postmaster, Active: true}
	if err := fixture.db.Create(&alias).Error; err != nil {
		test.Fatal(err)
	}

	mail.wants(postmaster, http.StatusOK, true)
	mail.wants("nobody@retired.test", http.StatusOK, false)
	mail.wants("forward@a.test", http.StatusOK, true)

	if response := doRequest(fixture.router, http.MethodDelete, fmt.Sprintf("/api/v1/admin/domains/%d", domain.ID), owner, ""); response.Code != http.StatusNoContent {
		test.Fatalf("retire: %d %s", response.Code, response.Body)
	}

	for _, address := range []string{postmaster, "nobody@retired.test", "NOBODY+tag@RETIRED.test", "forward@a.test"} {
		mail.wants(address, http.StatusServiceUnavailable, false)
	}
	// Controls: only the retired domain waits.
	mail.wants("nobody@never-hosted.test", http.StatusOK, false)
	mail.wants("nobody@a.test", http.StatusOK, false)
	mail.wants(liveAddress, http.StatusOK, true)

	for _, address := range []string{postmaster, "forward@a.test"} {
		response := mail.send(http.MethodPost, "/api/v1/messages/deliver", gatewayDelivery(address))
		if response.Code != http.StatusServiceUnavailable || strings.TrimSpace(response.Body.String()) != retiredDomainRefusal {
			test.Fatalf("deliver to %s = %d %s", address, response.Code, response.Body)
		}
	}
	// One waiting recipient holds the delivery back, in either order, before anything is
	// stored: a sender told the live copy failed would send it again.
	for _, to := range [][]string{{postmaster}, {liveAddress, postmaster}, {postmaster, liveAddress}} {
		response := mail.send(http.MethodPost, "/restmail/messages", retiredDelivery(to...))
		if response.Code != http.StatusServiceUnavailable || strings.TrimSpace(response.Body.String()) != retiredDomainRefusal {
			test.Fatalf("restmail delivery to %v = %d %s", to, response.Code, response.Body)
		}
	}
	if stored := storedMessages(test, fixture, liveAccount.ID); stored != 0 {
		test.Fatalf("a deferred delivery stored %d messages for the live recipient", stored)
	}

	pairs, err := examplepairs.Read()
	if err != nil {
		test.Fatal(err)
	}
	routes := requirementRoutes(test)
	for _, operation := range []string{"checkMailboxAddress", "restmailCheckMailbox", "deliverMessage", "restmailDeliver"} {
		test.Run("example/"+operation, func(test *testing.T) {
			pair, ok := pairs[operation]["retired_domain"]
			if !ok {
				test.Fatal("the named pair is missing")
			}
			examplepairs.Proven(test, operation, "retired_domain")
			response := httptest.NewRecorder()
			fixture.router.ServeHTTP(response, requirementRequest(routes[operation], pair))
			assertRequirementResponse(test, pair, response)
		})
	}

	if response := doRequest(fixture.router, http.MethodPost, fmt.Sprintf("/api/v1/admin/domains/%d/restore", domain.ID), owner, ""); response.Code != http.StatusOK {
		test.Fatalf("restore: %d %s", response.Code, response.Body)
	}
	mail.wants(postmaster, http.StatusOK, true)
	mail.wants("nobody@retired.test", http.StatusOK, false)
	mail.wants("forward@a.test", http.StatusOK, true)
	if response := mail.send(http.MethodPost, "/api/v1/messages/deliver", gatewayDelivery(postmaster)); response.Code != http.StatusCreated {
		test.Fatalf("deliver after restore = %d %s", response.Code, response.Body)
	}
	if stored := storedMessages(test, fixture, account.ID); stored != 1 {
		test.Fatalf("restored postmaster holds %d messages, want 1", stored)
	}
}

// A new domain that takes a retired name answers for that name: its absent addresses are
// absent, not waiting for a restoration that would now be refused.
func TestALiveDomainAnswersForARetiredName(test *testing.T) {
	useRealGrants(test)
	fixture := newGrantsFixture(test)
	owner := fixture.token(test, "owner")
	mail := retiredMail{test: test, router: fixture.router}
	domain := createLiveDomain(test, fixture, owner, "reused.test")
	if response := doRequest(fixture.router, http.MethodDelete, fmt.Sprintf("/api/v1/admin/domains/%d", domain.ID), owner, ""); response.Code != http.StatusNoContent {
		test.Fatalf("retire: %d %s", response.Code, response.Body)
	}
	mail.wants("nobody@reused.test", http.StatusServiceUnavailable, false)
	createLiveDomain(test, fixture, owner, "reused.test")
	mail.wants("nobody@reused.test", http.StatusOK, false)
	mail.wants("postmaster@reused.test", http.StatusOK, true)
}
