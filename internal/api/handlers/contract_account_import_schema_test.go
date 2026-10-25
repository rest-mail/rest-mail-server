package handlers

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/auth"
	yaml "go.yaml.in/yaml/v2"
)

// A length bound alone permits arbitrary strings, so the contract's valid
// maxLength probe used to send 256 characters that no password importer accepts.
// Describe the accepted shapes as well as the size cap, on both descriptions.
func TestCreateAccountImportedHashContract(t *testing.T) {
	data, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Pattern   string
					MaxLength int `yaml:"maxLength"`
				}
			}
		}
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	schema := doc.Components.Schemas["CreateAccountRequest"].Properties["password_hash"]
	field, ok := reflect.TypeOf(createAccountRequest{}).FieldByName("PasswordHash")
	if !ok || schema.Pattern == "" || schema.Pattern != field.Tag.Get("pattern") {
		t.Fatal("the oracle and typed request must describe the same imported-hash pattern")
	}
	if schema.MaxLength != 256 || field.Tag.Get("maxLength") != "256" {
		t.Fatal("the imported-hash size cap must be described and enforced")
	}
	pattern, err := regexp.Compile(schema.Pattern)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, hash string
		valid      bool
	}{
		{"absent credential", "", true},
		{"bcrypt minimum cost", "$2b$04$" + strings.Repeat(".", 53), true},
		{"bcrypt maximum cost", "{BLF-CRYPT}$2y$14$" + strings.Repeat(".", 53), true},
		{"bcrypt old version", "$2a$12$" + strings.Repeat(".", 53), true},
		{"bcrypt cost too low", "$2b$03$" + strings.Repeat(".", 53), false},
		{"bcrypt cost too high", "$2b$15$" + strings.Repeat(".", 53), false},
		{"sha256 default rounds", "$5$salt$" + strings.Repeat(".", 43), true},
		{"sha256 minimum rounds", "{SHA256-CRYPT}$5$rounds=1000$salt$" + strings.Repeat(".", 43), true},
		{"sha256 maximum rounds", "$5$rounds=1000000$salt$" + strings.Repeat(".", 43), true},
		{"sha512 default rounds", accountImportSHA512, true},
		{"sha512 minimum rounds", "$6$rounds=1000$salt$" + strings.Repeat(".", 86), true},
		{"sha512 maximum rounds", "{SHA512-CRYPT}$6$rounds=1000000$abcdefghijklmnop$" + strings.Repeat(".", 86), true},
		{"sha rounds too low", "$6$rounds=999$salt$" + strings.Repeat(".", 86), false},
		{"sha rounds too high", "$6$rounds=1000001$salt$" + strings.Repeat(".", 86), false},
		{"sha rounds leading zero", "$6$rounds=01000$salt$" + strings.Repeat(".", 86), false},
		{"wrong scheme prefix", "{SHA256-CRYPT}$6$salt$" + strings.Repeat(".", 86), false},
		{"salt too long", "$6$abcdefghijklmnopq$" + strings.Repeat(".", 86), false},
		{"checksum too short", "$6$salt$" + strings.Repeat(".", 85), false},
		{"checksum invalid alphabet", "$6$salt$" + strings.Repeat("!", 86), false},
		{"argon2id", "{ARGON2ID}" + accountImportArgon2ID, true},
		{"argon2id raw", accountImportArgon2ID, true},
		{"argon2i", "{ARGON2I}" + strings.Replace(accountImportArgon2ID, "argon2id", "argon2i", 1), true},
		{"argon2 largest bounds", strings.Replace(accountImportArgon2ID, "m=4096,t=2,p=1", "m=262144,t=10,p=8", 1), true},
		{"argon2 memory too high", strings.Replace(accountImportArgon2ID, "m=4096", "m=262145", 1), false},
		{"argon2 too many passes", strings.Replace(accountImportArgon2ID, "t=2", "t=11", 1), false},
		{"argon2 too many lanes", strings.Replace(accountImportArgon2ID, "p=1", "p=9", 1), false},
		{"argon2 old version", strings.Replace(accountImportArgon2ID, "v=19", "v=16", 1), false},
		{"argon2d", strings.Replace(accountImportArgon2ID, "argon2id", "argon2d", 1), false},
		{"argon2 wrong scheme prefix", "{ARGON2I}" + accountImportArgon2ID, false},
		{"weak scheme", "{PLAIN}password", false},
		{"arbitrary string at maximum length", strings.Repeat("x", 256), false},
		{"over maximum length", strings.Repeat("x", 257), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pattern.MatchString(tc.hash); got != tc.valid {
				t.Fatalf("schema accepts hash = %v, want %v", got, tc.valid)
			}
			if tc.hash != "" {
				_, err := auth.NormalizeImportedPasswordHash(tc.hash)
				if (err == nil) != tc.valid {
					t.Fatalf("handler accepts hash = %v, want %v", err == nil, tc.valid)
				}
			}
		})
	}
}

// An Argon2id PHC string from OpenSSL (see internal/auth's fixtures).
const accountImportArgon2ID = "$argon2id$v=19$m=4096,t=2,p=1$aW1wb3J0c2FsdDAxMjM0NQ$JCLzuH2QfsNfwyNUzLd3IjMXXY7Rb46hqjBt5hs545I"
