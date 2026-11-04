package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/crypto"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// runMainEnv makes the test binary run the command itself, so a test can read
// its exit status and what it did to the database.
const runMainEnv = "ROTATE_KEY_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// A new key the server would refuse must be refused before any row changes:
// otherwise the data ends up under a key no production service starts with
// (#1913). A key at the floor runs, so the refusal is the key's doing and not
// a database the command could not reach.
func TestMain_RefusesNewKeyTheServerWouldRefuse(t *testing.T) {
	const oldKey = "rotate-test-old-master-key"
	floor := strings.Repeat("n", config.MinMasterKeyLength)
	cases := []struct {
		name, newKey, wantRefusal string
		wantRotated               bool
	}{
		{"short", "tiny-key", "MASTER_KEY is too short (8 bytes)", false},
		{"one under the floor", floor[1:], "MASTER_KEY is too short", false},
		{"at the floor", floor, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			database := openRotateTestDB(t)
			const directory = "https://acme.rotate-test.example/directory"
			stored, err := crypto.EncryptString("account-key", oldKey)
			if err != nil {
				t.Fatal(err)
			}
			if err := database.Create(&models.ACMEAccount{Directory: directory, KeyPEM: stored}).Error; err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command(os.Args[0])
			cmd.Env = append(os.Environ(),
				runMainEnv+"=1",
				"ENVIRONMENT=development",
				"DB_HOST="+envOr("DB_HOST", "localhost"),
				"DB_PORT="+envOr("DB_PORT", "5432"),
				"DB_NAME="+database.Migrator().CurrentDatabase(),
				"DB_USER="+envOr("DB_USER", "restmail"),
				"DB_PASS="+envOr("DB_PASS", "restmail"),
				"ROTATE_OLD_MASTER_KEY="+oldKey,
				"ROTATE_NEW_MASTER_KEY="+tc.newKey,
			)
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			runErr := cmd.Run()

			var got models.ACMEAccount
			if err := database.Where("directory = ?", directory).First(&got).Error; err != nil {
				t.Fatal(err)
			}
			if tc.wantRotated {
				if runErr != nil {
					t.Fatalf("a key at the floor was not accepted: %v", runErr)
				}
				if plain, err := crypto.DecryptString(got.KeyPEM, tc.newKey); err != nil || plain != "account-key" {
					t.Fatalf("the row did not move to the new key: %v", err)
				}
				return
			}
			var exit *exec.ExitError
			if !errors.As(runErr, &exit) {
				t.Fatalf("exit = %v, want a refusal", runErr)
			}
			if !strings.Contains(output.String(), tc.wantRefusal) {
				t.Fatal("the refusal does not say why")
			}
			if got.KeyPEM != stored {
				t.Fatal("the refused run changed a row")
			}
			if strings.Contains(output.String(), tc.newKey) {
				t.Fatal("the refusal printed the key")
			}
		})
	}
}
