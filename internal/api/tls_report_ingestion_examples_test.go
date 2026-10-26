package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
)

const tlsIngestionPath = "/.well-known/smtp-tlsrpt"

const tlsIngestionLimit = 512 << 10

// Only hosted-domain lookups and report inserts are supported. No credential,
// session, grant or mailbox lookup is needed on this public receiver.
type tlsIngestionConnection struct {
	testDataMutationConnection
	lookups, inserts  int
	cancelAfterInsert context.CancelFunc
}

func (c *tlsIngestionConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c *tlsIngestionConnection) Driver() driver.Driver { return c }

func (c *tlsIngestionConnection) Open(string) (driver.Conn, error) { return c, nil }

func (c *tlsIngestionConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(q, `SELECT * FROM "domains"`) {
		c.relevant(ctx, q, args)
		c.lookups++
		if c.mode == "lookup_failure" || c.mode == "late_lookup_failure" && c.lookups == 2 {
			return nil, errors.New("private TLS domain lookup detail")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(args) != 2 || !strings.Contains(q, "name = $1") || !strings.Contains(q, "LIMIT $2") {
			return nil, errors.New("unexpected TLS domain selector")
		}
		name, ok := args[0].Value.(string)
		if !ok {
			return nil, errors.New("TLS domain selector is not a string")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "name"}}
		if id := map[string]int64{"restmail.test": 42, "example.com": 43}[name]; id != 0 {
			r.rows = [][]driver.Value{{id, name}}
		}
		return r, nil
	}
	if strings.HasPrefix(q, `INSERT INTO "tls_reports"`) {
		c.relevant(ctx, q, args)
		c.inserts++
		if c.mode == "insert_failure" || c.mode == "late_insert_failure" && c.inserts == 2 {
			return nil, errors.New("private TLS report insertion detail")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if c.cancelAfterInsert != nil {
			c.cancelAfterInsert()
			c.cancelAfterInsert = nil
		}
		return &authReadSnapshotRows{columns: []string{"id"}, rows: [][]driver.Value{{int64(c.inserts)}}}, nil
	}
	return nil, errors.New("unexpected query on public TLS report route")
}

func tlsIngestionScenario(t *testing.T, name string) bulkQueueExampleScenario {
	t.Helper()
	store := &bulkQueueExampleStore{}
	c := &tlsIngestionConnection{testDataMutationConnection: testDataMutationConnection{domainMutationConnection: domainMutationConnection{store: store}, mode: name}}
	pool := sql.OpenDB(c)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	db := openAccountReadDB(t, pool)
	jwt := auth.NewJWTService("tls-ingestion-fixture-secret", time.Hour, time.Hour)
	routers := NewRouters(db, jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 1 << 20,
		TLSRPTRateLimitEnabled: name == "rate_limited", TLSRPTRateLimitRPS: 1.0 / 3600, TLSRPTRateLimitBurst: 1}, nil)
	t.Cleanup(routers.Close)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(context.WithValue(r.Context(), testDataMutationContextKey{}, true))
		defer cancel()
		if name == "cancelled" {
			c.cancel = cancel
		}
		if name == "cancelled_after_first" {
			c.cancelAfterInsert = cancel
		}
		if name == "rate_limited" {
			prime := httptest.NewRequest(http.MethodPost, tlsIngestionPath, strings.NewReader("[]"))
			prime.RemoteAddr = r.RemoteAddr
			prime.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			routers.Public.ServeHTTP(rr, prime)
			if rr.Code != 400 {
				t.Errorf("throttle primer got %d", rr.Code)
			}
		}
		routers.Public.ServeHTTP(w, r.WithContext(ctx))
	})
	return bulkQueueExampleScenario{accountReadScenario: accountReadScenario{handler: h}, store: store}
}

func tlsIngestionWire(name string, pair oapifly.ExamplePair) ([]byte, string) {
	body := append([]byte(nil), pair.Request.Body...)
	media := pair.Request.MediaType
	if media == "" {
		media = "application/json"
	}
	if name == "request_too_large" || name == "body_boundary" {
		n := tlsIngestionLimit
		if name == "request_too_large" {
			n++
		}
		body = append(body, bytes.Repeat([]byte(" "), n-len(body))...)
	}
	return body, media
}

func tlsIngestionRequest(s bulkQueueExampleScenario, body []byte, media string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, tlsIngestionPath, bytes.NewReader(body))
	r.Header.Set("Content-Type", media)
	r.RemoteAddr = "192.0.2.51:12345"
	rr := httptest.NewRecorder()
	s.handler.ServeHTTP(rr, r)
	return rr
}

func tlsIngestionRolledBack(calls []bulkQueueExampleMutation) bool {
	for _, call := range calls {
		if call.query == "ROLLBACK" {
			return true
		}
	}
	return false
}

func assertTLSIngestionSQL(t *testing.T, s bulkQueueExampleScenario, name string, body []byte, status int, start, end time.Time) {
	t.Helper()
	calls := s.store.snapshot()
	// database/sql rolls a cancelled transaction back from its own goroutine,
	// which can finish after the handler has answered: wait for that ROLLBACK.
	if strings.HasPrefix(name, "cancelled") {
		for deadline := time.Now().Add(2 * time.Second); !tlsIngestionRolledBack(calls) && time.Now().Before(deadline); {
			time.Sleep(5 * time.Millisecond)
			calls = s.store.snapshot()
		}
	}
	var begin, commit, rollback, failed int
	var inserted []map[string]driver.Value
	var domains []string
	for _, call := range calls {
		switch call.query {
		case "BEGIN":
			begin++
		case "COMMIT":
			commit++
		case "ROLLBACK":
			rollback++
		case "COMMIT_FAILED":
			failed++
		case "DATA_CONTEXT":
			if call.args[0].Value != true {
				t.Error("report SQL lost HTTP context")
			}
		default:
			if strings.HasPrefix(call.query, "SELECT") {
				domains = append(domains, call.args[0].Value.(string))
			}
			if strings.HasPrefix(call.query, "INSERT") {
				fields, err := exampleInsertFields(call.query, call.args)
				if err != nil {
					t.Fatal(err)
				}
				inserted = append(inserted, fields)
			}
		}
	}
	if len(domains) == 0 {
		if len(calls) != 0 {
			t.Fatalf("pre-storage refusal performed SQL: %v", calls)
		}
		return
	}
	if begin != 1 {
		t.Errorf("report transactions %d, want one", begin)
	}
	if status == 201 {
		if commit != 1 || rollback != 0 {
			t.Errorf("success commits %d rollbacks %d", commit, rollback)
		}
	} else if name == "commit_failure" {
		if failed != 1 || commit != 0 {
			t.Error("commit failure was hidden")
		}
	} else if commit != 0 || rollback != 1 {
		t.Errorf("failed report committed %d, rolled back %d", commit, rollback)
	}
	if name == "cancelled" && len(inserted) != 0 {
		t.Error("cancelled lookup continued to insertion")
	}
	if name == "cancelled_after_first" && len(inserted) != 1 {
		t.Error("cancelled report continued to its next insertion")
	}
	if name == "no_matching_domains" && len(inserted) != 0 {
		t.Error("unknown policy was stored")
	}
	if status != 201 {
		return
	}
	var report struct {
		Organization string            `json:"organization-name"`
		DateRange    map[string]string `json:"date-range"`
		Policies     []struct {
			Policy struct {
				Domain string `json:"policy-domain"`
				Type   string `json:"policy-type"`
			} `json:"policy"`
			Summary struct {
				Success int64 `json:"total-successful-session-count"`
				Failure int64 `json:"total-failure-session-count"`
			} `json:"summary"`
			Failure []map[string]interface{} `json:"failure-details"`
		} `json:"policies"`
	}
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatal(err)
	}
	i := 0
	for _, p := range report.Policies {
		domain := strings.TrimSuffix(p.Policy.Domain, ".")
		id := map[string]int64{"restmail.test": 42, "example.com": 43}[domain]
		if id == 0 {
			continue
		}
		if i >= len(inserted) {
			t.Fatal("known policy was not stored")
		}
		row := inserted[i]
		i++
		kind := p.Policy.Type
		if kind != "sts" && kind != "tlsa" {
			kind = "no-policy"
		}
		if row["domain_id"] != id || row["policy_domain"] != domain || row["policy_type"] != kind || row["reporting_org"] != report.Organization || row["total_successful"] != p.Summary.Success || row["total_failure"] != p.Summary.Failure || row["raw_report"] != string(body) {
			t.Fatal("stored report lost domain, policy, counts, organization or original bytes")
		}
		for column, key := range map[string]string{"start_date": "start-datetime", "end_date": "end-datetime"} {
			want, err := time.Parse(time.RFC3339, report.DateRange[key])
			stamp, ok := row[column].(time.Time)
			if err != nil || !ok || !stamp.Equal(want) {
				t.Errorf("stored %s changed", column)
			}
		}
		stamp, ok := row["received_at"].(time.Time)
		if !ok || stamp.Before(start) || stamp.After(end) {
			t.Error("received_at outside request")
		}
		if len(p.Failure) > 0 {
			want, _ := json.Marshal(p.Failure)
			got, ok := row["failure_details"].([]byte)
			if !ok {
				t.Fatalf("failure details type %T", row["failure_details"])
			}
			assertPublicExampleBody(t, want, got)
		}
	}
	if i != len(inserted) {
		t.Error("extra report rows stored")
	}
}

func TestTLSIngestionExamples(t *testing.T) {
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs["receiveTlsReport"]) < 15 {
		t.Fatal("TLS ingestion examples incomplete")
	}
	for name, pair := range pairs["receiveTlsReport"] {
		t.Run(name, func(t *testing.T) {
			s := tlsIngestionScenario(t, name)
			body, media := tlsIngestionWire(name, pair)
			start := time.Now()
			rr := tlsIngestionRequest(s, body, media)
			end := time.Now()
			if rr.Code != pair.Response.Status {
				t.Fatalf("got %d %s, want %d", rr.Code, rr.Body, pair.Response.Status)
			}
			assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			if name == "rate_limited" {
				n, err := strconv.Atoi(rr.Header().Get("Retry-After"))
				if err != nil || n < 1 || n > 3600 {
					t.Fatalf("invalid Retry-After %q", rr.Header().Get("Retry-After"))
				}
			}
			assertTLSIngestionSQL(t, s, name, body, rr.Code, start, end)
		})
	}
	for _, g := range gapGenerators(t) {
		for _, warning := range g.Report() {
			if warning.OperationID == "receiveTlsReport" {
				t.Errorf("TLS report warning: %v", warning)
			}
		}
	}
}

// RFC 8460 §5.4's binary gzip form, plus the receiver's existing encoded and
// sniffed forms, travel as actual bytes. Oversize cases inflate past the same cap.
func TestTLSIngestionGzipTransports(t *testing.T) {
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte(pairs["receiveTlsReport"]["stored"].Request.Body)
	for _, media := range []string{"application/tlsrpt+gzip", "application/tlsrpt+json", "application/json"} {
		for _, encoded := range []bool{false, true} {
			for _, oversized := range []bool{false, true} {
				t.Run(media+"/encoded="+strconv.FormatBool(encoded)+"/oversized="+strconv.FormatBool(oversized), func(t *testing.T) {
					body := append([]byte(nil), plain...)
					if oversized {
						body = append(body, bytes.Repeat([]byte(" "), tlsIngestionLimit+1-len(body))...)
					}
					var compressed bytes.Buffer
					zw := gzip.NewWriter(&compressed)
					if _, err := zw.Write(body); err != nil {
						t.Fatal(err)
					}
					if err := zw.Close(); err != nil {
						t.Fatal(err)
					}
					s := tlsIngestionScenario(t, "stored")
					r := httptest.NewRequest(http.MethodPost, tlsIngestionPath, bytes.NewReader(compressed.Bytes()))
					r.Header.Set("Content-Type", media)
					if encoded {
						r.Header.Set("Content-Encoding", "gzip")
					}
					rr := httptest.NewRecorder()
					start := time.Now()
					s.handler.ServeHTTP(rr, r)
					end := time.Now()
					want := 201
					if oversized {
						want = 413
					}
					if rr.Code != want {
						t.Fatalf("gzip got %d %s want %d", rr.Code, rr.Body, want)
					}
					if oversized {
						assertPublicExampleBody(t, []byte(`{"error":{"code":"request_too_large","message":"Request body too large"}}`), rr.Body.Bytes())
					} else {
						assertPublicExampleBody(t, pairs["receiveTlsReport"]["stored"].Response.Body, rr.Body.Bytes())
					}
					assertTLSIngestionSQL(t, s, "stored", body, rr.Code, start, end)
				})
			}
		}
	}
}

func TestTLSIngestionExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["receiveTlsReport"] {
		t.Run(name, func(t *testing.T) {
			doc := contractDocument(t, describedRouters(t).generator("TLS ingestion examples"))
			op := doc["paths"].(map[string]interface{})[tlsIngestionPath].(map[string]interface{})["post"].(map[string]interface{})
			doc["paths"] = map[string]interface{}{tlsIngestionPath: map[string]interface{}{"post": op}}
			content := op["requestBody"].(map[string]interface{})["content"].(map[string]interface{})
			media := content["application/json"].(map[string]interface{})
			examples := media["examples"].(map[string]interface{})
			selected, ok := examples[name]
			if name == "unsupported_media" {
				// Unsupported media cannot be an accepted request-body schema. Use
				// a compiler carrier, then send the pair's actual empty text/plain body.
				selected, ok = examples["stored"]
			}
			if !ok {
				t.Fatal("missing published request")
			}
			media["examples"] = map[string]interface{}{name: selected}
			op["requestBody"].(map[string]interface{})["content"] = map[string]interface{}{"application/json": media}
			status := strconv.Itoa(pair.Response.Status)
			response := op["responses"].(map[string]interface{})[status].(map[string]interface{})
			op["responses"] = map[string]interface{}{status: response}
			for _, v := range response["content"].(map[string]interface{}) {
				m := v.(map[string]interface{})
				e := m["examples"].(map[string]interface{})
				chosen, ok := e[name]
				if !ok {
					t.Fatal("missing published response")
				}
				m["examples"] = map[string]interface{}{name: chosen}
			}
			dir := t.TempDir()
			data, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "openapi.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			body, wireMedia := tlsIngestionWire(name, pair)
			hook := "import vertrag_hooks as hooks\n@hooks.before_each\ndef published_request(transaction):\n    transaction['fullPath'] = " + strconv.Quote(tlsIngestionPath) + "\n    transaction['request']['body'] = " + strconv.Quote(string(body)) + "\n    transaction['request'].setdefault('headers', {})['Content-Type'] = " + strconv.Quote(wireMedia) + "\n"
			if err := os.WriteFile(filepath.Join(dir, "pair.py"), []byte(hook), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "vertrag.yml"), []byte("language: python\nhookfiles: ./pair.py\n"), 0600); err != nil {
				t.Fatal(err)
			}
			s := tlsIngestionScenario(t, name)
			server := httptest.NewServer(s.handler)
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "run", "--endpoint", server.URL, "--reporter", "json", "--output", "report.json", "openapi.json")
			cmd.Dir = dir
			output, err := cmd.CombinedOutput()
			reportData, readErr := os.ReadFile(filepath.Join(dir, "report.json"))
			if err != nil || readErr != nil {
				t.Fatalf("Vertrag: %v, report: %v\n%s\n%s", err, readErr, output, reportData)
			}
			var report struct {
				Summary struct{ Total, Passing, Failing, Errors, Skipped int }
			}
			if err := json.Unmarshal(reportData, &report); err != nil {
				t.Fatal(err)
			}
			if report.Summary.Total != 1 || report.Summary.Passing != 1 || report.Summary.Failing != 0 || report.Summary.Errors != 0 || report.Summary.Skipped != 0 {
				t.Fatalf("exchange did not pass: %s", reportData)
			}
		})
	}
}
