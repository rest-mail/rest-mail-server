package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	"gorm.io/gorm"
)

func draftAttachmentRequirementSetup(t *testing.T, name string, body []byte) (http.Handler, string, []byte) {
	t.Helper()
	f := newDraftUploadFixture(t, 8192)
	if name == "session_unavailable" {
		routers := NewRouters(f.db, f.jwt, f.cfg, nil)
		t.Cleanup(routers.Close)
		f.router = routers.Public
	}
	faults := 0
	const callback = "draft_attachment_requirement_fault"
	if err := f.db.Callback().Query().Before("gorm:query").Register(callback, func(query *gorm.DB) {
		if (name == "session_unavailable" && query.Statement.Table == "mailboxes") ||
			(name != "session_unavailable" && query.Statement.Table == "messages") {
			faults++
			_ = query.AddError(errors.New("private draft attachment database failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = f.db.Callback().Query().Remove(callback)
		if faults != 1 {
			t.Errorf("injected draft attachment database fault reached %d times, want 1", faults)
		}
	})
	return f.router, strings.TrimPrefix(f.session, "Bearer "), body
}

func TestDraftAttachmentNamedExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	recorded := oapifly.ExampleFile{}
	for _, operation := range []string{"uploadDraftAttachment", "listDraftAttachments", "deleteDraftAttachment"} {
		recorded[operation] = map[string]oapifly.ExamplePair{}
		cases := map[string]int{"success": 200, "unauthenticated": 401, "forbidden": 403, "missing": 404, "invalid_id": 422, "request_too_large": 413, "dependency_failed": 500, "session_unavailable": 503, "rate_limited": 429}
		if operation == "uploadDraftAttachment" {
			cases["success"], cases["unsupported_media"], cases["message_too_large"], cases["mailbox_full"] = 201, 400, 413, 422
		}
		if operation == "deleteDraftAttachment" {
			cases["success"] = 204
		}
		for name, status := range cases {
			t.Run(operation+"/"+name, func(t *testing.T) {
				f := newDraftUploadFixture(t, 8192)
				path, method, body := f.path(), http.MethodGet, ""
				pathValues := map[string]any{"id": 1}
				if operation != "uploadDraftAttachment" {
					row := uploadedAttachment(t, f.upload(t, strings.NewReader("attachment"), "contract.pdf"))
					if operation == "deleteDraftAttachment" {
						method, path = http.MethodDelete, fmt.Sprintf("%s/%d", path, row.ID)
						pathValues["attachmentId"] = 1
					}
				} else {
					method, body = http.MethodPost, "attachment"
				}
				if name == "missing" || name == "invalid_id" {
					target := "2147483647"
					pathValues["id"] = 2147483647
					if name == "invalid_id" {
						target, pathValues["id"] = "0", 0
					}
					path = strings.Replace(path, fmt.Sprintf("/draft/%d/", f.draft.ID), "/draft/"+target+"/", 1)
				}
				if name == "unauthenticated" {
					f.session = ""
				}
				if name == "forbidden" {
					token, public, hash, err := auth.NewAPIKey()
					if err != nil {
						t.Fatal(err)
					}
					credential := models.Credential{AccountID: f.mailboxID, Kind: models.CredentialKindAPIKey, PublicID: public, SecretHash: hash,
						Scope: models.CredentialScope{Permissions: []auth.ScopedPermission{{Capability: models.CapMailRead, Scope: "mailbox:" + f.address}}}}
					if err := f.db.Create(&credential).Error; err != nil {
						t.Fatal(err)
					}
					f.session = "Bearer " + token
				}
				if name == "mailbox_full" {
					if err := f.db.Model(&models.Mailbox{}).Where("id = ?", f.mailboxID).Update("quota_bytes", 1).Error; err != nil {
						t.Fatal(err)
					}
				}
				if name == "dependency_failed" || name == "session_unavailable" {
					if name == "session_unavailable" {
						routers := NewRouters(f.db, f.jwt, f.cfg, nil)
						t.Cleanup(routers.Close)
						f.router = routers.Public
					}
					callback := "draft_attachment_example_failure"
					if err := f.db.Callback().Query().Before("gorm:query").Register(callback, func(query *gorm.DB) {
						if query.Statement.Table == "messages" || (name == "session_unavailable" && query.Statement.Table == "mailboxes") {
							_ = query.AddError(errors.New("private attachment database failure"))
						}
					}); err != nil {
						t.Fatal(err)
					}
					defer func() { _ = f.db.Callback().Query().Remove(callback) }()
				}
				if name == "message_too_large" {
					body = strings.Repeat("x", 8193)
				}
				if name == "request_too_large" {
					size := 1025
					if method == http.MethodPost {
						size = 8193
					}
					body = strings.Repeat("x", size)
				}
				if name == "rate_limited" {
					cfg := *f.cfg
					cfg.AuthRateLimitEnabled, cfg.AuthRateLimitRPS, cfg.AuthRateLimitBurst = true, 1.0/3600, 1
					routers := NewRouters(f.db, f.jwt, &cfg, nil)
					t.Cleanup(routers.Close)
					f.router, f.session = routers.Public, "Bearer rmk_invalid"
				}
				req := httptest.NewRequest(method, path, strings.NewReader(body))
				req.Header.Set("Authorization", f.session)
				header := map[string]any{}
				if method == http.MethodPost {
					header["X-Attachment-Filename"], header["X-Attachment-Content-Type"] = "contract.pdf", "application/pdf"
					req.Header.Set("Content-Type", "application/octet-stream")
					for key, value := range header {
						req.Header.Set(key, value.(string))
					}
				}
				if name == "unsupported_media" {
					req.Header.Set("Content-Type", "application/json")
				}
				if name == "rate_limited" {
					probe := req.Clone(req.Context())
					probe.Body = http.NoBody
					f.router.ServeHTTP(httptest.NewRecorder(), probe)
				}
				rr := httptest.NewRecorder()
				f.router.ServeHTTP(rr, req)
				if rr.Code != status {
					t.Fatalf("%s/%s = %d %s, want %d", operation, name, rr.Code, rr.Body, status)
				}
				var response json.RawMessage
				if rr.Body.Len() > 0 {
					response = append(response, rr.Body.Bytes()...)
				}
				if name == "success" && method != http.MethodDelete {
					var payload map[string]any
					if err := json.Unmarshal(response, &payload); err != nil {
						t.Fatal(err)
					}
					normalize := func(row map[string]any) {
						row["id"], row["message_id"], row["created_at"] = 1, 1, "2030-01-02T03:04:05Z"
					}
					if method == http.MethodPost {
						normalize(payload["data"].(map[string]any))
					} else {
						normalize(payload["data"].([]any)[0].(map[string]any))
					}
					response, _ = json.Marshal(payload)
				}
				pair := oapifly.ExamplePair{Request: oapifly.ExampleRequest{Path: pathValues, Header: header}, Response: oapifly.ExampleResponse{Status: status, MediaType: rr.Header().Get("Content-Type"), Body: response}}
				pair.Requires = map[string]string{"state": "existing_draft"}
				if name == "dependency_failed" || name == "session_unavailable" {
					pair.Requires = map[string]string{"fault": "database"}
				}
				if method == http.MethodPost {
					pair.Request.Body, _ = json.Marshal(body)
					pair.Request.MediaType = req.Header.Get("Content-Type")
				}
				if name == "unsupported_media" {
					pair.Requires = map[string]string{"body": "unsupported_media"}
					pair.Request.MediaType = ""
					pair.Description = "The refusal test sends these bytes with Content-Type application/json, which is not a supported upload media type."
				}
				recorded[operation][name] = pair
				if os.Getenv("RESTMAIL_DRAFT_ATTACHMENTS_RECORD") == "" {
					want, ok := pairs[operation][name]
					if !ok {
						t.Fatal("missing named attachment pair")
					}
					if len(response) > 0 {
						assertPublicExampleBody(t, want.Response.Body, response)
					}
					examplepairs.Proven(t, operation, name)
				}
			})
		}
	}
	if output := os.Getenv("RESTMAIL_DRAFT_ATTACHMENTS_RECORD"); output != "" {
		data, err := oapifly.MarshalExamples(recorded)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(output, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
