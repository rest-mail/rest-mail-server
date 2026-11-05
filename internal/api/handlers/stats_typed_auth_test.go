package handlers

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

func TestInboundTransportSecurity_OnlySavedTypedVerdicts(t *testing.T) {
	database := openTransportSecTestDB(t)
	transaction := database.Begin()
	if transaction.Error != nil {
		t.Fatal(transaction.Error)
	}
	t.Cleanup(func() { transaction.Rollback() })
	if err := transaction.Exec("DELETE FROM messages").Error; err != nil {
		t.Fatal(err)
	}
	plaintext := false
	for _, fixture := range []struct {
		name, results, raw string
		pass               bool
	}{
		{name: "forged receiver header", results: `[{"method":"spf","result":"fail"},{"method":"dkim","result":"fail"}]`, raw: "Authentication-Results: restmail; spf=pass; dkim=pass\r\n\r\nbody"},
		{name: "forged third party header", results: `[]`, raw: "Authentication-Results: attacker.test; dkim=pass\r\n\r\nbody"},
		{name: "body token", results: `[{"method":"spf","result":"softfail"}]`, raw: "Subject: hi\r\n\r\nspf=pass dkim=pass"},
		{name: "legacy header", raw: "Authentication-Results: restmail; spf=pass\r\n\r\nbody"},
		{name: "legacy null source"},
		{name: "empty results", results: `[]`},
		{name: "JSON null", results: `null`},
		{name: "unrelated methods", results: `[{"method":"arc","result":"pass"},{"method":"dmarc","result":"pass"}]`},
		{name: "split method and result", results: `[{"method":"spf","result":"fail"},{"method":"arc","result":"pass"}]`},
		{name: "exact pass only", results: `[{"method":"spf","result":"pass-forged"}]`},
		{name: "typed SPF without raw", results: `[{"method":"spf","result":"pass"}]`, pass: true},
		{name: "typed DKIM with failing raw", results: `[{"method":"dkim","result":"pass"}]`, raw: "Authentication-Results: restmail; dkim=fail\r\n\r\nbody", pass: true},
		{name: "one passing signature", results: `[{"method":"dkim","result":"fail"},{"method":"dkim","result":"pass"}]`, pass: true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			message := models.Message{MailboxID: 1, Sender: "sender@remote.test", ReceivedTLS: &plaintext, AuthResults: models.JSONB(fixture.results), RawMessage: fixture.raw}
			if err := transaction.Create(&message).Error; err != nil {
				t.Fatal(err)
			}
			if fixture.name == "legacy null source" {
				if err := transaction.Model(&message).Update("raw_message", nil).Error; err != nil {
					t.Fatal(err)
				}
			}
			counts, err := NewStatsHandler(transaction).getInboundTransportSecurity()
			if err != nil {
				t.Fatal(err)
			}
			wantPass := 0
			if fixture.pass {
				wantPass = 1
			}
			if counts.TotalInboundMX != 1 || counts.Plaintext != 1 || counts.PlaintextAuthPass != wantPass || counts.PlaintextAuthFail != 1-wantPass {
				t.Errorf("counts = %+v, want one plaintext delivery with pass=%d, fail=%d", counts, wantPass, 1-wantPass)
			}
			if err := transaction.Delete(&message).Error; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInboundTransportSecurity_QueryDoesNotReadMessageContent(t *testing.T) {
	for _, column := range []string{"raw_message", "raw_octets", "body_text", "body_html", "headers"} {
		if strings.Contains(inboundTransportSecuritySQL, column) {
			t.Errorf("dashboard aggregate reads %s", column)
		}
	}
}

func TestTypedAuthMigrationPreservesLegacyRows(t *testing.T) {
	database := openAliasUpdateScratchDB(t, &models.Message{})
	if err := database.Migrator().DropColumn(&models.Message{}, "AuthResults"); err != nil {
		t.Fatal(err)
	}
	const raw = "Authentication-Results: restmail; spf=pass\r\n\r\nspf=pass"
	if err := database.Exec("INSERT INTO messages (mailbox_id, sender, received_tls, raw_message) VALUES (1, 'sender@remote.test', false, ?)", raw).Error; err != nil {
		t.Fatal(err)
	}
	var before, after uint
	if err := database.Raw("SELECT pg_relation_filenode('messages')").Scan(&before).Error; err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := migrateModels(database, &models.Message{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Raw("SELECT pg_relation_filenode('messages')").Scan(&after).Error; err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("additive migration rewrote the table: filenode %d -> %d", before, after)
	}
	var untouched bool
	if err := database.Raw("SELECT auth_results IS NULL AND raw_message = ? FROM messages", raw).Scan(&untouched).Error; err != nil || !untouched {
		t.Fatalf("legacy source/verdicts changed: unchanged=%v, error=%v", untouched, err)
	}
	counts, err := NewStatsHandler(database).getInboundTransportSecurity()
	if err != nil || counts.PlaintextAuthPass != 0 || counts.PlaintextAuthFail != 1 {
		t.Fatalf("legacy counts = %+v, error=%v", counts, err)
	}
}

type statsAuthFilter struct {
	name    string
	results []pipeline.AuthResult
}

func (filter statsAuthFilter) Name() string       { return filter.name }
func (statsAuthFilter) Type() pipeline.FilterType { return pipeline.FilterTypeTransform }
func (filter statsAuthFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	if filter.name == "spf_check" {
		pipeline.AuthResultsFromContext(ctx).Add(filter.results...)
	}
	email.Headers.Raw["Authentication-Results"] = []string{"restmail; spf=pass; dkim=pass"}
	return &pipeline.FilterResult{Type: filter.Type(), Action: pipeline.ActionContinue, Message: email}, nil
}

func TestDeliveryPersistsTypedAuthForDashboard(t *testing.T) {
	for _, route := range []string{"smtp", "staged smtp", "restmail"} {
		for _, verdict := range []string{"fail", "pass"} {
			t.Run(route+"/"+verdict, func(t *testing.T) {
				handler, params, transaction, _ := stagedHandler(t, pipeline.ActionContinue)
				if err := migrateModels(transaction, &models.Alias{}, &models.OutboundQueue{}); err != nil {
					t.Fatal(err)
				}
				want := []pipeline.AuthResult{
					{Method: pipeline.AuthSPF, Result: verdict, Property: pipeline.PropMailFrom, Domain: "remote.test"},
					{Method: pipeline.AuthDKIM, Result: "fail", Property: pipeline.PropHeaderD, Domain: "remote.test"},
				}
				registry := withQuietVacation(pipeline.NewRegistry())
				for _, name := range []string{"spf_check", "rspamd"} {
					registry.Register(name, func([]byte) (pipeline.Filter, error) { return statsAuthFilter{name: name, results: want}, nil })
				}
				engine := pipeline.NewEngine(registry, nil)
				handler.engine = engine
				plaintext := false
				params.ReceivedTLS = &plaintext
				params.RawMessage = "Authentication-Results: restmail; spf=pass\r\nAuthentication-Results: attacker.test; dkim=pass\r\nFrom: sender@remote.test\r\nSubject: forged\r\n\r\nspf=pass dkim=pass\r\n"
				if route == "restmail" {
					restmail := NewRestmailHandler(transaction, engine, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{})
					delivered, failed := deliverRESTMAIL(t, restmail, params.Sender, params.RawMessage, params.Mailbox.Address)
					if len(delivered) != 1 || len(failed) != 0 {
						t.Fatalf("delivered=%v, failed=%v", delivered, failed)
					}
				} else {
					if route == "staged smtp" {
						params.Stage, params.State = deliverRequired, newDeliveryState()
						if _, err := handler.deliverToLocal(context.Background(), params); err != errContentStageNext {
							t.Fatalf("required stage: %v", err)
						}
						encoded, err := json.Marshal(params.State)
						if err != nil {
							t.Fatal(err)
						}
						params.State = newDeliveryState()
						if err := json.Unmarshal(encoded, params.State); err != nil {
							t.Fatal(err)
						}
						params.Stage = deliverContent
					}
					if _, err := handler.deliverToLocal(context.Background(), params); err != nil {
						t.Fatal(err)
					}
				}
				var stored models.Message
				if err := transaction.Where("mailbox_id = ?", params.Mailbox.ID).First(&stored).Error; err != nil {
					t.Fatal(err)
				}
				var got []pipeline.AuthResult
				if err := json.Unmarshal(stored.AuthResults, &got); err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("stored verdicts = %+v, want %+v, error=%v", got, want, err)
				}
			})
		}
	}
}
