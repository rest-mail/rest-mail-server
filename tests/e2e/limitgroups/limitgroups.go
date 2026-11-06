// Package limitgroups names the limits the limits suite (tests/e2e/limits_test.go)
// knows how to spend, and checks a description's declared 429s against them.
//
// The suite runs on a whole topology, which a pull request usually skips, so a new
// 429 it cannot place used to surface only in a dispatched E2E run. Keeping the
// check here lets internal/api run it on the generated description, with no
// servers (#1942).
package limitgroups

import (
	"fmt"
	"sort"
)

// The limit groups, by the description of the 429 their routes declare. A 429
// with any other description is a limit the suite does not know yet.
const (
	AuthThrottleShared  = "rate_limited: too many requests to the shared authentication throttle"
	AuthThrottleGrants  = "rate_limited: login's per-IP limit"
	OwnerThrottleGroup  = "rate_limited: too many requests to the owner endpoints' throttle"
	TLSRPTThrottleGroup = "rate_limited: too many reports from one address"
	APIKeyRefusalGroup  = "rate_limited: refused API keys, throttled at login's rate"
	// PasswordRefusalGroup is a gateway opening a mail client's session with a
	// refused password (#1586), throttled at login's rate.
	PasswordRefusalGroup = "rate_limited: refused passwords, throttled at login's rate"
	// HandlerLimitGroup is the bare status text: what a route says when it
	// lists 429 among its errors. The limit is its handler's (HandlerLimits),
	// or else the refused API keys' throttle in front of the admin routes.
	HandlerLimitGroup = "Too Many Requests"
)

// HandlerLimits are the operations whose handler answers 429 itself, and the
// limit each spends: the account's send rate (#184), the address's recovery
// failures (#124), and the recovery emails asked for an address.
var HandlerLimits = map[string]string{
	"POST /api/v1/messages/send":                    "sends",
	"POST /api/v1/messages/draft/{id}/send":         "sends",
	"POST /api/v1/messages/{id}/forward":            "sends",
	"POST /api/v1/messages/{id}/unsubscribe/mailto": "sends",
	"POST /api/v1/auth/recover":                     "recovery",
	"POST /api/v1/auth/recover/email":               "recovery email requests",
}

var known = map[string]bool{
	AuthThrottleShared: true, AuthThrottleGrants: true, OwnerThrottleGroup: true,
	TLSRPTThrottleGroup: true, APIKeyRefusalGroup: true, PasswordRefusalGroup: true,
	HandlerLimitGroup: true,
}

// Problems places every declared 429 in a limit the suite spends, and says what
// it could not place. limits maps a 429's description to the operations declaring
// it, each written "METHOD /path" in sorted order.
func Problems(limits map[string][]string) []string {
	var problems []string
	for description, operations := range limits {
		if !known[description] {
			problems = append(problems, fmt.Sprintf("%d operations declare a 429 the limits suite does not spend (%q), e.g. %s: add the limit to tests/e2e/limits_test.go",
				len(operations), description, operations[0]))
		}
	}
	for _, operation := range limits[HandlerLimitGroup] {
		if HandlerLimits[operation] == "" {
			problems = append(problems, fmt.Sprintf("%s declares the bare 429, and no handler of its limits it: describe the limit where it is applied", operation))
		}
	}
	sort.Strings(problems)
	return problems
}
