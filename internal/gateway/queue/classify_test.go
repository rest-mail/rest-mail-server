package queue

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strings"
	"syscall"
	"testing"

	"github.com/rest-mail/go-mtasts/pkg/mtasts"
	"github.com/restmail/restmail/internal/db/models"
)

// wireReply reads an SMTP reply as it comes off the wire, one line per
// element, the way net/smtp does: a multiline reply's lines become one
// *textproto.Error.
func wireReply(t *testing.T, lines ...string) *textproto.Error {
	t.Helper()
	r := textproto.NewReader(bufio.NewReader(strings.NewReader(strings.Join(lines, "\r\n") + "\r\n")))
	_, _, err := r.ReadResponse(250)
	var reply *textproto.Error
	if !errors.As(err, &reply) {
		t.Fatalf("not a reply: %v", err)
	}
	return reply
}

// asRcptRefusal is the error deliver() returns when RCPT TO is refused: the
// reply parsed into an *SMTPError, wrapped once more on the way out.
func asRcptRefusal(reply *textproto.Error) error {
	se := parseSMTPError(reply)
	se.RemoteMTA = "mx.remote.test"
	return fmt.Errorf("all MX hosts failed: %w", error(se))
}

// Real replies from the large providers, as their published error references
// and their bounces give them (addresses, IPs and trailing IDs replaced), and
// from the software most other mail servers run.
func TestClassifyFailure_Replies(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  models.FailureCategory
	}{
		// Gmail.
		{"gmail no such user", []string{
			"550-5.1.1 The email account that you tried to reach does not exist. Please try",
			"550-5.1.1 double-checking the recipient's email address for typos or",
			"550-5.1.1 unnecessary spaces. For more information, go to",
			"550 5.1.1  https://support.google.com/mail/?p=NoSuchUser a1si1234567qkb.12 - gsmtp",
		}, models.FailureBadMailbox},
		{"gmail disabled account", []string{
			"550-5.2.1 The email account that you tried to reach is disabled. Learn more at",
			"550 5.2.1  https://support.google.com/mail/?p=DisabledUser a1si1234567qkb.12 - gsmtp",
		}, models.FailureBadMailbox},
		{"gmail no such domain", []string{
			"553-5.1.2 We weren't able to find the recipient domain. Please check for any",
			"553-5.1.2 spelling errors, and make sure you didn't enter any spaces, periods,",
			"553 5.1.2 or other punctuation after the recipient's email address. a1si1234567qkb.12 - gsmtp",
		}, models.FailureBadDomain},
		{"gmail over quota, permanent code", []string{
			"552-5.2.2 The recipient's inbox is out of storage space. Please direct the",
			"552-5.2.2 recipient to",
			"552 5.2.2  https://support.google.com/mail/?p=OverQuotaPerm a1si1234567qkb.12 - gsmtp",
		}, models.FailureMailboxFull},
		{"gmail over quota, temporary code", []string{
			"452-4.2.2 The recipient's inbox is out of storage space and inbound mail",
			"452-4.2.2 cannot be delivered. Please direct the recipient to",
			"452 4.2.2  https://support.google.com/mail/?p=OverQuotaTemp a1si1234567qkb.12 - gsmtp",
		}, models.FailureMailboxFull},
		{"gmail recipient receiving too fast", []string{
			"450-4.2.1 The user you are trying to contact is receiving mail at a rate that",
			"450-4.2.1 prevents additional messages from being delivered. Please resend your",
			"450-4.2.1 message at a later time. If the user is able to receive mail at that",
			"450 4.2.1 time, your message will be delivered. a1si1234567qkb.12 - gsmtp",
		}, models.FailureRateLimited},
		// The permanent version of the same must never read as a dead mailbox:
		// the address is fine.
		{"gmail recipient receiving too fast, permanent code", []string{
			"550-5.2.1 The user you are trying to contact is receiving mail at a rate that",
			"550 5.2.1 prevents additional messages from being delivered. a1si1234567qkb.12 - gsmtp",
		}, models.FailureOtherPermanent},
		{"gmail rate limited sender", []string{
			"421-4.7.28 Our system has detected an unusual rate of unsolicited mail originating",
			"421-4.7.28 from your IP address. To protect our users from spam, mail sent from your IP",
			"421-4.7.28 address has been temporarily rate limited. For more information, go to",
			"421 4.7.28  https://support.google.com/mail/?p=UnsolicitedRateLimitError a1si1234567qkb.12 - gsmtp",
		}, models.FailureRateLimited},
		{"gmail blocked sender", []string{
			"550-5.7.1 Our system has detected an unusual rate of unsolicited mail originating",
			"550-5.7.1 from your IP address. To protect our users from spam, mail sent from your IP",
			"550 5.7.1 address has been blocked. a1si1234567qkb.12 - gsmtp",
		}, models.FailurePolicy},
		{"gmail unauthenticated sender", []string{
			"550-5.7.26 This mail has been blocked because the sender is unauthenticated.",
			"550-5.7.26 Gmail requires all senders to authenticate with either SPF or DKIM.",
			"550 5.7.26  a1si1234567qkb.12 - gsmtp",
		}, models.FailurePolicy},
		{"gmail starttls first", []string{
			"530 5.7.0 Must issue a STARTTLS command first. a1si1234567qkb.12 - gsmtp",
		}, models.FailurePolicy},
		{"gmail try again later", []string{
			"421-4.7.0 Try again later, closing connection. (EHLO)",
			"421 4.7.0  https://support.google.com/a/answer/3221692 a1si1234567qkb.12 - gsmtp",
		}, models.FailureOtherTransient},
		{"gmail temporarily rejected", []string{
			"451 4.3.0 Mail server temporarily rejected message. a1si1234567qkb.12 - gsmtp",
		}, models.FailureOtherTransient},

		// Outlook.com and Exchange Online.
		{"exchange recipient not found", []string{
			"550 5.1.10 RESOLVER.ADR.RecipientNotFound; Recipient not found by SMTP address lookup",
		}, models.FailureBadMailbox},
		{"exchange recip not found", []string{
			"550 5.1.1 RESOLVER.ADR.RecipNotFound; not found",
		}, models.FailureBadMailbox},
		{"exchange directory-based edge blocking", []string{
			"550 5.4.1 Recipient address rejected: Access denied. AS(201806281) [DB5EUR03FT012.eop-EUR03.prod.protection.outlook.com]",
		}, models.FailureBadMailbox},
		{"outlook.com mailbox unavailable", []string{
			"550 5.5.0 Requested action not taken: mailbox unavailable (S2017062302). [AM0PR01MB1234.eurprd01.prod.exchangelabs.com]",
		}, models.FailureBadMailbox},
		{"exchange blocklisted sender", []string{
			"550 5.7.1 Service unavailable, Client host [203.0.113.5] blocked using Spamhaus. To request removal from this list see https://www.spamhaus.org/query/ip/203.0.113.5 AS(1450) [BN8NAM12FT034.eop-nam12.prod.protection.outlook.com]",
		}, models.FailurePolicy},
		{"exchange banned sending ip", []string{
			"550 5.7.606 Access denied, banned sending IP [203.0.113.5]. To request removal from this list please visit https://sender.office.com/ and follow the directions. AS(1430) [BN8NAM12FT034.eop-nam12.prod.protection.outlook.com]",
		}, models.FailurePolicy},
		{"exchange dmarc reject", []string{
			"550 5.7.509 Access denied, sending domain example.com does not pass DMARC verification and has a DMARC policy of reject. [BN8NAM12FT034.eop-nam12.prod.protection.outlook.com]",
		}, models.FailurePolicy},
		{"exchange mailbox full", []string{
			"554 5.2.2 mailbox full; STOREDRV.Deliver.Exception:QuotaExceededException.MapiExceptionShutoffQuotaExceeded; Failed to process message due to a permanent exception",
		}, models.FailureMailboxFull},
		{"outlook.com rate limited on reputation", []string{
			"451 4.7.650 The mail server [203.0.113.5] has been temporarily rate limited due to IP reputation. For e-mail delivery information, see https://postmaster.live.com (S775) [AM0PR01MB1234.eurprd01.prod.exchangelabs.com]",
		}, models.FailureRateLimited},
		{"exchange server busy", []string{
			"451 4.7.500 Server busy. Please try again later from [203.0.113.5]. (AS761) [BN8NAM12FT034.eop-nam12.prod.protection.outlook.com]",
		}, models.FailureRateLimited},
		{"exchange too many connections", []string{
			"421 4.3.2 The maximum number of concurrent server connections has exceeded a limit, closing transmission channel",
		}, models.FailureRateLimited},
		{"exchange starttls required", []string{
			"451 5.7.3 STARTTLS is required to send mail [BN8NAM12FT034.eop-nam12.prod.protection.outlook.com]",
		}, models.FailureTLS},

		// Yahoo and AOL.
		{"yahoo no such user", []string{
			"554 delivery error: dd This user doesn't have a yahoo.com account (someone@yahoo.com) [0] - mta1234.mail.gq1.yahoo.com",
		}, models.FailureBadMailbox},
		{"yahoo disabled mailbox", []string{
			"554 delivery error: dd Sorry your message to someone@yahoo.com cannot be delivered. This mailbox is disabled (554.30). - mta1234.mail.gq1.yahoo.com",
		}, models.FailureBadMailbox},
		{"yahoo deferred for volume", []string{
			"421 4.7.0 [TSS04] Messages from 203.0.113.5 temporarily deferred due to unexpected volume or user complaints - 4.16.55.1; see https://postmaster.yahooinc.com/error-codes",
		}, models.FailureRateLimited},
		{"yahoo permanently deferred", []string{
			"553 5.7.2 [TSS09] All messages from 203.0.113.5 will be permanently deferred; Retrying will NOT succeed. See https://postmaster.yahooinc.com/error-codes",
		}, models.FailurePolicy},
		{"yahoo policy", []string{
			"554 5.7.9 Message not accepted for policy reasons.  See https://postmaster.yahooinc.com/error-codes",
		}, models.FailurePolicy},
		{"yahoo unresolvable sender domain", []string{
			"451 Message temporarily deferred due to unresolvable RFC.5321 from domain; see https://postmaster.yahooinc.com/error-codes",
		}, models.FailureOtherTransient},

		// Greylisting, as Postgrey and milter-greylist word it.
		{"postgrey", []string{
			"450 4.2.0 <someone@example.org>: Recipient address rejected: Greylisted, see http://postgrey.schweikert.ch/help/example.org.html",
		}, models.FailureGreylisted},
		{"milter-greylist", []string{
			"451 4.7.1 Greylisting in action, please come back in 00:05:00",
		}, models.FailureGreylisted},

		// Postfix.
		{"postfix user unknown", []string{
			"550 5.1.1 <nobody@example.org>: Recipient address rejected: User unknown in virtual mailbox table",
		}, models.FailureBadMailbox},
		{"postfix domain not found, for now", []string{
			"450 4.1.2 <someone@nowhere.example>: Recipient address rejected: Domain not found",
		}, models.FailureDNS},
		{"postfix relay denied", []string{
			"554 5.7.1 <someone@example.org>: Relay access denied",
		}, models.FailurePolicy},
		{"postfix message too big", []string{
			"552 5.3.4 Message size exceeds fixed limit",
		}, models.FailureOtherPermanent},
		{"postfix timeout", []string{
			"421 4.4.2 mx.example.org Error: timeout exceeded",
		}, models.FailureConnection},

		// Basic codes alone, worded as RFC 5321 has them.
		{"550 alone", []string{"550 Requested action not taken: mailbox unavailable"}, models.FailureBadMailbox},
		{"551 alone", []string{"551 User not local; please try <forward-path>"}, models.FailureBadMailbox},
		{"552 alone", []string{"552 Requested mail action aborted: exceeded storage allocation"}, models.FailureMailboxFull},
		{"552 for size", []string{"552 Message size exceeds maximum permitted"}, models.FailureOtherPermanent},
		{"553 alone", []string{"553 Requested action not taken: mailbox name not allowed"}, models.FailureBadMailbox},
		{"554 alone", []string{"554 Transaction failed"}, models.FailureOtherPermanent},
		// Words of a policy and of a dead mailbox both: never read as dead.
		{"blocked, and the mailbox unavailable", []string{
			"554 Your IP 203.0.113.5 is blocked: mailbox unavailable",
		}, models.FailurePolicy},
		{"451 alone", []string{"451 Requested action aborted: local error in processing"}, models.FailureOtherTransient},
		{"521 no mail here", []string{"521 mx.example.org does not accept mail"}, models.FailureBadDomain},
		{"556 null mx", []string{"556 5.1.10 Recipient address has null MX"}, models.FailureBadDomain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyFailure(asRcptRefusal(wireReply(t, tc.lines...))); got != tc.want {
				t.Errorf("ClassifyFailure = %q, want %q", got, tc.want)
			}
		})
	}
}

// A refusal at the greeting, to EHLO or to DATA reaches the worker as net/smtp
// returned it, not parsed: it is classified all the same.
func TestClassifyFailure_ReplyNetSMTPReturnedAsIs(t *testing.T) {
	greeting := wireReply(t, "554 5.7.1 Service unavailable; Client host [203.0.113.5] blocked using zen.spamhaus.org")
	err := fmt.Errorf("all MX hosts failed: %w", fmt.Errorf("SMTP client for mx.remote.test:25: %w", greeting))
	if got := ClassifyFailure(err); got != models.FailurePolicy {
		t.Errorf("greeting refusal: %q, want policy", got)
	}
	data := wireReply(t, "451 4.7.1 Greylisting in action, please come back in 00:05:00")
	err = fmt.Errorf("all MX hosts failed: %w", fmt.Errorf("DATA to mx.remote.test: %w", data))
	if got := ClassifyFailure(err); got != models.FailureGreylisted {
		t.Errorf("DATA refusal: %q, want greylisted", got)
	}
	// And the bounce reports its status, not a code-less one.
	if se := smtpErrorFrom(err); se == nil || se.Code != 451 || se.Enhanced != "4.7.1" {
		t.Errorf("smtpErrorFrom = %+v, want 451 4.7.1", se)
	}
}

func TestClassifyFailure_NoReply(t *testing.T) {
	notFound := &net.DNSError{Err: "no such host", Name: "nowhere.invalid", IsNotFound: true}
	cases := []struct {
		name string
		err  error
		want models.FailureCategory
	}{
		{"connection refused",
			fmt.Errorf("all MX hosts failed: %w", fmt.Errorf("connect to mx.remote.test:25: %w",
				&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED})),
			models.FailureConnection},
		{"dial timeout",
			fmt.Errorf("all MX hosts failed: %w", fmt.Errorf("connect to mx.remote.test:25: %w",
				&net.OpError{Op: "dial", Net: "tcp", Err: timeoutError{}})),
			models.FailureConnection},
		{"closed before the greeting",
			fmt.Errorf("all MX hosts failed: %w", fmt.Errorf("SMTP client for mx.remote.test:25: %w", io.EOF)),
			models.FailureConnection},
		{"cut off mid-conversation",
			fmt.Errorf("all MX hosts failed: %w", error(parseSMTPError(errors.New("read tcp 10.0.0.1:4321->203.0.113.9:25: i/o timeout")))),
			models.FailureConnection},
		{"no such domain",
			fmt.Errorf("all MX hosts failed: %w", fmt.Errorf("connect to nowhere.invalid:25: %w",
				&net.OpError{Op: "dial", Net: "tcp", Err: notFound})),
			models.FailureBadDomain},
		{"nameserver failing",
			fmt.Errorf("MX lookup failed for remote.test: %w",
				&net.DNSError{Err: "server misbehaving", Name: "remote.test", IsTemporary: true}),
			models.FailureDNS},
		{"lookup timed out",
			fmt.Errorf("MX lookup failed for remote.test: %w",
				&net.DNSError{Err: "i/o timeout", Name: "remote.test", IsTimeout: true}),
			models.FailureDNS},
		{"mta-sts refused the host",
			fmt.Errorf("all MX hosts failed: %w", error(&mtasts.EnforceError{Domain: "remote.test", MXHost: "mx.remote.test", Reason: "STARTTLS not offered"})),
			models.FailureTLS},
		{"no dkim key to sign with",
			fmt.Errorf("all MX hosts failed: %w", errors.New("prepare outbound message: decrypt DKIM key: bad key")),
			models.FailureOtherTransient},
		// Failures the worker makes up itself.
		{"control characters in an address",
			&SMTPError{Code: 550, Enhanced: "5.1.3", Message: "address contains control characters"},
			models.FailureBadMailbox},
		{"8-bit message, 7-bit next hop",
			fmt.Errorf("all MX hosts failed: %w", error(&SMTPError{Code: 550, Enhanced: "5.6.3", Message: "next hop does not support 8BITMIME and 7-bit conversion is not implemented"})),
			models.FailureOtherPermanent},
		{"null mx",
			&SMTPError{Code: 556, Enhanced: "5.1.10", Message: "recipient domain accepts no mail (null MX)"},
			models.FailureBadDomain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyFailure(tc.err); got != tc.want {
				t.Errorf("ClassifyFailure(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// The categories a suppression list reads (#141) are the permanent ones, and
// nothing transient may look like them.
func TestFailureCategory_Permanent(t *testing.T) {
	for c, want := range map[models.FailureCategory]bool{
		models.FailureBadMailbox: true, models.FailureBadDomain: true, models.FailurePolicy: true,
		models.FailureOtherPermanent: true, models.FailureMailboxFull: false, models.FailureGreylisted: false,
		models.FailureRateLimited: false, models.FailureConnection: false, models.FailureTLS: false,
		models.FailureDNS: false, models.FailureOtherTransient: false, "": false,
	} {
		if c.Permanent() != want {
			t.Errorf("%q.Permanent() = %v, want %v", c, !want, want)
		}
		if c != "" && c.Description() == "" {
			t.Errorf("%q has no description for the bounce", c)
		}
	}
}
