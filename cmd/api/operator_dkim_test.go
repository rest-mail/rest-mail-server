package main

import (
	"bytes"
	"crypto/rsa"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	dkim "github.com/rest-mail/go-dkim"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/outbound"
	"gorm.io/gorm"
)

const ensureTestMaster = "isolated-dkim-ensure-test-master-key"

func dkimEnsureFixture(t *testing.T) *gorm.DB {
	t.Helper()
	database := scratchDatabase(t)
	t.Logf("isolated scratch database: %s", os.Getenv("DB_NAME"))
	t.Setenv("MASTER_KEY", ensureTestMaster)
	t.Setenv("ENVIRONMENT", "development")
	t.Setenv("RESTMAIL_OPERATOR_DEBUG", "1")
	if err := database.Migrator().CreateTable(&models.Domain{}); err != nil {
		t.Fatal(err)
	}
	// Deliberately omit every other server table. The operator must never try
	// migrations/RBAC bootstrap, or touch unrelated rows.
	if err := database.Exec("CREATE TABLE unrelated_guard (value text); INSERT INTO unrelated_guard VALUES ('preserve')").Error; err != nil {
		t.Fatal(err)
	}
	return database
}

func readEnsureDomain(t *testing.T, database *gorm.DB) models.Domain {
	t.Helper()
	var domain models.Domain
	if err := database.Where("name = ?", "example.test").Take(&domain).Error; err != nil {
		t.Fatal("cannot read fixture domain")
	}
	return domain
}

func ensureRun(t *testing.T, created bool) dkimEnsureResult {
	t.Helper()
	status, out, errOut := run("dkim-key", "ensure", "--domain", "example.test")
	if status != exitOK || errOut != "" {
		t.Fatalf("ensure: status %d, expected success and empty stderr", status)
	}
	var result dkimEnsureResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal("result must be one JSON object")
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(out), &fields); err != nil || len(fields) != 4 {
		t.Fatal("result must have exactly four public fields")
	}
	if result.Domain != "example.test" || result.Created != created || result.Selector == "" || !strings.HasPrefix(result.PublicDKIMTXT, "v=DKIM1;") {
		t.Fatal("incorrect public result")
	}
	return result
}

func assertEnsureUnchanged(t *testing.T, database *gorm.DB, before string) {
	t.Helper()
	if operatorDatabaseSnapshot(t, database) != before {
		t.Fatal("ensure changed database on a preservation/failure path")
	}
}

func TestDKIMEnsure_CreatesEncryptedAndRetriesWithoutWrites(t *testing.T) {
	database := dkimEnsureFixture(t)
	if err := database.Create(&models.Domain{Name: "example.test"}).Error; err != nil {
		t.Fatal(err)
	}
	beforeSchema := strings.Split(operatorDatabaseSnapshot(t, database), "\n")[0]
	first := ensureRun(t, true)
	domain := readEnsureDomain(t, database)
	if domain.DKIMCreatedAt == nil || domain.DKIMCreatedAt.After(time.Now()) {
		t.Fatal("created key has no installation timestamp")
	}
	if !models.DKIMKeyIsEncrypted(domain.DKIMPrivateKey) || strings.Contains(domain.DKIMPrivateKey, "PRIVATE KEY") {
		t.Fatal("key not encrypted at rest")
	}
	plain, err := models.LoadDKIMPrivateKey(domain.DKIMPrivateKey, ensureTestMaster)
	if err != nil {
		t.Fatal("cannot decrypt created key")
	}
	key, err := outbound.ParseDKIMKey(plain)
	if err != nil {
		t.Fatal("cannot parse created key")
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok || rsaKey.N.BitLen() < 2048 {
		t.Fatal("created key must be RSA >=2048 bits")
	}
	if err := outbound.CheckDKIMKey(domain.Name, domain.DKIMSelector, plain); err != nil {
		t.Fatal("created key does not sign and verify")
	}
	expected, err := dkimRecordValue(domain.DKIMPrivateKey, ensureTestMaster)
	if err != nil || first.PublicDKIMTXT != expected || first.Selector != dkim.DefaultSelector {
		t.Fatal("public record does not match stored key/default selector")
	}
	if after := strings.Split(operatorDatabaseSnapshot(t, database), "\n")[0]; after != beforeSchema {
		t.Fatal("ensure changed schema")
	}
	var sentinel string
	if err := database.Raw("SELECT value FROM unrelated_guard").Scan(&sentinel).Error; err != nil || sentinel != "preserve" {
		t.Fatal("unrelated data changed")
	}
	snapshot := operatorDatabaseSnapshot(t, database)
	second := ensureRun(t, false)
	if first.Selector != second.Selector || first.PublicDKIMTXT != second.PublicDKIMTXT {
		t.Fatal("retry rotated key")
	}
	assertEnsureUnchanged(t, database, snapshot)
}

func TestDKIMEnsure_PreservesExistingCustomSelector(t *testing.T) {
	database := dkimEnsureFixture(t)
	private, public, err := dkim.GenerateKey(2048)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := models.EncryptDKIMPrivateKey(private, ensureTestMaster)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&models.Domain{Name: "example.test", DKIMSelector: "custom.2026", DKIMPrivateKey: stored, UpdatedAt: time.Unix(123456, 0)}).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := operatorDatabaseSnapshot(t, database)
	result := ensureRun(t, false)
	expected, err := dkim.RecordValue(public)
	if err != nil || result.Selector != "custom.2026" || result.PublicDKIMTXT != expected {
		t.Fatal("existing public record not preserved")
	}
	assertEnsureUnchanged(t, database, snapshot)
}

func TestDKIMEnsure_AbsentLegacyFields(t *testing.T) {
	database := dkimEnsureFixture(t)
	for _, pair := range [][2]any{{nil, nil}, {nil, ""}, {"", nil}, {"", ""}} {
		if err := database.Exec("DELETE FROM domains").Error; err != nil {
			t.Fatal(err)
		}
		if err := database.Exec("INSERT INTO domains (name, dkim_selector, dkim_private_key) VALUES (?, ?, ?)", "example.test", pair[0], pair[1]).Error; err != nil {
			t.Fatal(err)
		}
		ensureRun(t, true)
		if !models.DKIMKeyIsEncrypted(readEnsureDomain(t, database).DKIMPrivateKey) {
			t.Fatal("legacy empty fields produced plaintext")
		}
	}
}

func TestDKIMEnsure_RefusesMaterialWithoutMutationOrLeak(t *testing.T) {
	database := dkimEnsureFixture(t)
	private, _, err := dkim.GenerateKey(2048)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := models.EncryptDKIMPrivateKey(private, ensureTestMaster)
	if err != nil {
		t.Fatal(err)
	}
	wrongKey, err := models.EncryptDKIMPrivateKey(private, "different-master-key-for-test")
	if err != nil {
		t.Fatal(err)
	}
	invalid, err := models.EncryptDKIMPrivateKey("PRIVATE-INVALID-MATERIAL-SENTINEL", ensureTestMaster)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name          string
		selector, key any
	}{
		{"private only", "", stored}, {"private with null selector", nil, stored},
		{"selector only", "mail", ""}, {"selector with null key", "mail", nil},
		{"invalid selector", "bad_selector", stored}, {"whitespace selector", " ", stored},
		{"invalid key", "mail", invalid}, {"corrupt ciphertext", "mail", models.DKIMKeyCipherPrefix + "PRIVATE-CIPHER-SENTINEL"},
		{"wrong master", "mail", wrongKey}, {"plaintext legacy key", "mail", private},
		{"whitespace key", "mail", " "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := database.Exec("DELETE FROM domains").Error; err != nil {
				t.Fatal(err)
			}
			if err := database.Exec("INSERT INTO domains (name, dkim_selector, dkim_private_key) VALUES (?, ?, ?)", "example.test", tc.selector, tc.key).Error; err != nil {
				t.Fatal(err)
			}
			snapshot := operatorDatabaseSnapshot(t, database)
			status, out, errOut := run("dkim-key", "ensure", "--domain", "example.test")
			if status != exitFailed || out != "" || errorCodeIn(t, errOut) != "dkim_material" {
				t.Fatal("expected controlled material refusal")
			}
			for _, secret := range []string{private, stored, wrongKey, invalid, "PRIVATE-CIPHER-SENTINEL", ensureTestMaster, "PRIVATE-INVALID-MATERIAL-SENTINEL"} {
				if strings.Contains(out+errOut, secret) {
					t.Fatal("private material leaked")
				}
			}
			assertEnsureUnchanged(t, database, snapshot)
		})
	}
}

func TestDKIMEnsure_MissingDomainAndMasterKey(t *testing.T) {
	database := dkimEnsureFixture(t)
	snapshot := operatorDatabaseSnapshot(t, database)
	status, out, errOut := run("dkim-key", "ensure", "--domain", "example.test")
	if status != exitFailed || out != "" || errorCodeIn(t, errOut) != "no_such_domain" {
		t.Fatal("expected missing domain failure")
	}
	assertEnsureUnchanged(t, database, snapshot)
	if err := database.Create(&models.Domain{Name: "example.test"}).Error; err != nil {
		t.Fatal(err)
	}
	snapshot = operatorDatabaseSnapshot(t, database)
	for _, master := range []string{"", "short", strings.Repeat(" ", 32)} {
		t.Setenv("MASTER_KEY", master)
		status, out, errOut = run("dkim-key", "ensure", "--domain", "example.test")
		if status != exitFailed || out != "" || errorCodeIn(t, errOut) != "config" {
			t.Fatal("expected master key failure")
		}
		assertEnsureUnchanged(t, database, snapshot)
	}
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("MASTER_KEY", "")
	status, out, errOut = run("dkim-key", "ensure", "--domain", "example.test")
	if status != exitFailed || out != "" || errorCodeIn(t, errOut) != "config_or_database" {
		t.Fatal("expected redacted config failure")
	}
	if strings.Contains(errOut, ensureTestMaster) {
		t.Fatal("config leaked")
	}
	assertEnsureUnchanged(t, database, snapshot)
}

func TestDKIMEnsure_MissingSchemaDoesNotInitialize(t *testing.T) {
	database := scratchDatabase(t)
	t.Logf("isolated scratch database: %s", os.Getenv("DB_NAME"))
	t.Setenv("MASTER_KEY", ensureTestMaster)
	t.Setenv("RESTMAIL_OPERATOR_DEBUG", "1")
	snapshot := operatorDatabaseSnapshot(t, database)
	status, out, errOut := run("dkim-key", "ensure", "--domain", "example.test")
	if status != exitFailed || out != "" || errorCodeIn(t, errOut) != "dkim_ensure" {
		t.Fatal("expected controlled missing schema error")
	}
	if strings.Contains(errOut, "SELECT") || strings.Contains(errOut, "SQLSTATE") {
		t.Fatal("driver error leaked")
	}
	assertEnsureUnchanged(t, database, snapshot)
}

func TestDKIMEnsure_ConcurrentEnsuresCreateOnce(t *testing.T) {
	database := dkimEnsureFixture(t)
	if err := database.Create(&models.Domain{Name: "example.test"}).Error; err != nil {
		t.Fatal(err)
	}
	// Set global logging before launching goroutines; each command then has its
	// own connection, output buffers, transaction and lock.
	run("dkim-key", "ensure")
	const n = 4
	var wg sync.WaitGroup
	results := make([]dkimEnsureResult, n)
	statuses := make([]int, n)
	errorsOut := make([]string, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			var out, errOut bytes.Buffer
			statuses[i] = dkimKeyCommand([]string{"ensure", "--domain", "example.test"}, &out, &errOut)
			errorsOut[i] = errOut.String()
			if json.Unmarshal(out.Bytes(), &results[i]) != nil {
				statuses[i] = -1
			}
		}(i)
	}
	close(start)
	wg.Wait()
	created := 0
	for i, result := range results {
		if statuses[i] != exitOK || errorsOut[i] != "" {
			t.Fatal("concurrent ensure failed")
		}
		if result.Created {
			created++
		}
		if result.Selector != results[0].Selector || result.PublicDKIMTXT != results[0].PublicDKIMTXT {
			t.Fatal("concurrent ensure rotated material")
		}
	}
	if created != 1 {
		t.Fatalf("created %d keys, want 1", created)
	}
	snapshot := operatorDatabaseSnapshot(t, database)
	ensureRun(t, false)
	assertEnsureUnchanged(t, database, snapshot)
}

func TestDKIMEnsure_UpdateFailureRollsBackAndRedacts(t *testing.T) {
	database := dkimEnsureFixture(t)
	if err := database.Create(&models.Domain{Name: "example.test"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`CREATE FUNCTION reject_dkim_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'PRIVATE-DRIVER-SENTINEL %', NEW.dkim_private_key; END $$`,
		`CREATE TRIGGER reject_dkim_update BEFORE UPDATE ON domains FOR EACH ROW EXECUTE FUNCTION reject_dkim_update()`,
	} {
		if err := database.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	snapshot := operatorDatabaseSnapshot(t, database)
	status, out, errOut := run("dkim-key", "ensure", "--domain", "example.test")
	if status != exitFailed || out != "" || errorCodeIn(t, errOut) != "dkim_ensure" {
		t.Fatal("expected update failure")
	}
	for _, secret := range []string{"PRIVATE-DRIVER-SENTINEL", models.DKIMKeyCipherPrefix, "UPDATE", "SQLSTATE"} {
		if strings.Contains(errOut, secret) {
			t.Fatal("database/private diagnostic leaked")
		}
	}
	assertEnsureUnchanged(t, database, snapshot)
}

func TestDKIMEnsure_LockTimeoutDoesNotMutate(t *testing.T) {
	database := dkimEnsureFixture(t)
	if err := database.Create(&models.Domain{Name: "example.test"}).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := operatorDatabaseSnapshot(t, database)
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	if err := tx.Exec("SELECT id FROM domains WHERE name = ? FOR UPDATE", "example.test").Error; err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	status, out, errOut := run("dkim-key", "ensure", "--domain", "example.test")
	if status != exitFailed || out != "" || errorCodeIn(t, errOut) != "dkim_ensure" {
		t.Fatal("expected bounded lock failure")
	}
	if elapsed := time.Since(start); elapsed < 2*time.Second || elapsed > dkimEnsureTimeout+time.Second {
		t.Fatal("lock timeout not bounded")
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	assertEnsureUnchanged(t, database, snapshot)
}

func TestDKIMEnsure_Usage(t *testing.T) {
	for _, args := range [][]string{
		{"dkim-key"}, {"dkim-key", "rotate"}, {"dkim-key", "ensure"},
		{"dkim-key", "ensure", "--domain", "example.test", "extra"},
		{"dkim-key", "ensure", "--domain", "bad_domain.test"},
		{"dkim-key", "ensure", "--domain", "example.test\nPRIVATE-FLAG-SENTINEL"},
		{"dkim-key", "ensure", "--unknown=PRIVATE-FLAG-SENTINEL"},
		{"dkim-key", "ensure", "--domain", "EXAMPLE.test"},
		{"dkim-key", "ensure", "--domain", "example.test."},
	} {
		status, out, errOut := run(args...)
		if status != exitUsage || out != "" || errorCodeIn(t, errOut) != "bad_flag" {
			t.Fatal("invalid usage accepted")
		}
		if strings.Contains(errOut, "PRIVATE-FLAG-SENTINEL") {
			t.Fatal("argument echoed")
		}
	}
	status, out, errOut := run("help")
	if status != exitOK || errOut != "" || !strings.Contains(out, "dkim-key ensure --domain NAME") {
		t.Fatal("missing command help")
	}
}

// A subprocess catches writes through GORM's process-global stdout logger as
// well as the command's writers, including when debug logging is requested.
func TestDKIMEnsure_RealOutputContainsOnlyPublicJSON(t *testing.T) {
	if os.Getenv("RESTMAIL_DKIM_ENSURE_SUBPROCESS") == "1" {
		os.Exit(runOperator([]string{"dkim-key", "ensure", "--domain", "example.test"}, os.Stdout, os.Stderr))
	}
	database := dkimEnsureFixture(t)
	if err := database.Create(&models.Domain{Name: "example.test"}).Error; err != nil {
		t.Fatal(err)
	}
	invoke := func(success bool) string {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestDKIMEnsure_RealOutputContainsOnlyPublicJSON$")
		cmd.Env = append(os.Environ(), "RESTMAIL_DKIM_ENSURE_SUBPROCESS=1", "RESTMAIL_OPERATOR_DEBUG=1")
		var out, errOut bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errOut
		err := cmd.Run()
		if success {
			if err != nil || errOut.Len() != 0 || !json.Valid(out.Bytes()) {
				t.Fatal("real process did not emit only public JSON")
			}
		} else {
			if err == nil || out.Len() != 0 || errorCodeIn(t, errOut.String()) != "dkim_ensure" {
				t.Fatal("real process did not emit one redacted error")
			}
		}
		for _, secret := range []string{ensureTestMaster, models.DKIMKeyCipherPrefix, "PRIVATE KEY", "PRIVATE-DRIVER-SENTINEL", "SQLSTATE", "UPDATE domains", "SELECT"} {
			if strings.Contains(out.String()+errOut.String(), secret) {
				t.Fatal("real process leaked confidential material or diagnostics")
			}
		}
		return out.String()
	}
	var first dkimEnsureResult
	if err := json.Unmarshal([]byte(invoke(true)), &first); err != nil || !first.Created {
		t.Fatal("subprocess did not create key")
	}
	domain := readEnsureDomain(t, database)
	if strings.Contains(invoke(true), domain.DKIMPrivateKey) {
		t.Fatal("ciphertext leaked")
	}
	// A different domain with existing private material must remain untouched.
	if err := database.Create(&models.Domain{Name: "other.test", DKIMSelector: "keep", DKIMPrivateKey: "UNRELATED-PRIVATE-SENTINEL"}).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := operatorDatabaseSnapshot(t, database)
	invoke(true)
	assertEnsureUnchanged(t, database, snapshot)
	if err := database.Exec("UPDATE domains SET dkim_selector = NULL, dkim_private_key = NULL WHERE name = ?", "example.test").Error; err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`CREATE FUNCTION output_dkim_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'PRIVATE-DRIVER-SENTINEL %', NEW.dkim_private_key; END $$`,
		`CREATE TRIGGER output_dkim_guard BEFORE UPDATE ON domains FOR EACH ROW EXECUTE FUNCTION output_dkim_guard()`,
	} {
		if err := database.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	snapshot = operatorDatabaseSnapshot(t, database)
	invoke(false)
	assertEnsureUnchanged(t, database, snapshot)
}
