package api

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

const vanishedUIDsPath = "/api/v1/accounts/{id}/folders/{folder}/vanished"

func TestVanishedUIDCapabilityExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	pair := pairs["listVanishedUIDs"]["capability_removed"]
	scenarios := accountListScenarios(t, func(_ string, keys map[string]accountListKey) driver.Connector {
		return accountCredentialListConnection{mode: "write_only", keys: keys}
	})
	for _, name := range []string{"write_only", "key_write_only"} {
		t.Run(name, func(t *testing.T) {
			examplepairs.Proven(t, "listVanishedUIDs", "capability_removed")
			scenario := scenarios[name]
			request := requirementRequest(requirementRoute{http.MethodGet, vanishedUIDsPath}, pair)
			request.Header.Set("Authorization", "Bearer "+scenario.token)
			response := httptest.NewRecorder()
			scenario.handler.ServeHTTP(response, request)
			assertRequirementResponse(t, pair, response)
		})
	}
}

// The mod-sequence pairs (RFC 7162, go-imap#162), sent through the production router to
// a scratch PostgreSQL database whose triggers keep the mod-sequences: a change made with
// unchanged_since after another change is refused, and the UIDs that left a folder are
// listed above a mod-sequence. Only the allocated account id is replaced with the
// published one.
func TestCondstoreStoredExamples(t *testing.T) {
	if os.Getenv("DB_HOST") == "" {
		t.Skip("set DB_HOST for PostgreSQL production-router mod-sequence proofs")
	}
	useRealGrants(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	seed := func(f *credentialFixture, id uint) {
		t.Helper()
		m := models.Message{ID: id, MailboxID: f.mailboxID, Folder: "INBOX", MsgID: "<message@example.net>",
			Sender: "sender@example.net", SenderName: "Sender", RecipientsTo: models.JSONB(`["recipient@example.com"]`),
			RecipientsCc: models.JSONB(`[]`), Subject: "Subject", SizeBytes: 80, ReceivedAt: stamp, CreatedAt: stamp, UpdatedAt: stamp}
		if err := f.db.Create(&m).Error; err != nil {
			t.Fatal(err)
		}
	}

	for _, name := range []string{"unchanged_since_negative", "unchanged_since_zero"} {
		t.Run("updateMessage/"+name, func(t *testing.T) {
			examplepairs.Proven(t, "updateMessage", name)
			pair := pairs["updateMessage"][name]
			fixture := newCredentialFixture(t)
			seed(fixture, 1)
			var before models.Message
			if err := fixture.db.First(&before, 1).Error; err != nil {
				t.Fatal(err)
			}
			response := fixture.do(http.MethodPatch, messageCollectionReadURI(messageUpdatePath, pair), fixture.session, string(pair.Request.Body))
			assertRequirementResponse(t, pair, response)
			var after models.Message
			if err := fixture.db.First(&after, 1).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("invalid mod-sequence changed the stored message: before=%+v after=%+v", before, after)
			}
		})
	}

	t.Run("updateMessage/stale_condition_without_a_change", func(t *testing.T) {
		fixture := newCredentialFixture(t)
		seed(fixture, 1)
		var before models.Message
		if err := fixture.db.First(&before, 1).Error; err != nil {
			t.Fatal(err)
		}
		response := fixture.do(http.MethodPatch, "/api/v1/messages/1", fixture.session, `{"unchanged_since":1}`)
		if response.Code != http.StatusOK {
			t.Fatalf("a request with no change: %d %s", response.Code, response.Body.String())
		}
		var after models.Message
		if err := fixture.db.First(&after, 1).Error; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("a request with no change wrote: before=%+v after=%+v", before, after)
		}
		response = fixture.do(http.MethodPatch, "/api/v1/messages/1", fixture.session, `{"is_read":true,"unchanged_since":1}`)
		if response.Code != http.StatusConflict {
			t.Fatalf("a stale conditional write: %d %s", response.Code, response.Body.String())
		}
		assertPublicExampleBody(t, pairs["updateMessage"]["modified"].Response.Body, response.Body.Bytes())
		if err := fixture.db.First(&after, 1).Error; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("a stale conditional write changed the message: before=%+v after=%+v", before, after)
		}
	})

	t.Run("updateMessage/modified", func(t *testing.T) {
		examplepairs.Proven(t, "updateMessage", "modified")
		pair := pairs["updateMessage"]["modified"]
		f := newCredentialFixture(t)
		seed(f, 1)
		// Another client flags the message: its mod_seq rises from 2, the first in the folder, to 3.
		if err := f.db.Model(&models.Message{}).Where("id = ?", 1).Update("is_flagged", true).Error; err != nil {
			t.Fatal(err)
		}
		rr := f.do(http.MethodPatch, messageCollectionReadURI(messageUpdatePath, pair), f.session, string(pair.Request.Body))
		if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
			t.Fatalf("got %d %s %s, want %d %s", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String(), pair.Response.Status, pair.Response.MediaType)
		}
		assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
		var stored models.Message
		if err := f.db.First(&stored, 1).Error; err != nil {
			t.Fatal(err)
		}
		if stored.IsRead || stored.ModSeq != 3 {
			t.Fatalf("refused change wrote: is_read %t, mod_seq %d; want false, 3", stored.IsRead, stored.ModSeq)
		}

		// At the current mod_seq the same change is made and answered with the new one.
		rr = f.do(http.MethodPatch, "/api/v1/messages/1", f.session, `{"is_read":true,"unchanged_since":3}`)
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"mod_seq":4`) {
			t.Fatalf("change at the current mod_seq: %d %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("listVanishedUIDs/found", func(t *testing.T) {
		examplepairs.Proven(t, "listVanishedUIDs", "found")
		pair := pairs["listVanishedUIDs"]["found"]
		f := newCredentialFixture(t)
		// UIDs 3, 5 and 7 each arrive in INBOX and leave it for good: the folder's
		// mod-sequence rises with every arrival and every departure.
		for _, id := range []uint{3, 5, 7} {
			seed(f, id)
			if err := f.db.Delete(&models.Message{}, id).Error; err != nil {
				t.Fatal(err)
			}
		}
		path := strings.Replace(vanishedUIDsPath, "{id}", strconv.FormatUint(uint64(f.accountID), 10), 1)
		inbox := strings.Replace(path, "{folder}", "INBOX", 1)
		rr := f.do(http.MethodGet, messageCollectionReadURI(path, pair), f.session, "")
		if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
			t.Fatalf("got %d %s %s, want %d %s", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String(), pair.Response.Status, pair.Response.MediaType)
		}
		assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())

		// The folder listing's HIGHESTMODSEQ counts the departures, though INBOX is empty.
		rr = f.do(http.MethodGet, "/api/v1/accounts/"+strconv.FormatUint(uint64(f.accountID), 10)+"/folders", f.session, "")
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"name":"INBOX","total":0,"unread":0,"role":"inbox","uid_next":8,"highest_mod_seq":7`) {
			t.Fatalf("folder listing after the departures: %d %s", rr.Code, rr.Body.String())
		}

		// Pages follow after_uid, and a message that comes back is no longer listed.
		rr = f.do(http.MethodGet, inbox+"?limit=1", f.session, "")
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"data":[{"uid":3,"mod_seq":3}]`) || !strings.Contains(rr.Body.String(), `"cursor":"3","has_more":true`) {
			t.Fatalf("first page: %d %s", rr.Code, rr.Body.String())
		}
		seed(f, 5)
		rr = f.do(http.MethodGet, inbox+"?after_uid=3", f.session, "")
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"data":[{"uid":7,"mod_seq":7}]`) {
			t.Fatalf("after UID 3, with UID 5 back: %d %s", rr.Code, rr.Body.String())
		}
	})
}

func TestUpdateMessageUnchangedSinceCoverageWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" || os.Getenv("DB_HOST") == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY and DB_HOST for the production-router coverage proof")
	}
	useRealGrants(t)
	fixture := newCredentialFixture(t)
	message := models.Message{ID: 1, MailboxID: fixture.mailboxID, Folder: "INBOX", Subject: "Subject", RecipientsTo: models.JSONB(`[]`), RecipientsCc: models.JSONB(`[]`)}
	if err := fixture.db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	document := contractDocument(t, describedRouters(t).pairGenerator(t, "unchanged-since coverage"))
	operation := document["paths"].(map[string]any)[messageUpdatePath].(map[string]any)["patch"].(map[string]any)
	responses := operation["responses"].(map[string]any)
	operation["responses"] = map[string]any{"200": responses["200"]}
	document["paths"] = map[string]any{messageUpdatePath: map[string]any{"patch": operation}}
	directory := t.TempDir()
	data, err := marshalPairDescription(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "openapi.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	server := servedAsDescribed(fixture.router)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "run", "--endpoint", server.URL, "--phases", "examples,coverage", "--fail-on-skipped-probes", "--header", "Authorization: "+fixture.session, "--reporter", "json", "--output", "report.json", "openapi.json")
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("updateMessage coverage failed: %v\n%s", err, output)
	}
	data, err = os.ReadFile(filepath.Join(directory, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Summary struct{ Total, Passing, Failing, Errors, Skipped int }
		Results []struct{ Name, Status string }
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.Summary.Total < 10 || report.Summary.Failing != 0 || report.Summary.Errors != 0 || report.Summary.Skipped != 0 {
		t.Fatalf("coverage did not execute without skips: %s", data)
	}
	for _, boundary := range []string{
		`"unchanged_since" at minimum (1)`,
		`"unchanged_since" at one below minimum (0)`,
	} {
		found := 0
		for _, result := range report.Results {
			if strings.HasPrefix(result.Name, "coverage:") && strings.HasSuffix(result.Name, boundary) {
				found++
				if result.Status != "pass" {
					t.Fatalf("boundary %s: %+v, want pass", boundary, result)
				}
			}
		}
		if found != 1 {
			t.Fatalf("boundary %s was sent %d times, want exactly once", boundary, found)
		}
	}
	t.Logf("updateMessage examples and coverage: %d/%d passed, no skips", report.Summary.Passing, report.Summary.Total)
}
