package api

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

func TestBanCreationExamples(t *testing.T) {
	f := newBanCreationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs["createBan"]) < 30 {
		t.Fatal("incomplete ban creation pairs")
	}
	for name, pair := range pairs["createBan"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f[name]
			if !ok {
				t.Fatalf("missing scenario%s", name)
			}
			body := []byte(pair.Request.Body)
			if name == "malformed_json" {
				body = []byte("{")
			}
			if name == "request_too_large" {
				body = bytes.Repeat([]byte("x"), 513)
			}
			if name == "body_boundary" {
				if len(body) > 512 {
					t.Fatal("oversized boundary")
				}
				body = append(body, bytes.Repeat([]byte(" "), 512-len(body))...)
			}
			before := 0
			if s.store != nil {
				before = len(s.store.snapshot())
			}
			r := httptest.NewRequest(http.MethodPost, banCreationPath, bytes.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			if s.token != "" {
				r.Header.Set("Authorization", "Bearer "+s.token)
			}
			rr := httptest.NewRecorder()
			s.handler.ServeHTTP(rr, r)
			if rr.Code != pair.Response.Status {
				t.Fatalf("got%d %s want%d", rr.Code, rr.Body, pair.Response.Status)
			}
			assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			writes := 0
			if s.store != nil {
				for _, m := range s.store.snapshot()[before:] {
					if strings.HasPrefix(m.query, "SELECT") {
						continue
					}
					writes++
					var fields map[string]driver.Value
					var err error
					insert := strings.HasPrefix(m.query, "INSERT")
					if insert {
						fields, err = exampleInsertFields(m.query, m.args)
					} else {
						fields, err = vacationMutationFields(m.query, m.args)
					}
					if err != nil {
						t.Fatal(err)
					}
					if rr.Code < 300 {
						var answer struct {
							Data struct {
								ID        uint
								IP        string
								Reason    string
								Protocol  string
								CreatedBy string     `json:"created_by"`
								ExpiresAt *time.Time `json:"expires_at"`
								CreatedAt time.Time  `json:"created_at"`
							}
						}
						if err := json.Unmarshal(pair.Response.Body, &answer); err != nil {
							t.Fatal(err)
						}
						for k, want := range map[string]interface{}{"reason": answer.Data.Reason, "protocol": answer.Data.Protocol, "created_by": answer.Data.CreatedBy} {
							if fields[k] != want {
								t.Fatalf("stored%s=%v want%v", k, fields[k], want)
							}
						}
						if insert {
							if fields["ip"] != answer.Data.IP || fields["created_at"] != sieveInstallationTime {
								t.Fatal("insert changed address/time")
							}
						} else if fields["id"] != int64(answer.Data.ID) {
							t.Fatal("upsert changed primary key")
						}
						if answer.Data.ExpiresAt == nil {
							if fields["expires_at"] != nil {
								t.Fatal("permanent ban retained an expiry")
							}
						} else if got, ok := fields["expires_at"].(time.Time); !ok || !got.Equal(*answer.Data.ExpiresAt) {
							t.Fatalf("expiry binding%v want%v", fields["expires_at"], answer.Data.ExpiresAt)
						}
					}
				}
			}
			want := rr.Code < 300 || name == "insert_unavailable" || name == "update_unavailable"
			if (writes == 1) != want || writes > 1 {
				t.Fatalf("writes%d want%t", writes, want)
			}
		})
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if w.OperationID == "createBan" {
				t.Errorf("ban creation warning:%v", w)
			}
		}
	}
}

func TestBanCreationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for ban creation exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newBanCreationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["createBan"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f[name]
			if !ok {
				t.Fatalf("missing scenario%s", name)
			}
			checkQueueMutationPairWithVertrag(t, binary, s, http.MethodPost, banCreationPath, name, pair, 513)
		})
	}
}
