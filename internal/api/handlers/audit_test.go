package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/audit"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// openAuditTestDB gives a test a database of its own with the audit log
// migrated, dropped afterwards: entries cannot be deleted once written, so
// they must not go into the database other tests share. It skips (never
// fails) when no database is reachable.
func openAuditTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	admin, err := connectTestDB()
	if err != nil {
		t.Skipf("audit API DB test skipped: no database reachable (%v)", err)
	}
	name := fmt.Sprintf("restmail_auditapi_%d_%d", os.Getpid(), time.Now().UnixNano())
	if err := admin.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Skipf("audit API DB test skipped: cannot create a database (%v)", err)
	}
	cfg := testDBConfig()
	cfg.DBName = name
	scratch, err := rmdb.Connect(cfg)
	if err != nil {
		t.Fatalf("connect to scratch database: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := scratch.DB(); err == nil {
			_ = sqlDB.Close()
		}
		admin.Exec("DROP DATABASE IF EXISTS " + name)
	})
	if err := rmdb.MigrateAuditLog(scratch); err != nil {
		t.Fatalf("migrate the audit log: %v", err)
	}
	return scratch
}

type auditPage struct {
	Data       []models.AuditEntry `json:"data"`
	Pagination struct {
		Cursor  string `json:"cursor"`
		HasMore bool   `json:"has_more"`
	} `json:"pagination"`
}

func listAudit(t *testing.T, h *AuditHandler, query string) (int, auditPage) {
	t.Helper()
	rr := httptest.NewRecorder()
	auditRoutes(h).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/admin/audit?"+query, nil))
	var page auditPage
	if rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode %s: %v", rr.Body.String(), err)
		}
	}
	return rr.Code, page
}

func seqs(page auditPage) []int64 {
	out := make([]int64, len(page.Data))
	for i, e := range page.Data {
		out[i] = e.Seq
	}
	return out
}

func TestAuditAPI_ListFiltersAndPages(t *testing.T) {
	db := openAuditTestDB(t)
	base := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	adminID, mailboxID := uint(1), uint(5)
	for i, e := range []models.AuditEntry{
		{ActorKind: models.AuditActorAnonymous, ActorName: "admin", Action: "auth.sign_in", Outcome: models.AuditOutcomeDenied, Status: 401},
		{ActorKind: models.AuditActorAccount, ActorID: &adminID, ActorName: "admin@example.com", Action: "auth.sign_in", Outcome: models.AuditOutcomeSuccess, Status: 200},
		{ActorKind: models.AuditActorAccount, ActorID: &adminID, ActorName: "admin@example.com", Action: "admin.change", Outcome: models.AuditOutcomeSuccess, Status: 201},
		{ActorKind: models.AuditActorAccount, ActorID: &mailboxID, ActorName: "Chris_100%@example.com", Action: "auth.password_change", Outcome: models.AuditOutcomeFailed, Status: 422},
		{ActorKind: models.AuditActorAccount, ActorID: &adminID, ActorName: "admin@example.com", Action: "admin.change", Outcome: models.AuditOutcomeError, Status: 500},
	} {
		e.OccurredAt = base.Add(time.Duration(i) * time.Hour)
		if err := audit.Append(db, &e); err != nil {
			t.Fatal(err)
		}
	}
	h := NewAuditHandler(db)

	if _, page := listAudit(t, h, ""); fmt.Sprint(seqs(page)) != "[5 4 3 2 1]" || page.Pagination.HasMore {
		t.Fatalf("all entries, newest first: %v (more: %v)", seqs(page), page.Pagination.HasMore)
	}

	// Paged by before_seq: the cursor is where the next page starts.
	_, first := listAudit(t, h, "limit=2")
	if fmt.Sprint(seqs(first)) != "[5 4]" || !first.Pagination.HasMore || first.Pagination.Cursor != "4" {
		t.Fatalf("first page: %v %+v", seqs(first), first.Pagination)
	}
	_, second := listAudit(t, h, "limit=2&before_seq="+first.Pagination.Cursor)
	if fmt.Sprint(seqs(second)) != "[3 2]" || !second.Pagination.HasMore {
		t.Fatalf("second page: %v %+v", seqs(second), second.Pagination)
	}
	_, last := listAudit(t, h, "limit=2&before_seq=2")
	if fmt.Sprint(seqs(last)) != "[1]" || last.Pagination.HasMore {
		t.Fatalf("last page: %v %+v", seqs(last), last.Pagination)
	}

	for query, want := range map[string]string{
		"actor_kind=account":                     "[5 4 3 2]",
		"actor_kind=anonymous":                   "[1]",
		"actor_id=5":                             "[4]",
		"actor=CHRIS_100%25":                     "[4]", // contains, any case, % and _ taken literally
		"actor=C_ris":                            "[]",  // _ would match the h
		"actor=C%25example":                      "[]",  // % would match "hris_100"
		"action=admin.change":                    "[5 3]",
		"outcome=denied":                         "[1]",
		"since=2026-10-17T11:00:00Z":             "[5 4 3]",
		"until=2026-10-17T10:00:00Z":             "[2 1]",
		"action=auth.sign_in&actor_kind=account": "[2]",
	} {
		code, page := listAudit(t, h, query)
		if code != http.StatusOK || fmt.Sprint(seqs(page)) != want {
			t.Errorf("%s: %d %v, want %s", query, code, seqs(page), want)
		}
	}

	// A filter that does not parse is refused, never ignored.
	for _, query := range []string{
		"actor_kind=root", "actor_id=x", "outcome=maybe", "since=yesterday",
		"until=2026-10-17", "before_seq=0", "limit=0", "limit=201",
	} {
		if code, _ := listAudit(t, h, query); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422", query, code)
		}
	}
}

func TestAuditAPI_Verify(t *testing.T) {
	db := openAuditTestDB(t)
	h := NewAuditHandler(db)
	verify := func(query ...string) audit.Result {
		t.Helper()
		rr := httptest.NewRecorder()
		auditRoutes(h).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/admin/audit/verify?"+strings.Join(query, "&"), nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("verify: %d %s", rr.Code, rr.Body.String())
		}
		var body struct{ Data audit.Result }
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Data
	}

	if res := verify(); !res.OK || res.Checked != 0 || res.LastHash != audit.GenesisHash {
		t.Fatalf("empty log: %+v", res)
	}
	for i := 0; i < 3; i++ {
		e := models.AuditEntry{ActorKind: models.AuditActorSystem, Action: "admin.change", Outcome: models.AuditOutcomeSuccess, Status: 200}
		if err := audit.Append(db, &e); err != nil {
			t.Fatal(err)
		}
	}
	res := verify()
	if !res.OK || res.Checked != 3 || res.FirstSeq != 1 || res.LastSeq != 3 || len(res.LastHash) != 64 {
		t.Fatalf("three entries: %+v", res)
	}
	// The latest (seq, hash), as an anchor kept outside the server, holds; in
	// capitals too.
	anchor := fmt.Sprintf("anchor=%d:%s", res.LastSeq, strings.ToUpper(res.LastHash))
	if a := verify(anchor); !a.OK || a.Anchor == nil || a.Anchor.Seq != 3 || a.Anchor.Hash != res.LastHash {
		t.Fatalf("against its own anchor: %+v", a)
	}
	if a := verify("anchor=4:" + res.LastHash); a.OK || a.BrokenAtSeq == nil || *a.BrokenAtSeq != 4 {
		t.Fatalf("against an anchor past the end: %+v", a)
	}

	// Whoever owns the database can switch the guard off and edit an entry;
	// the chain shows it.
	if err := db.Exec("ALTER TABLE audit_log DISABLE TRIGGER audit_log_no_update_or_delete").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE audit_log SET outcome = 'denied' WHERE seq = 2").Error; err != nil {
		t.Fatal(err)
	}
	res = verify()
	if res.OK || res.BrokenAtSeq == nil || *res.BrokenAtSeq != 2 || res.Checked != 1 || res.Reason == "" {
		t.Fatalf("after an edit: %+v", res)
	}
}

// An anchor is a seq and a hash, both, and each must parse: a check
// against a mistyped anchor must never read as a check of the chain alone.
func TestAuditAPI_VerifyRefusesABadAnchor(t *testing.T) {
	h := NewAuditHandler(nil)
	hash := strings.Repeat("ab", 32)
	for _, query := range []string{
		"anchor=3",
		"anchor=3:",
		"anchor=" + hash,
		"anchor=:" + hash,
		"anchor=0:" + hash,
		"anchor=-1:" + hash,
		"anchor=+3:" + hash,
		"anchor=03:" + hash,
		"anchor=x:" + hash,
		"anchor=3:" + hash[:62],
		"anchor=3:" + strings.Repeat("zz", 32),
		"anchor=3:" + hash + ":" + hash,
	} {
		rr := httptest.NewRecorder()
		auditRoutes(h).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/admin/audit/verify?"+query, nil))
		if rr.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422", query, rr.Code)
		}
	}
}
