package handlers

import (
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type validationDBBoundary struct{}

// runToValidationDB stops at the expected database boundary without a live
// connection. Unexpected panics still fail instead of passing as validation.
func runToValidationDB(t *testing.T, table string, call func(*gorm.DB)) (reached bool) {
	t.Helper()
	db, err := gorm.Open(postgres.Open("host=127.0.0.1 port=1 user=unused dbname=unused sslmode=disable"), &gorm.Config{
		DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	stop := func(tx *gorm.DB) {
		if tx.Statement.Table != table {
			t.Fatalf("reached table %q, want %q", tx.Statement.Table, table)
		}
		panic(validationDBBoundary{})
	}
	if err := db.Callback().Query().Before("gorm:query").Register("validation-boundary", stop); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Create().Before("gorm:create").Register("validation-boundary", stop); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if value := recover(); value != nil {
			if _, ok := value.(validationDBBoundary); !ok {
				panic(value)
			}
			reached = true
		}
	}()
	call(db)
	return reached
}
