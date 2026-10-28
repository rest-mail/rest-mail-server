package examplepairs

import (
	"encoding/json"
	"os"
	"sort"
	"sync"
	"testing"
)

// ProofsEnv names the file the in-process example tests write the pairs they proved to
// (#1237). Unset, nothing is written: a run of a few tests proves a few pairs, and only
// the whole suite, as CI runs it, says what the in-process tests prove.
const ProofsEnv = "RESTMAIL_EXAMPLE_PROOFS"

var (
	mu     sync.Mutex
	proven = map[string]map[string]bool{}
)

// Proven records that the pair name of operation op is proven when t passes. A test
// calls it at the start of the subtest that sends the pair and checks the answer.
func Proven(t testing.TB, op, name string) {
	t.Helper()
	t.Cleanup(func() {
		if t.Failed() || t.Skipped() {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if proven[op] == nil {
			proven[op] = map[string]bool{}
		}
		proven[op][name] = true
	})
}

// WriteProofs writes the pairs proven so far, by operation, to the file ProofsEnv
// names, merging what an earlier package's run wrote there.
func WriteProofs() error {
	path := os.Getenv(ProofsEnv)
	if path == "" {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()
	all := map[string][]string{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &all); err != nil {
			return err
		}
	}
	for op, names := range proven {
		seen := map[string]bool{}
		for _, name := range all[op] {
			seen[name] = true
		}
		for name := range names {
			seen[name] = true
		}
		list := make([]string, 0, len(seen))
		for name := range seen {
			list = append(list, name)
		}
		sort.Strings(list)
		all[op] = list
	}
	out, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}
