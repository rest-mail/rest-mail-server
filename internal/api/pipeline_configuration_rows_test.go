package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rest-mail/go-arc/pkg/arc"
	"github.com/rest-mail/rest-mail-server/internal/attachmentstore/s3test"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"gorm.io/gorm"
)

func pipelineRowFixture(t *testing.T, env map[string]string, steps ...pipeline.FilterConfig) *credentialFixture {
	t.Helper()
	for _, key := range []string{
		"SCANNER_HMAC_SECRET", "SIEVE_REDIRECT_ALLOW_EXTERNAL", "SIEVE_REDIRECT_ALLOWED_DOMAINS", "TRUSTED_ARC_SEALERS",
		"ATTACHMENT_STORAGE_TYPE", "ATTACHMENT_S3_ENDPOINT", "ATTACHMENT_S3_REGION", "ATTACHMENT_S3_BUCKET",
		"ATTACHMENT_S3_ACCESS_KEY_ID", "ATTACHMENT_S3_SECRET_ACCESS_KEY", "ATTACHMENT_S3_FORCE_PATH_STYLE", "ATTACHMENT_S3_PREFIX",
		"MASTER_KEY", "INTERNAL_MTLS_ENABLED", "RESTMAIL_OWNER_KEYS", "RESTMAIL_STATUS_KEYS",
		"AUTH_RATE_LIMIT_ENABLED", "RESTMAIL_DELIVER_STRICT", "PIPELINE_FILTER_ERROR_ACTION",
		"RESTMAIL_APNS_KEY_FILE", "RESTMAIL_APNS_KEY_ID", "RESTMAIL_APNS_TEAM_ID", "RESTMAIL_APNS_TOPIC",
	} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("ENVIRONMENT", "test")
	t.Setenv("JWT_SECRET", routerTestSecret)
	t.Setenv("AUTH_RATE_LIMIT_ENABLED", "false")
	t.Setenv("RESTMAIL_DELIVER_TRUSTED_CIDRS", "127.0.0.0/8")
	t.Setenv("MESSAGE_STORAGE_DIR", t.TempDir())
	for key, value := range env {
		t.Setenv(key, value)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load deployment configuration: %v", err)
	}
	fixture := newCredentialFixtureWith(t, cfg)
	if err := fixture.db.Create(&models.QuotaUsage{MailboxID: fixture.mailboxID}).Error; err != nil {
		t.Fatal(err)
	}
	var mailbox models.Mailbox
	if err := fixture.db.First(&mailbox, fixture.mailboxID).Error; err != nil {
		t.Fatal(err)
	}
	if steps == nil {
		steps = []pipeline.FilterConfig{}
	}
	if err := fixture.db.Create(&models.Pipeline{
		DomainID: mailbox.DomainID, Direction: "inbound", Active: true, Filters: pipelineRowJSON(t, steps),
	}).Error; err != nil {
		t.Fatal(err)
	}
	return fixture
}

func pipelineRowJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func pipelineRowStep(t *testing.T, name string, kind pipeline.FilterType, settings any) pipeline.FilterConfig {
	t.Helper()
	return pipeline.FilterConfig{Name: name, Type: kind, Enabled: true, Config: pipelineRowJSON(t, settings)}
}

const pipelineRowRaw = "From: sender@sender.test\r\nTo: chris@example.test\r\nSubject: Configuration wiring\r\n" +
	"Date: Wed, 04 Nov 2026 12:00:00 +0000\r\nMessage-ID: <configuration@sender.test>\r\n\r\nconfiguration message\r\n"

func pipelineRowDeliver(t *testing.T, fixture *credentialFixture, raw string, status int, stored int64) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/restmail/messages", strings.NewReader(string(pipelineRowJSON(t, map[string]any{
		"from": "sender@sender.test", "to": []string{fixture.address}, "raw_message": raw,
	}))))
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	fixture.router.ServeHTTP(response, request)
	if response.Code != status {
		t.Fatalf("delivery = %d %s, want %d", response.Code, response.Body, status)
	}
	if status == http.StatusCreated {
		var result struct {
			Data struct {
				Delivered []string `json:"delivered"`
				Failed    []string `json:"failed"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Data.Delivered) != 1 || result.Data.Delivered[0] != fixture.address || len(result.Data.Failed) != 0 {
			t.Fatalf("delivery recipients = %s", response.Body)
		}
	} else {
		code := "rejected"
		if status == http.StatusServiceUnavailable {
			code = "deferred"
		} else if status == http.StatusUnprocessableEntity {
			code = "undeliverable"
		}
		if !strings.Contains(response.Body.String(), `"code":"`+code+`"`) {
			t.Fatalf("delivery did not reach the pipeline refusal: %s", response.Body)
		}
	}
	var count int64
	if err := fixture.db.Model(&models.Message{}).Where("mailbox_id = ?", fixture.mailboxID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != stored {
		t.Fatalf("stored messages = %d, want %d", count, stored)
	}
	return response
}

func TestPipelineConfigurationRowsScanners(t *testing.T) {
	for _, scanner := range []struct{ name, path, clean, reject string }{
		{"rspamd", "/checkv2", `{"action":"no action","score":0}`, `{"action":"reject","score":20}`},
		{"clamav", "/scan", `{"Status":"OK"}`, `{"Status":"FOUND","Description":"fixture-verdict"}`},
	} {
		t.Run(scanner.name, func(t *testing.T) {
			for _, row := range []struct {
				name, secret, signingKey string
				reject, unreachable, off bool
				status                   int
			}{
				{name: "authenticated", secret: "deployment-secret", signingKey: "deployment-secret", status: 201},
				{name: "forged", secret: "deployment-secret", signingKey: "other-secret", status: 503},
				{name: "unsigned", secret: "deployment-secret", status: 503},
				{name: "signed_rejection", secret: "deployment-secret", signingKey: "deployment-secret", reject: true, status: 403},
				{name: "unreachable", secret: "deployment-secret", unreachable: true, status: 503},
				{name: "no_secret", status: 201},
				{name: "no_secret_unreachable", unreachable: true, status: 503},
				{name: "not_in_pipeline", secret: "deployment-secret", off: true, status: 201},
			} {
				t.Run(row.name, func(t *testing.T) {
					var calls atomic.Int64
					server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
						if request.Method == http.MethodGet && request.URL.Path == "/ping" {
							writer.WriteHeader(http.StatusOK)
							return
						}
						calls.Add(1)
						body, err := io.ReadAll(request.Body)
						if err != nil || request.Method != http.MethodPost || request.URL.Path != scanner.path || !strings.Contains(string(body), "configuration message") {
							t.Errorf("scanner request = %s %s %q, error %v", request.Method, request.URL.Path, body, err)
						}
						verdict := scanner.clean
						if row.reject {
							verdict = scanner.reject
						}
						if row.signingKey != "" {
							signature := hmac.New(sha256.New, []byte(row.signingKey))
							_, _ = signature.Write([]byte(verdict))
							writer.Header().Set("X-Scanner-Signature", hex.EncodeToString(signature.Sum(nil)))
						}
						_, _ = io.WriteString(writer, verdict)
					}))
					t.Cleanup(server.Close)
					if row.unreachable {
						server.Close()
					}
					step := pipelineRowStep(t, scanner.name, pipeline.FilterTypeAction, map[string]any{"url": server.URL, "timeout_ms": 1000})
					step.Enabled = !row.off
					fixture := pipelineRowFixture(t, map[string]string{"SCANNER_HMAC_SECRET": row.secret}, step)
					stored := int64(0)
					if row.status == http.StatusCreated {
						stored = 1
					}
					pipelineRowDeliver(t, fixture, pipelineRowRaw, row.status, stored)
					wantCalls := int64(1)
					if row.off || row.unreachable {
						wantCalls = 0
					}
					if calls.Load() != wantCalls {
						t.Fatalf("scanner calls = %d, want %d", calls.Load(), wantCalls)
					}
				})
			}
		})
	}
}

func TestPipelineConfigurationRowsSieveRedirect(t *testing.T) {
	for _, row := range []struct {
		name, allow, domains, target string
		forward                      bool
	}{
		{name: "default_denies_external", target: "forward@outside.test"},
		{name: "explicit_deny", allow: "false", target: "forward@outside.test"},
		{name: "allow_external", allow: "true", target: "forward@outside.test", forward: true},
		{name: "listed_domain", domains: "other.test, OUTSIDE.TEST", target: "forward@outside.test", forward: true},
		{name: "unlisted_domain", domains: "other.test", target: "forward@outside.test"},
		{name: "local_domain", target: "forward@example.test", forward: true},
	} {
		t.Run(row.name, func(t *testing.T) {
			fixture := pipelineRowFixture(t, map[string]string{
				"SIEVE_REDIRECT_ALLOW_EXTERNAL": row.allow, "SIEVE_REDIRECT_ALLOWED_DOMAINS": row.domains,
			}, pipelineRowStep(t, "sieve", pipeline.FilterTypeTransform, map[string]string{"script": fmt.Sprintf("redirect %q;", row.target)}))
			stored, queued := int64(1), int64(0)
			if row.forward {
				stored, queued = 0, 1
			}
			pipelineRowDeliver(t, fixture, pipelineRowRaw, http.StatusCreated, stored)
			var forwards []models.OutboundQueue
			if err := fixture.db.Find(&forwards).Error; err != nil {
				t.Fatal(err)
			}
			if int64(len(forwards)) != queued {
				t.Fatalf("queued redirects = %d, want %d", len(forwards), queued)
			}
			if row.forward && (forwards[0].Recipient != row.target || !strings.Contains(forwards[0].RawMessage, "configuration message")) {
				t.Fatalf("redirect did not preserve target and message: %+v", forwards[0])
			}
		})
	}
}

func TestPipelineConfigurationRowsARCSealers(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	serveTXT(t, map[string][]string{
		"sel._domainkey.forwarder.test": {"v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(publicKey)},
		"_dmarc.sender.test":            {"v=DMARC1; p=reject"},
	})
	sealed, err := arc.Seal(t.Context(), []byte(pipelineRowRaw), arc.SealOptions{
		Domain: "forwarder.test", Selector: "sel", PrivateKey: privateKey,
		AuthResults: "forwarder.test; dmarc=pass header.from=sender.test", ChainValidation: "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		name, trust, raw string
		status           int
	}{
		{"listed", "other.test, FORWARDER.TEST", string(sealed.Message), 201},
		{"no_trust", "", string(sealed.Message), 403},
		{"unlisted", "other.test", string(sealed.Message), 403},
		{"tampered", "forwarder.test", strings.ReplaceAll(string(sealed.Message), "configuration message", "tampered message"), 403},
		{"no_chain", "forwarder.test", pipelineRowRaw, 403},
	} {
		t.Run(row.name, func(t *testing.T) {
			fixture := pipelineRowFixture(t, map[string]string{"TRUSTED_ARC_SEALERS": row.trust},
				pipelineRowStep(t, "arc_verify", pipeline.FilterTypeTransform, map[string]any{}),
				pipelineRowStep(t, "dmarc_check", pipeline.FilterTypeAction, map[string]any{}))
			stored := int64(0)
			if row.status == http.StatusCreated {
				stored = 1
			}
			pipelineRowDeliver(t, fixture, row.raw, row.status, stored)
		})
	}
}

func TestPipelineConfigurationRowsAttachments(t *testing.T) {
	for _, storage := range []string{"s3", "filesystem"} {
		t.Run(storage, func(t *testing.T) {
			server := s3test.New(t)
			env := map[string]string{}
			if storage == "s3" {
				env = map[string]string{
					"ATTACHMENT_STORAGE_TYPE": "s3", "ATTACHMENT_S3_ENDPOINT": server.Config.S3Endpoint,
					"ATTACHMENT_S3_REGION": server.Config.S3Region, "ATTACHMENT_S3_BUCKET": server.Config.S3Bucket,
					"ATTACHMENT_S3_ACCESS_KEY_ID": server.Config.S3AccessKeyID, "ATTACHMENT_S3_SECRET_ACCESS_KEY": server.Config.S3SecretAccessKey,
					"ATTACHMENT_S3_FORCE_PATH_STYLE": "true", "ATTACHMENT_S3_PREFIX": "attachments/configuration/",
				}
			}
			fixture := pipelineRowFixture(t, env, pipelineRowStep(t, "extract_attachments", pipeline.FilterTypeTransform,
				map[string]string{"storage_path": t.TempDir()}))
			fixture.own(fixture.mailboxID, fixture.address)
			const content = "configuration attachment bytes\r\n"
			raw := strings.Replace(pipelineRowRaw, "\r\n\r\nconfiguration message\r\n", "\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=matrix\r\n\r\n"+
				"--matrix\r\nContent-Type: text/plain\r\n\r\nconfiguration message\r\n"+
				"--matrix\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=fixture.bin\r\n"+
				"Content-Transfer-Encoding: base64\r\n\r\n"+base64.StdEncoding.EncodeToString([]byte(content))+"\r\n--matrix--\r\n", 1)
			if storage == "s3" {
				server.Failure.Store(http.StatusForbidden)
				pipelineRowDeliver(t, fixture, raw, http.StatusServiceUnavailable, 0)
				server.Failure.Store(0)
			}
			stopTraceRecorder(fixture.routers)
			const callback = "configuration-row-attachment-write-failure"
			if err := fixture.db.Callback().Create().Before("gorm:create").Register(callback, func(query *gorm.DB) {
				if query.Statement.Table == "attachments" {
					_ = query.AddError(errors.New("fixture attachment metadata failure"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			callbackActive := true
			t.Cleanup(func() {
				if callbackActive {
					_ = fixture.db.Callback().Create().Remove(callback)
				}
			})
			pipelineRowDeliver(t, fixture, raw, http.StatusUnprocessableEntity, 0)
			var mailbox models.Mailbox
			if err := fixture.db.First(&mailbox, fixture.mailboxID).Error; err != nil {
				t.Fatal(err)
			}
			var partialAttachments int64
			if err := fixture.db.Model(&models.Attachment{}).Count(&partialAttachments).Error; err != nil {
				t.Fatal(err)
			}
			if mailbox.QuotaUsedBytes != 0 || partialAttachments != 0 {
				t.Fatalf("metadata failure leaked quota or attachments: mailbox=%+v attachments=%d", mailbox, partialAttachments)
			}
			if err := fixture.db.Callback().Create().Remove(callback); err != nil {
				t.Fatal(err)
			}
			callbackActive = false
			pipelineRowDeliver(t, fixture, raw, http.StatusCreated, 1)
			var attachments []models.Attachment
			if err := fixture.db.Find(&attachments).Error; err != nil {
				t.Fatal(err)
			}
			if len(attachments) != 1 || attachments[0].StorageType != storage {
				t.Fatalf("attachments = %+v, want one %s attachment", attachments, storage)
			}
			attachment := attachments[0]
			var message models.Message
			if err := fixture.db.First(&message, attachment.MessageID).Error; err != nil {
				t.Fatal(err)
			}
			var quota models.QuotaUsage
			if err := fixture.db.First(&quota, fixture.mailboxID).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.First(&mailbox, fixture.mailboxID).Error; err != nil {
				t.Fatal(err)
			}
			if !message.HasAttachments || quota.AttachmentBytes != int64(len(content)) || mailbox.QuotaUsedBytes != int64(message.SizeBytes)+int64(len(content)) {
				t.Fatalf("message/attachment quota lost: message=%+v quota=%+v used=%d", message, quota, mailbox.QuotaUsedBytes)
			}
			checksum := sha256.Sum256([]byte(content))
			if attachment.Checksum != hex.EncodeToString(checksum[:]) || attachment.Filename != "fixture.bin" {
				t.Fatalf("attachment lost content identity: %+v", attachment)
			}
			if storage == "s3" {
				if !strings.HasPrefix(attachment.StorageRef, "attachments/configuration/") || server.Requests.Load() == 0 {
					t.Fatalf("attachment did not reach configured S3 prefix: %+v", attachment)
				}
			} else {
				data, err := os.ReadFile(attachment.StorageRef)
				if err != nil || string(data) != content || server.Requests.Load() != 0 {
					t.Fatalf("filesystem attachment = %q, error %v; S3 requests = %d", data, err, server.Requests.Load())
				}
			}
			if storage == "s3" {
				before := server.Requests.Load()
				response := fixture.do(http.MethodGet, fmt.Sprintf("/api/v1/attachments/%d", attachment.ID), fixture.session, "")
				if response.Code != http.StatusOK || response.Body.String() != content || server.Requests.Load() <= before {
					t.Fatalf("S3 attachment download = %d %q, requests = %d", response.Code, response.Body, server.Requests.Load()-before)
				}
			}
		})
	}
}

func TestPipelineConfigurationRowsS3RequiresDeploymentSettings(t *testing.T) {
	fixture := pipelineRowFixture(t, nil, pipelineRowStep(t, "extract_attachments", pipeline.FilterTypeTransform,
		map[string]string{"storage_type": "s3"}))
	pipelineRowDeliver(t, fixture, pipelineRowRaw, http.StatusServiceUnavailable, 0)
}
