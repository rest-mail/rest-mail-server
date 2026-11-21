package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/respond"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

func operatorRefusal(t *testing.T, exit int, raw, wantCode string) {
	t.Helper()
	var response respond.ErrorResponse
	if err := json.Unmarshal([]byte(raw), &response); err != nil || exit != exitFailed || response.Error.Code != wantCode || response.Error.Message == "" {
		t.Fatalf("want CLI refusal %s, got exit %d: %s (%v)", wantCode, exit, raw, err)
	}
}

func TestAC04OperatorAPIsWithScopedCredentials(t *testing.T) {
	server := newQueueServer(t)
	rows := server.seed(t)
	if err := server.db.Model(&models.OutboundQueue{}).Where("id IN ?", []uint{rows.pending, rows.greylisted, rows.delivered}).
		Update("created_at", time.Now().UTC().Truncate(time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if err := server.db.Model(&models.OutboundQueue{}).Where("id = ?", rows.refused).
		Update("sending_domain", "other.test").Error; err != nil {
		t.Fatal(err)
	}
	if err := server.db.Model(&models.OutboundQueue{}).Where("id = ?", rows.pending).
		Updates(map[string]any{"sender": "", "sending_domain": "example.test"}).Error; err != nil {
		t.Fatal(err)
	}
	scoped, _ := server.key(t, "scoped-queue", grants.Domain("example.test"), "queue:read", "queue:manage")
	noQueue, _ := server.key(t, "no-queue", grants.All, "database:read")
	queuePath := "/api/v1/admin/queue"

	t.Run("scoped rows before counts and pagination", func(t *testing.T) {
		var persisted []uint
		if err := server.db.Raw(`SELECT id FROM outbound_queue WHERE id != ? ORDER BY created_at DESC, id DESC`, rows.refused).Scan(&persisted).Error; err != nil {
			t.Fatal(err)
		}
		for _, offset := range []int{0, 2, 4, 6, 7} {
			exit, out, errOut := server.run(t, scoped, "", "list", "--json", "--limit", "2", "--offset", strconv.Itoa(offset))
			var page queuePage
			if err := json.Unmarshal([]byte(out), &page); err != nil || exit != exitOK {
				t.Fatalf("scoped list offset %d: exit %d: %s%s (%v)", offset, exit, out, errOut, err)
			}
			ids := make([]uint, 0, len(page.Items))
			for _, item := range page.Items {
				ids = append(ids, item.ID)
			}
			want := persisted[min(offset, len(persisted)):min(offset+2, len(persisted))]
			if page.Limit != 2 || page.Offset != offset || page.Total != int64(len(persisted)) || !slices.Equal(ids, want) || page.Items == nil {
				t.Fatalf("scoped list offset %d: got %v of %d (%d/%d), want %v of %d", offset, ids, page.Total, page.Limit, page.Offset, want, len(persisted))
			}
		}
		exit, out, errOut := server.run(t, scoped, "", "stats", "--json")
		var stats apitypes.QueueStatistics
		if err := json.Unmarshal([]byte(out), &stats); err != nil || exit != exitOK || stats.Total != int64(len(persisted)) || stats.Deferred != 1 {
			t.Fatalf("scoped stats: exit %d: %s%s (%v)", exit, out, errOut, err)
		}
		exit, out, errOut = server.run(t, scoped, "", "show", strconv.FormatUint(uint64(rows.pending), 10), "--json")
		var item queueEntry
		if err := json.Unmarshal([]byte(out), &item); err != nil || exit != exitOK || item.ID != rows.pending || item.Recipient != "bob@example.org" {
			t.Fatalf("scoped show: exit %d: %s%s (%v)", exit, out, errOut, err)
		}
	})

	t.Run("out of scope and missing entries are indistinguishable", func(t *testing.T) {
		var bodies [][]byte
		for _, id := range []uint{rows.refused, 999999} {
			exit, _, errOut := server.run(t, scoped, "", "show", fmt.Sprint(id), "--json")
			operatorRefusal(t, exit, errOut, "forbidden")
			bodies = append(bodies, operatorAPIGet(t, server, scoped, fmt.Sprintf("%s/%d", queuePath, id), http.StatusForbidden))
		}
		if !bytes.Equal(bodies[0], bodies[1]) {
			t.Fatalf("out-of-scope and missing refusals differ: %s vs %s", bodies[0], bodies[1])
		}
	})

	t.Run("unauthorized reads and scoped mutation are refused", func(t *testing.T) {
		for _, check := range []struct {
			name, key, code string
			args            []string
		}{
			{"list missing capability", noQueue, "forbidden", []string{"list"}},
			{"stats missing capability", noQueue, "forbidden", []string{"stats"}},
			{"show missing capability", noQueue, "forbidden", []string{"show", fmt.Sprint(rows.pending)}},
			{"invalid credential", "not-a-key", "unauthorized", []string{"stats"}},
			{"retry scoped manage", scoped, "forbidden", []string{"retry", fmt.Sprint(rows.greylisted)}},
			{"purge scoped preview", scoped, "forbidden", []string{"purge", "--all", "--yes"}},
		} {
			t.Run(check.name, func(t *testing.T) {
				exit, _, errOut := server.run(t, check.key, "", append(check.args, "--json")...)
				operatorRefusal(t, exit, errOut, check.code)
			})
		}
		server.wantStatuses(t, rows.statuses())
	})

	t.Run("bulk preview must cover the whole mutation", func(t *testing.T) {
		_, mixed, err := repositories.NewCredentialRepository(server.db).CreateAPIKey(server.holder.ID, server.holder.ID, "mixed-scope", []auth.ScopedPermission{
			{Capability: "queue:read", Scope: grants.Domain("example.test")},
			{Capability: "queue:manage", Scope: grants.All},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		exit, _, errOut := server.run(t, mixed, "", "retry", "--status", "deferred", "--yes", "--json")
		operatorRefusal(t, exit, errOut, "forbidden")
		server.wantStatuses(t, rows.statuses())
	})

	t.Run("invalid pagination is a typed API error", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, server.url+queuePath+"?scope=authorized&limit=not-a-number", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+scoped)
		res, err := server.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var problem respond.ValidationErrorResponse
		if err := json.NewDecoder(res.Body).Decode(&problem); err != nil || res.StatusCode != http.StatusUnprocessableEntity || problem.Error.Code != "validation_failed" || problem.Error.Details.Fields["limit"] != "must be an integer" {
			t.Fatalf("pagination refusal: %d %+v (%v)", res.StatusCode, problem, err)
		}
	})

	t.Run("masked and audited slow statements", func(t *testing.T) {
		if err := server.db.Exec("CREATE EXTENSION IF NOT EXISTS pg_stat_statements").Error; err != nil {
			t.Fatal(err)
		}
		const literal, credential = "operator-test-literal", "fake-operator-credential"
		if err := server.db.Exec(`SELECT pg_sleep(0.02), length('` + literal + `')`).Error; err != nil {
			t.Fatal(err)
		}
		if err := server.db.Exec(`SELECT /* ` + credential + ` */ pg_sleep(0.02), length('` + literal + `'), 1`).Error; err != nil {
			t.Fatal(err)
		}
		var queryID int64
		if err := server.db.Raw(`SELECT queryid FROM pg_stat_statements WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database()) AND query LIKE ? AND calls = 1`, "%/* "+credential+" */%").Scan(&queryID).Error; err != nil || queryID == 0 {
			t.Fatalf("persisted unredacted statement not found: %v (id %d)", err, queryID)
		}
		key, keyID := server.key(t, "slow-reader", grants.All, "database:read")
		exit, out, errOut := server.cli(t, key, "", "slow-statements", "list", "--limit", "100", "--json")
		var view slowStatementsView
		if err := json.Unmarshal([]byte(out), &view); err != nil || exit != exitOK {
			t.Fatalf("slow statements: exit %d: %s%s (%v)", exit, out, errOut, err)
		}
		if strings.Contains(out, literal) || strings.Contains(out, credential) {
			t.Fatal("slow statement response exposed a literal or credential")
		}
		var normalized, masked bool
		for _, statement := range view.Statements {
			normalized = normalized || (statement.Query == "SELECT pg_sleep($1), length($2)" && statement.Calls == 1 && statement.Rows == 1)
			masked = masked || (statement.Query == fmt.Sprintf("[redacted query %d]", queryID) && statement.Calls == 1)
		}
		if !normalized || !masked {
			t.Fatalf("wanted normalized statement and stored query ID redaction, got normalized=%v, masked=%v", normalized, masked)
		}
		var audit []models.AuditEntry
		if err := server.db.Where("route = ? AND method = ? AND credential_id = ?", "/api/v1/admin/database/statements", http.MethodGet, keyID).Find(&audit).Error; err != nil {
			t.Fatal(err)
		}
		if len(audit) != 1 || audit[0].Action != "admin.database.statements.read" || audit[0].Outcome != models.AuditOutcomeSuccess || audit[0].Status != http.StatusOK || audit[0].ActorID == nil || *audit[0].ActorID != server.holder.ID || len(audit[0].Target) != 0 {
			t.Fatalf("slow statement read did not create one redacted audit row: %+v", audit)
		}
	})

	t.Run("database diagnostics need their own global capability", func(t *testing.T) {
		key, _ := server.key(t, "scoped-sql", grants.Domain("example.test"), "database:read")
		for _, denied := range []string{server.read, key} {
			exit, _, errOut := server.cli(t, denied, "", "slow-statements", "list", "--json")
			operatorRefusal(t, exit, errOut, "forbidden")
		}
	})
}
