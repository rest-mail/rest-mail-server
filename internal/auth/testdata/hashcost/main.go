// Command hashcost prints what a binary built without go test does with the
// stored-hash cost. internal/auth's tests build and run it, because only such a
// binary can show the cost production gets.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"golang.org/x/crypto/bcrypt"
)

func cost(hash string, err error) int {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	got, err := bcrypt.Cost([]byte(strings.TrimPrefix(hash, "{BLF-CRYPT}")))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return got
}

func main() {
	report := map[string]interface{}{}
	func() {
		defer func() { report["lowering_panics"] = recover() != nil }()
		auth.UseMinimumHashCostInTests()
	}()
	report["password_cost"] = cost(auth.HashPassword("password"))
	report["recovery_code_cost"] = cost(auth.HashRecoveryCode("abcd-efgh"))
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
