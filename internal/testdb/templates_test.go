package testdb

import (
	"os"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/config"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"gorm.io/gorm"
)

func TestMain(tests *testing.M) {
	os.Exit(Finish(tests.Run()))
}

type templateProofRow struct {
	ID    uint `gorm:"primaryKey"`
	Value string
}

type templateProofExtendedRow struct {
	ID      uint `gorm:"primaryKey"`
	Value   string
	Payload string
}

func (templateProofExtendedRow) TableName() string { return "template_proof_rows" }

func templateProofConfig(t *testing.T) *config.Config {
	t.Helper()
	if os.Getenv("DB_HOST") == "" {
		t.Skip("set DB_HOST for PostgreSQL template proofs")
	}
	port, err := strconv.Atoi(os.Getenv("DB_PORT"))
	if err != nil {
		t.Fatal(err)
	}
	return &config.Config{
		DBHost: os.Getenv("DB_HOST"), DBPort: port, DBName: os.Getenv("DB_NAME"),
		DBUser: os.Getenv("DB_USER"), DBPass: os.Getenv("DB_PASS"), DBMaxIdleConns: 2,
	}
}

func TestTemplatesReuseSetupAndIsolateRowsAndSchema(t *testing.T) {
	cfg := templateProofConfig(t)
	var migrations atomic.Int64
	setup := []Setup{Models(&templateProofRow{}), {Name: t.Name(), Run: func(*gorm.DB) error {
		migrations.Add(1)
		return nil
	}}}
	first := Clone(t, cfg, "templateproof", setup...)
	if err := first.Create(&templateProofRow{Value: "only in the first clone"}).Error; err != nil {
		t.Fatal(err)
	}
	second := Clone(t, cfg, "templateproof", setup...)
	var rows int64
	if err := second.Model(&templateProofRow{}).Count(&rows).Error; err != nil || rows != 0 {
		t.Fatalf("row isolation: rows %d, error %v", rows, err)
	}
	if err := first.Migrator().DropTable(&templateProofRow{}); err != nil {
		t.Fatal(err)
	}
	third := Clone(t, cfg, "templateproof", setup...)
	if !third.Migrator().HasTable(&templateProofRow{}) || !second.Migrator().HasTable(&templateProofRow{}) {
		t.Fatal("dropping a table in one clone changed another clone or its template")
	}
	if count := migrations.Load(); count != 1 {
		t.Fatalf("migration set ran %d times, want once", count)
	}
}

func TestTemplatesKeyIncludesModelsAndSetupOrder(t *testing.T) {
	cfg := templateProofConfig(t)
	plain := Clone(t, cfg, "templateproof", Models(&templateProofRow{}))
	extended := Clone(t, cfg, "templateproof", Models(&templateProofExtendedRow{}))
	if plain.Migrator().HasColumn(&templateProofRow{}, "payload") || !extended.Migrator().HasColumn(&templateProofExtendedRow{}, "payload") {
		t.Fatal("different migration sets reused one template")
	}
	var calls atomic.Int64
	first := Setup{Name: t.Name() + ":first", Run: func(*gorm.DB) error { calls.Add(1); return nil }}
	second := Setup{Name: t.Name() + ":second", Run: func(*gorm.DB) error { calls.Add(1); return nil }}
	Clone(t, cfg, "templateproof", first, second)
	Clone(t, cfg, "templateproof", second, first)
	if count := calls.Load(); count != 4 {
		t.Fatalf("ordered setups ran %d steps, want four", count)
	}
}

func TestTemplatesParallelCopiesMigrateOnceAndAreDropped(t *testing.T) {
	cfg := templateProofConfig(t)
	admin, err := rmdb.Connect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ReuseConnections(t, admin)
	var migrations atomic.Int64
	names := make(chan string, 4)
	t.Run("copies", func(t *testing.T) {
		for range 4 {
			t.Run("copy", func(t *testing.T) {
				t.Parallel()
				database := Clone(t, cfg, "templateproof", Models(&templateProofRow{}), Setup{
					Name: "parallel-template-proof",
					Run:  func(*gorm.DB) error { migrations.Add(1); return nil },
				})
				var name string
				if err := database.Raw("SELECT current_database()").Scan(&name).Error; err != nil {
					t.Fatal(err)
				}
				names <- name
			})
		}
	})
	close(names)
	seen := map[string]bool{}
	for name := range names {
		if seen[name] {
			t.Fatalf("parallel tests shared database %s", name)
		}
		seen[name] = true
		var remaining int64
		if err := admin.Raw("SELECT count(*) FROM pg_database WHERE datname = ?", name).Scan(&remaining).Error; err != nil || remaining != 0 {
			t.Fatalf("clone cleanup: %s, remaining %d, error %v", name, remaining, err)
		}
	}
	if migrations.Load() != 1 || len(seen) != 4 {
		t.Fatalf("parallel setup: migrations %d, distinct clones %d", migrations.Load(), len(seen))
	}
}
