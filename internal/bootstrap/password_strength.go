package bootstrap

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	zxcvbn "github.com/ccojocar/zxcvbn-go"
)

// A chosen password is a password a person picks for an account: at creation,
// a change, a reset, a recovery, an invitation, or the operator's
// reset-password. Generated secrets (app passwords, API keys, the installer's
// bootstrap password) never come here. Every one of those paths asks
// ChosenPasswordProblem or CheckChosenPassword, so there is one rule.
//
// The rule: at least MinChosenPasswordLength characters, a zxcvbn score of at
// least MinChosenPasswordScore, and nothing built from the account's own
// address. zxcvbn (Dropbox's estimator, here the pure-Go port
// github.com/ccojocar/zxcvbn-go) matches the password against bundled lists of
// common passwords, English words, names and surnames, keyboard patterns,
// repeats, sequences, dates and l33t substitutions, and estimates how many
// guesses an attacker needs. It runs offline: no password, hash or prefix of
// one leaves the server.

// MinChosenPasswordScore is the lowest zxcvbn score a chosen password may
// have. This port scores the estimated time to crack at 10,000 guesses a
// second: score 3 is at least 10^6 seconds, about 10^10 guesses, which zxcvbn
// calls "safely unguessable": out of reach online, and days of work offline
// against a stolen bcrypt hash per account. Score 2 starts at 10^8 guesses,
// hours offline; 4 would refuse many good passphrases of three or four
// uncommon words.
const MinChosenPasswordScore = 3

// maxScoredRunes is how much of a password zxcvbn reads. Its matching grows
// faster than linearly with length; anything past this many characters only
// adds strength, so the prefix alone decides.
const maxScoredRunes = 100

// minOwnNameRunes is the shortest local part or domain that a password may not
// contain. A one- or two-letter local part would otherwise refuse every
// password with those letters in it; zxcvbn still scores them as user input.
const minOwnNameRunes = 3

const (
	tooShortProblem  = "must be at least %d characters"
	guessableProblem = "is too easy to guess: use a longer passphrase of unrelated words, with nothing from the account's address"
	ownNameProblem   = "is too easy to guess: it contains the account's own address"
)

var (
	// ErrPasswordTooShort: a chosen password under MinChosenPasswordLength.
	ErrPasswordTooShort = fmt.Errorf("the password must be at least %d characters", MinChosenPasswordLength)
	// ErrPasswordGuessable: a chosen password long enough but too easy to
	// guess, or built from the account's own address. The message says why
	// and never repeats the password.
	ErrPasswordGuessable = errors.New("the password " + guessableProblem)
)

// PasswordTooShort reports whether password has fewer than
// MinChosenPasswordLength characters. Characters, not bytes: twelve Japanese
// or Chinese characters are 36 bytes, and four of them are not twelve.
func PasswordTooShort(password string) bool {
	return utf8.RuneCountInString(password) < MinChosenPasswordLength
}

// ChosenPasswordProblem is why password may not be chosen for the account at
// address, as a validation message for the password field, or "" when it may.
// address may be empty when the account is not known yet; the caller then
// checks again once it is.
func ChosenPasswordProblem(password, address string) string {
	if PasswordTooShort(password) {
		return fmt.Sprintf(tooShortProblem, MinChosenPasswordLength)
	}
	inputs := ownNames(address)
	lower := strings.ToLower(password)
	for _, name := range inputs {
		if utf8.RuneCountInString(name) >= minOwnNameRunes && strings.Contains(lower, name) {
			return ownNameProblem
		}
	}
	scored := password
	if utf8.RuneCountInString(scored) > maxScoredRunes {
		scored = string([]rune(scored)[:maxScoredRunes])
	}
	if zxcvbn.PasswordStrength(scored, inputs).Score < MinChosenPasswordScore {
		return guessableProblem
	}
	return ""
}

// CheckChosenPassword is ChosenPasswordProblem as an error: nil,
// ErrPasswordTooShort or ErrPasswordGuessable.
func CheckChosenPassword(password, address string) error {
	switch ChosenPasswordProblem(password, address) {
	case "":
		return nil
	case fmt.Sprintf(tooShortProblem, MinChosenPasswordLength):
		return ErrPasswordTooShort
	default:
		return ErrPasswordGuessable
	}
}

// ownNames are the parts of an address a password must not be built from:
// the address, its local part and its parts split at . _ - +, the domain and
// its labels. Lower case; the public suffix label (com, org) is left out.
func ownNames(address string) []string {
	address = strings.ToLower(strings.TrimSpace(address))
	if address == "" {
		return nil
	}
	names := []string{address}
	local, domain, found := strings.Cut(address, "@")
	if local != "" {
		names = append(names, local)
		if parts := splitName(local); len(parts) > 1 {
			names = append(names, parts...)
		}
	}
	if found && domain != "" {
		names = append(names, domain)
		labels := strings.Split(domain, ".")
		if len(labels) > 1 {
			labels = labels[:len(labels)-1]
		}
		for _, label := range labels {
			if label != "" && label != domain {
				names = append(names, label)
			}
		}
	}
	return names
}

func splitName(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == '.' || r == '_' || r == '-' || r == '+'
	})
}
