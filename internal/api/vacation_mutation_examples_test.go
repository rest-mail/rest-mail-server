package api

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

const vacationMutationPath = "/api/v1/accounts/{id}/vacation"

type vacationDisableFailure struct{}
type vacationMutationConnection struct{ mailRemovalConnection }

func (c vacationMutationConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c vacationMutationConnection) Driver() driver.Driver                        { return c }
func (c vacationMutationConnection) Open(string) (driver.Conn, error)             { return c, nil }

func vacationMutationFields(q string, args []driver.NamedValue) (map[string]driver.Value, error) {
	fields := map[string]driver.Value{}
	if strings.HasPrefix(q, `INSERT INTO "vacation_configs"`) {
		start, end := strings.Index(q, "("), strings.Index(q, ") VALUES")
		if start < 0 || end < start {
			return nil, errors.New("unexpected vacation insert layout")
		}
		cols := strings.Split(q[start+1:end], ",")
		if len(cols) != len(args) {
			return nil, errors.New("vacation insert field count changed")
		}
		for i, col := range cols {
			fields[strings.Trim(col, " \"\n\t")] = args[i].Value
		}
	} else {
		for _, match := range sieveUpdateBinding.FindAllStringSubmatch(q, -1) {
			i, err := strconv.Atoi(match[2])
			if err != nil || i < 1 || i > len(args) {
				return nil, errors.New("invalid vacation update binding")
			}
			fields[match[1]] = args[i-1].Value
		}
	}
	return fields, nil
}

func (c vacationMutationConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(q, `INSERT INTO "vacation_configs"`) {
		c.record(q, args)
		fields, err := vacationMutationFields(q, args)
		if err != nil {
			return nil, err
		}
		if fields["subject"] == "create unavailable" {
			return nil, errors.New("private vacation insert detail")
		}
		return &authReadSnapshotRows{columns: []string{"id"}, rows: [][]driver.Value{{fields["mailbox_id"].(int64) + 100}}}, nil
	}
	if strings.Contains(q, `FROM "vacation_configs"`) {
		if len(args) != 2 || !strings.Contains(q, "mailbox_id =") || args[1].Value != int64(1) {
			return nil, errors.New("vacation lookup lost mailbox or bound")
		}
		c.record(q, args)
		id := args[0].Value.(int64)
		if id == 4 {
			return nil, errors.New("private vacation lookup detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "mailbox_id", "enabled", "subject", "body", "start_date", "end_date", "created_at", "updated_at"}}
		if id != 2 {
			r.rows = [][]driver.Value{{id + 100, id, true, "Away", "Existing reply", sieveInstallationTime, sieveInstallationTime.AddDate(0, 0, 7), sieveInstallationTime, sieveInstallationTime}}
		}
		return r, nil
	}
	return c.mailRemovalConnection.QueryContext(ctx, q, args)
}
func (c vacationMutationConnection) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(q, `UPDATE "vacation_configs" SET`) {
		return nil, errors.New("unexpected ancillary vacation write")
	}
	c.record(q, args)
	fields, err := vacationMutationFields(q, args)
	if err != nil {
		return nil, err
	}
	if fields["subject"] == "update unavailable" || ctx.Value(vacationDisableFailure{}) == true {
		return nil, errors.New("private vacation update detail")
	}
	return driver.RowsAffected(1), nil
}
func newVacationMutationScenarios(t *testing.T) mailRemovalScenarios {
	f := newMailRulesWriteScenarios(t, func(base mailRemovalConnection) driver.Connector { return vacationMutationConnection{base} })
	for _, name := range []string{"body_array", "body_missing", "configured", "dates_reversed", "enabled_missing", "enabled_null", "reply_missing", "subject_missing", "disabled", "dates_equal", "dates_offset", "dates_null", "clear_dates", "enabled_false_create", "date_invalid"} {
		f.scenarios[name] = f.scenarios["found"]
	}
	s := f.scenarios["update_unavailable"]
	f.scenarios["update_unavailable"] = accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), vacationDisableFailure{}, true)))
	}), s.token}
	return f
}
func TestVacationMutationLookupFailuresDoNotWrite(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			f := newVacationMutationScenarios(t)
			s := f.scenarios["lookup_unavailable"]
			r := httptest.NewRequest(method, "/api/v1/accounts/4/vacation", strings.NewReader(`{"enabled":false}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+s.token)
			rr := httptest.NewRecorder()
			s.handler.ServeHTTP(rr, r)
			if rr.Code != 500 {
				t.Fatalf("lookup failure: got %d %s, want sanitized500", rr.Code, rr.Body)
			}
			if len(f.store.snapshot()) != 1 {
				t.Fatal("failed lookup attempted a vacation write")
			}
		})
	}
}
func TestVacationMutationExamples(t *testing.T) {
	f := newVacationMutationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"setVacation", "disableVacation"} {
		if len(pairs[op]) < 25 {
			t.Fatalf("incomplete %s examples", op)
		}
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := f.scenarios[name]
				if !ok {
					t.Fatalf("missing vacation scenario %s", name)
				}
				method := http.MethodPut
				if op == "disableVacation" {
					method = http.MethodDelete
				}
				var body []byte
				if name == "malformed_json" {
					body = []byte("{")
				} else if name == "request_too_large" {
					body = bytes.Repeat([]byte("x"), 257)
				} else if op == "setVacation" {
					body, err = json.Marshal(pair.Request.Body)
					if err != nil {
						t.Fatal(err)
					}
				}
				var input map[string]interface{}
				if op == "setVacation" && name != "body_array" && name != "malformed_json" {
					if err := json.Unmarshal(pair.Request.Body, &input); err != nil {
						t.Fatal(err)
					}
				}
				before := len(f.store.snapshot())
				req := httptest.NewRequest(method, messageCollectionReadURI(vacationMutationPath, pair), bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				if s.token != "" {
					req.Header.Set("Authorization", "Bearer "+s.token)
				}
				rr := httptest.NewRecorder()
				s.handler.ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status {
					t.Fatalf("got %d %s, want %d", rr.Code, rr.Body, pair.Response.Status)
				}
				if rr.Code == 204 {
					if rr.Body.Len() != 0 {
						t.Fatalf("204 body:%s", rr.Body)
					}
				} else {
					assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				}
				calls := f.store.snapshot()[before:]
				reads, writes := 0, 0
				wantRead := rr.Code < 300 || name == "lookup_unavailable" || name == "create_unavailable" || name == "update_unavailable"
				wantWrite := wantRead && name != "lookup_unavailable" && !(op == "disableVacation" && name == "missing")
				for _, m := range calls {
					if strings.HasPrefix(m.query, "SELECT") {
						reads++
						if m.args[0].Value != int64(pair.Request.Path["id"].(float64)) {
							t.Fatal("vacation lookup changed mailbox")
						}
						continue
					}
					writes++
					fields, err := vacationMutationFields(m.query, m.args)
					if err != nil {
						t.Fatal(err)
					}
					id := int64(pair.Request.Path["id"].(float64))
					if strings.HasPrefix(m.query, "UPDATE") {
						if fields["id"] != id+100 {
							t.Fatalf("update selected wrong row:%v", fields)
						}
					} else if fields["mailbox_id"] != id {
						t.Fatal("insert changed mailbox")
					}
					if op == "disableVacation" {
						if len(fields) != 3 || fields["enabled"] != false {
							t.Fatalf("disable changed other settings:%v", fields)
						}
						continue
					}
					if len(fields) != 7 && len(fields) != 8 {
						t.Fatalf("unexpected configuration fields:%v", fields)
					}
					if fields["enabled"] != input["enabled"] {
						t.Fatal("enabled changed")
					}
					for _, key := range []string{"subject", "body", "start_date", "end_date"} {
						expected, exists := input[key]
						if key == "subject" || key == "body" {
							if !exists {
								expected = ""
							}
							if fields[key] != expected {
								t.Fatalf("%s changed:%v", key, fields)
							}
						} else if !exists || expected == nil {
							if fields[key] != nil {
								t.Fatalf("%s not cleared:%v", key, fields[key])
							}
						} else {
							stamp, ok := fields[key].(time.Time)
							want, err := time.Parse(time.RFC3339, fmt.Sprint(expected))
							if !ok || err != nil || !stamp.Equal(want) {
								t.Fatalf("%s=%v want%v", key, fields[key], expected)
							}
						}
					}
				}
				if (reads == 1) != wantRead || reads > 1 || (writes == 1) != wantWrite || writes > 1 {
					t.Fatalf("vacation lookup/write=%d/%d want %t/%t", reads, writes, wantRead, wantWrite)
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if w.OperationID == "setVacation" || w.OperationID == "disableVacation" {
				t.Errorf("vacation mutation warning:%v", w)
			}
		}
	}
}
func TestVacationMutationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for vacation mutation exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newVacationMutationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"setVacation", "disableVacation"} {
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := f.scenarios[name]
				if !ok {
					t.Fatalf("missing vacation scenario %s", name)
				}
				method := http.MethodPut
				if op == "disableVacation" {
					method = http.MethodDelete
				}
				checkQueueMutationPairWithVertrag(t, binary, bulkQueueExampleScenario{s, nil}, method, vacationMutationPath, name, pair, 257)
			})
		}
	}
}
