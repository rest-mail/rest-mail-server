package queue

import (
	"errors"
	"io"
	"net"
	"strconv"
	"strings"

	"github.com/rest-mail/go-mtasts"
	"github.com/restmail/restmail/internal/db/models"
)

// ClassifyFailure says what kind of failure a delivery attempt's error was,
// which decides whether the message bounces at once or how it is retried. It is
// the one place that reads a remote's refusal, so anything that acts on the
// kind of failure (a suppression list of dead addresses, say) reads it here or
// from the category stored on the queue row.
//
// A reply from the remote is classified by its RFC 3463 enhanced status code
// where that says enough, by its text where no code exists for the case
// (greylisting, rate limits), and by the basic SMTP code otherwise. Whether it
// is permanent at all follows the basic code, as RFC 5321 has clients do, with
// one exception: a full mailbox is transient even when sent as a 5xx (RFC 3463
// X.2.2). A failure with no reply is classified by what failed: DNS, TLS, or
// the connection.
func ClassifyFailure(err error) models.FailureCategory {
	if reply := smtpErrorFrom(err); reply != nil && reply.Code >= 400 && reply.Code < 600 {
		return classifyReply(reply.Code, reply.Enhanced, reply.Message)
	}
	return classifyNoReply(err)
}

func classifyReply(code int, enhanced, text string) models.FailureCategory {
	permanent := code >= 500
	text = strings.ToLower(text)

	// RFC 7504: the host, or the domain, accepts no mail at all.
	if code == 521 || code == 556 {
		return models.FailureBadDomain
	}
	// No enhanced code says greylisting or a rate limit: the text does. Only a
	// transient reply can be either; a permanent one is a refusal whatever it
	// says.
	if !permanent {
		switch {
		case containsAny(text, greylistPhrases):
			return models.FailureGreylisted
		case containsAny(text, rateLimitPhrases):
			return models.FailureRateLimited
		}
	}
	if subject, detail, ok := parseEnhanced(enhanced); ok {
		if c := byStatusCode(permanent, subject, detail); c != "" {
			return c
		}
	}
	// A refusal in words of both a policy and a dead mailbox is read as policy:
	// calling a live address dead, so that it is never mailed again, is the
	// worse mistake.
	switch {
	case containsAny(text, mailboxFullPhrases):
		return models.FailureMailboxFull
	case !permanent && containsAny(text, tlsPhrases):
		return models.FailureTLS
	case permanent && containsAny(text, policyPhrases):
		return models.FailurePolicy
	case permanent && containsAny(text, badMailboxPhrases):
		return models.FailureBadMailbox
	}
	switch code {
	case 551, 553: // user not local; mailbox name not allowed
		return models.FailureBadMailbox
	case 552: // exceeded storage allocation, unless it is the message that is too big
		if !containsAny(text, messageSizePhrases) {
			return models.FailureMailboxFull
		}
	case 530: // authentication, or TLS, required
		return models.FailurePolicy
	}
	if permanent {
		return models.FailureOtherPermanent
	}
	return models.FailureOtherTransient
}

// byStatusCode maps an RFC 3463 subject and detail to a category, or "" when
// the code alone does not say (X.0.0, most 4.7.x).
func byStatusCode(permanent bool, subject, detail int) models.FailureCategory {
	switch {
	case subject == 2 && detail == 2: // mailbox full
		return models.FailureMailboxFull
	case !permanent:
		switch {
		case subject == 1 && detail == 2: // the destination domain did not resolve, for now
			return models.FailureDNS
		case subject == 4 && (detail == 1 || detail == 2): // no answer from host; bad connection
			return models.FailureConnection
		case subject == 4 && detail == 3: // directory (DNS) server failure
			return models.FailureDNS
		case subject == 4 && detail == 5: // mail system congestion
			return models.FailureRateLimited
		}
	case subject == 1:
		switch detail {
		// Bad, malformed or ambiguous destination mailbox; mailbox moved. X.1.10 is
		// RFC 7505's null MX, but a server that answered cannot have one: Exchange
		// Online sends 5.1.10 for a recipient it does not know.
		case 1, 3, 4, 6, 10:
			return models.FailureBadMailbox
		case 2: // bad destination system
			return models.FailureBadDomain
		case 7, 8: // our sender address, or its domain, refused
			return models.FailurePolicy
		}
	case subject == 4:
		switch detail {
		case 1: // Exchange Online's "Recipient address rejected: Access denied"
			return models.FailureBadMailbox
		case 4: // unable to route: the domain does not resolve
			return models.FailureBadDomain
		}
	case subject == 7: // security or policy
		return models.FailurePolicy
	}
	return ""
}

// parseEnhanced reads an RFC 3463 status code ("5.1.1") into its subject and
// detail. The class is not returned: the basic reply code decides permanence.
func parseEnhanced(enhanced string) (subject, detail int, ok bool) {
	parts := strings.Split(enhanced, ".")
	if len(parts) != 3 || (parts[0] != "2" && parts[0] != "4" && parts[0] != "5") {
		return 0, 0, false
	}
	subject, err1 := strconv.Atoi(parts[1])
	detail, err2 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return subject, detail, true
}

func classifyNoReply(err error) models.FailureCategory {
	var dnsErr *net.DNSError
	var enforceErr *mtasts.EnforceError
	var netErr net.Error
	switch {
	case errors.As(err, &dnsErr):
		// The domain, or its mail host, does not exist: no retry brings it back.
		if dnsErr.IsNotFound {
			return models.FailureBadDomain
		}
		return models.FailureDNS
	case errors.As(err, &enforceErr): // MTA-STS refused a host without verified TLS
		return models.FailureTLS
	case smtpErrorFrom(err) != nil:
		// A command that got no reply: the conversation broke off.
		return models.FailureConnection
	case errors.As(err, &netErr), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return models.FailureConnection
	}
	return models.FailureOtherTransient
}

func containsAny(text string, phrases []string) bool {
	for _, p := range phrases {
		if strings.Contains(text, p) {
			return true
		}
	}
	return false
}

// The phrases replies use where no status code says it, lower case.
var (
	greylistPhrases  = []string{"greylist", "graylist", "grey-list", "gray-list"}
	rateLimitPhrases = []string{
		"rate limit", "rate-limit", "ratelimit", "at a rate", "unusual rate", "too many",
		"too quickly", "too fast", "throttl", "unexpected volume", "concurrent", "server busy",
	}
	mailboxFullPhrases = []string{
		"mailbox full", "mailbox is full", "inbox is full", "over quota", "quota exceeded",
		"exceeded storage", "out of storage",
	}
	tlsPhrases        = []string{"starttls", "tls required", "requires tls", "must use tls"}
	badMailboxPhrases = []string{
		"does not exist", "doesn't exist", "no such user", "user unknown", "unknown user",
		"recipient not found", "recipnotfound", "recipientnotfound", "mailbox unavailable",
		"mailbox not found", "invalid recipient", "doesn't have a", "does not have a",
		"disabled", "inactive",
	}
	policyPhrases = []string{
		"spam", "block", "blacklist", "listed", "reputation", "policy", "dmarc", "spf",
		"dkim", "unauthenticated", "denied",
	}
	messageSizePhrases = []string{"size", "too large", "too big"}
)
