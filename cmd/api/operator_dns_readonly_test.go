package main

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// Include every user table and its contents, so schema changes, RBAC seeds and
// unrelated token/session writes all change the snapshot. Only hashes leave SQL.
func operatorDatabaseSnapshot(t *testing.T, database *gorm.DB) string {
	t.Helper()
	var tables []string
	if err := database.Raw(`SELECT tablename FROM pg_tables WHERE schemaname = 'public' ORDER BY tablename`).Scan(&tables).Error; err != nil {
		t.Fatal(err)
	}
	var snapshot strings.Builder
	var schema string
	if err := database.Raw(`SELECT md5(COALESCE(string_agg(
  concat_ws(':', c.relname, c.relkind, a.attname, a.atttypid, a.attnotnull, pg_get_expr(d.adbin, d.adrelid)),
  ',' ORDER BY c.relname, a.attnum), ''))
  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
  LEFT JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
  LEFT JOIN pg_attrdef d ON d.adrelid = c.oid AND d.adnum = a.attnum
  WHERE n.nspname = 'public'`).Scan(&schema).Error; err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&snapshot, "schema:%s\n", schema)
	for _, table := range tables {
		var digest string
		query := fmt.Sprintf(`SELECT md5(COALESCE(string_agg(row_data, ',' ORDER BY row_data), '')) FROM (SELECT row_to_json(t)::text AS row_data FROM %q t) rows`, table)
		if err := database.Raw(query).Scan(&digest).Error; err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&snapshot, "%s:%s\n", table, digest)
	}
	return snapshot.String()
}

func TestDNSRecords_MissingSchemaDoesNotInitialize(t *testing.T) {
	database := scratchDatabase(t)
	before := operatorDatabaseSnapshot(t, database)
	status, out, errOut := run("dns-records", "--mail-host", "mail.example.com")
	if status != exitFailed || out != "" || errorCodeIn(t, errOut) != "database" {
		t.Fatalf("missing schema: status %d, want controlled database failure with no stdout", status)
	}
	if after := operatorDatabaseSnapshot(t, database); before != after {
		t.Fatal("dns-records created schema or seeded data in an empty database")
	}
}

func TestDNSRecords_EnforcesReadOnlyTransaction(t *testing.T) {
	database := scratchDatabase(t)
	if err := database.Migrator().CreateTable(&models.Domain{}); err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&models.Domain{Name: "example.com"}).Error; err != nil {
		t.Fatal("seed domain failed")
	}
	// This connection is deliberately writable: a session default cannot hide
	// removal of the command's explicit ReadOnly transaction option.
	var defaultReadOnly string
	if err := database.Raw("SHOW default_transaction_read_only").Scan(&defaultReadOnly).Error; err != nil {
		t.Fatal(err)
	}
	if defaultReadOnly != "off" {
		t.Fatal("fixture requires a writable session default")
	}
	for _, query := range []string{
		`ALTER TABLE domains RENAME TO dns_fixture_domains`,
		`CREATE FUNCTION dns_readonly_guard() RETURNS boolean LANGUAGE plpgsql AS $$
   BEGIN
    IF current_setting('transaction_read_only') <> 'on' THEN
     RAISE EXCEPTION 'dns-records did not start a read-only transaction';
    END IF;
    BEGIN
     UPDATE dns_fixture_domains SET name = name;
     RAISE EXCEPTION 'dns-records allowed a data write';
    EXCEPTION WHEN read_only_sql_transaction THEN NULL;
    END;
    BEGIN
     CREATE TABLE dns_forbidden_schema (id integer);
     RAISE EXCEPTION 'dns-records allowed a schema write';
    EXCEPTION WHEN read_only_sql_transaction THEN NULL;
    END;
    RETURN true;
   END $$`,
		`CREATE VIEW domains AS SELECT *, dns_readonly_guard() AS readonly_guard FROM dns_fixture_domains`,
	} {
		if err := database.Exec(query).Error; err != nil {
			t.Fatal(err)
		}
	}
	before := operatorDatabaseSnapshot(t, database)
	status, out, errOut := run("dns-records", "--mail-host", "mail.example.com")
	if status != exitOK || !strings.Contains(out, `"domain":"example.com"`) || errOut != "" {
		t.Fatalf("database read-only guard rejected command: status %d, stderr %s", status, errOut)
	}
	if after := operatorDatabaseSnapshot(t, database); before != after {
		t.Fatal("command changed database state")
	}
}

func setOperatorDatabaseReadOnly(t *testing.T, database *gorm.DB) {
	t.Helper()
	// scratchDatabase generates this identifier itself; never alter a shared DB.
	name := os.Getenv("DB_NAME")
	if !strings.HasPrefix(name, "restmail_operator_") {
		t.Fatal("refusing non-scratch database")
	}
	if err := database.Exec(fmt.Sprintf(`ALTER DATABASE %q SET default_transaction_read_only = on`, name)).Error; err != nil {
		t.Fatal(err)
	}
}
