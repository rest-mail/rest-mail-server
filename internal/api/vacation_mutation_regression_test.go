package api

import (
	"context"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

type vacationDisableFailure struct{}

type vacationMutationConnection struct{ mailRemovalConnection }

func (c vacationMutationConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c vacationMutationConnection) Driver() driver.Driver { return c }

func (c vacationMutationConnection) Open(string) (driver.Conn, error) { return c, nil }

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
