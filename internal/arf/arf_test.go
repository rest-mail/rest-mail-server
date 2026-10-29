package arf

import (
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// rfc5965Example is the example report from RFC 5965 Appendix B.
const rfc5965Example = `From: <abusedesk@example.com>
Date: Thu, 8 Mar 2005 17:40:36 EDT
Subject: FW: Earn money
To: <abuse@example.net>
MIME-Version: 1.0
Content-Type: multipart/report; report-type=feedback-report;
     boundary="part1_13d.2e68ed54_boundary"

--part1_13d.2e68ed54_boundary
Content-Type: text/plain; charset="US-ASCII"
Content-Transfer-Encoding: 7bit

This is an email abuse report for an email message received from IP
192.0.2.1 on Thu, 8 Mar 2005 14:00:00 EDT.  For more information
about this format please see http://www.mipassoc.org/arf/.

--part1_13d.2e68ed54_boundary
Content-Type: message/feedback-report

Feedback-Type: abuse
User-Agent: SomeGenerator/1.0
Version: 1
Original-Mail-From: <somespammer@example.net>
Original-Rcpt-To: <user@example.com>
Arrival-Date: Thu, 8 Mar 2005 14:00:00 EDT
Reporting-MTA: dns; mail.example.com
Source-IP: 192.0.2.1
Authentication-Results: mail.example.com;
               spf=fail smtp.mail=somespammer@example.com
Reported-Domain: example.net
Reported-Uri: http://example.net/earn_money.html
Reported-Uri: mailto:user@example.com
Removal-Recipient: user@example.com

--part1_13d.2e68ed54_boundary
Content-Type: message/rfc822
Content-Disposition: inline

From: <somespammer@example.net>
Received: from mailserver.example.net
        (mailserver.example.net [192.0.2.1])
        by example.com with ESMTP id M63d4137594e46;
        Thu, 08 Mar 2005 14:00:00 -0400
To: <Undisclosed Recipients>
Subject: Earn money
MIME-Version: 1.0
Content-type: text/plain
Message-ID: 8787KJKJ3K4J3K4J3K4J3.mail@example.net
Date: Thu, 02 Sep 2004 12:31:03 -0500

Spam Spam Spam
Spam Spam Spam
Spam Spam Spam
Spam Spam Spam
--part1_13d.2e68ed54_boundary--
`

// outlookReport is shaped like a Junk Mail Reporting Program report: the
// recipient is redacted, the fields are lower-case, the version predates the
// RFC, and the whole original message comes back. The test sends it with CRLF.
const outlookReport = `From: staff@hotmail.com
To: fbl@mail.example.org
Subject: complaint about message from 203.0.113.25
Date: Tue, 27 Oct 2026 09:14:02 -0700
MIME-Version: 1.0
Content-Type: multipart/report; report-type=feedback-report; boundary="--boundary_1234_abcd"

----boundary_1234_abcd
Content-Type: text/plain; charset="US-ASCII"
Content-Transfer-Encoding: 7bit

This is an email abuse report for an email message received from IP 203.0.113.25 on Tue, 27 Oct 2026 08:59:41 -0700.

----boundary_1234_abcd
Content-Type: message/feedback-report

feedback-type: abuse
user-agent: Microsoft Junk Mail Reporting Program
version: 0.1
original-mail-from: <bounce+7f3a@mail.example.org>
source-ip: 203.0.113.25
arrival-date: Tue, 27 Oct 2026 08:59:41 -0700

----boundary_1234_abcd
Content-Type: message/rfc822
Content-Disposition: inline

Received: from mail.example.org (203.0.113.25) by mx.hotmail.example with ESMTP; Tue, 27 Oct 2026 15:59:41 +0000
DKIM-Signature: v=1; a=rsa-sha256; c=relaxed/relaxed; d=example.org; s=mail;
	h=from:to:subject:date:message-id; bh=47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=;
	b=dGVzdA==
From: Example News <news@example.org>
To: <redacted>
Subject: =?utf-8?Q?Caf=C3=A9_news?=
Date: Tue, 27 Oct 2026 15:59:38 +0000
Message-ID: <20261027155938.4f2a@mail.example.org>
MIME-Version: 1.0
Content-Type: text/plain; charset=utf-8

This week's offers.
----boundary_1234_abcd--
`

// yahooReport is shaped like a Yahoo complaint feedback loop report: the
// recipient is masked, the source is IPv6, and the type is quoted.
const yahooReport = `Return-Path: <>
From: Yahoo! Mail Abuse <feedbackloop@feedback.yahoo.example>
To: fbl@mail.example.org
Subject: FW: Weekly digest
Date: Wed, 28 Oct 2026 04:12:55 +0000
MIME-Version: 1.0
Content-Type: multipart/report; report-type="feedback-report"; boundary="----=_Part_1_2021"

------=_Part_1_2021
Content-Type: text/plain; charset=us-ascii
Content-Transfer-Encoding: 7bit

This is an email abuse report for an email message from example.org on Wed, 28 Oct 2026 04:10:12 +0000

------=_Part_1_2021
Content-Type: message/feedback-report

Feedback-Type: abuse
User-Agent: Yahoo!-Mail-Feedback/2.0
Version: 0.1
Original-Mail-From: <bounces-42@mail.example.org>
Original-Rcpt-To: xxxxxxxx@yahoo.example
Arrival-Date: Wed, 28 Oct 2026 04:10:12 +0000
Source-IP: 2001:db8::25
Authentication-Results: mta4012.mail.gq1.yahoo.example from=example.org; domainkeys=neutral (no sig); from=example.org; dkim=pass (ok)
Reported-Domain: example.org

------=_Part_1_2021
Content-Type: message/rfc822

From: Example <digest@example.org>
To: xxxxxxxx@yahoo.example
Subject: Weekly digest
Message-ID: <digest-2026-43@example.org>
Date: Wed, 28 Oct 2026 04:10:09 +0000

Your weekly digest.
------=_Part_1_2021--
`

// headersOnlyReport returns only the original header, and names several
// recipients and domains.
const headersOnlyReport = `From: fbl@isp.example
To: fbl@mail.example.org
MIME-Version: 1.0
Content-Type: multipart/report; report-type=feedback-report; boundary=b1

--b1
Content-Type: text/plain

A complaint.

--b1
Content-Type: message/feedback-report

Feedback-Type: not-spam
User-Agent: ISP-FBL/3.1
Version: 1
Original-Rcpt-To: <alice@isp.example>
Original-Rcpt-To: <bob@isp.example>
Reported-Domain: example.org
Reported-Domain: mail.example.org

--b1
Content-Type: text/rfc822-headers

From: Sender <sender@example.org>
To: alice@isp.example, bob@isp.example
Subject: Hello
Message-ID: <hello-1@mail.example.org>
--b1--
`

// summary is the part of a Report the table compares. ArrivalDate is the wall
// clock as written, because an obsolete zone such as EDT parses to a different
// offset depending on the machine's own zone.
type summary struct {
	FeedbackType, UserAgent, Version string
	OriginalMailFrom                 string
	OriginalRcptTo                   []string
	SourceIP                         string
	ArrivalDate                      string
	ReportedDomain                   []string
	AuthenticationResults            []string
	ReportedURI                      []string
	HeadersOnly                      bool
	MessageID, From, Subject         string
}

func summarise(r *Report) summary {
	s := summary{
		FeedbackType:          r.FeedbackType,
		UserAgent:             r.UserAgent,
		Version:               r.Version,
		OriginalMailFrom:      r.OriginalMailFrom,
		OriginalRcptTo:        r.OriginalRcptTo,
		ReportedDomain:        r.ReportedDomain,
		AuthenticationResults: r.AuthenticationResults,
		ReportedURI:           r.ReportedURI,
		HeadersOnly:           r.Original.HeadersOnly,
		MessageID:             r.Original.MessageID,
		From:                  r.Original.From,
		Subject:               r.Original.Subject,
	}
	if r.SourceIP.IsValid() {
		s.SourceIP = r.SourceIP.String()
	}
	if !r.ArrivalDate.IsZero() {
		s.ArrivalDate = r.ArrivalDate.Format("2006-01-02 15:04:05")
	}
	return s
}

// TestParse reads real-shaped reports and checks every field RFC 5965 §3
// defines, and the returned message or header.
func TestParse(t *testing.T) {
	headers := "From: Sender <sender@example.org>\r\nSubject: =?utf-8?B?SGVsbG8gd29ybGQ=?=\r\nMessage-ID: <b64@example.org>\r\n"
	base64Headers := build(caseInsensitiveType, minimalFields,
		"Content-Type: text/rfc822-headers\nContent-Transfer-Encoding: base64\n\n"+base64.StdEncoding.EncodeToString([]byte(headers))+"\n")
	cases := []struct {
		name string
		raw  string
		want summary
	}{
		{"RFC 5965 Appendix B", rfc5965Example, summary{
			FeedbackType:          "abuse",
			UserAgent:             "SomeGenerator/1.0",
			Version:               "1",
			OriginalMailFrom:      "somespammer@example.net",
			OriginalRcptTo:        []string{"user@example.com"},
			SourceIP:              "192.0.2.1",
			ArrivalDate:           "2005-03-08 14:00:00",
			ReportedDomain:        []string{"example.net"},
			AuthenticationResults: []string{"mail.example.com; spf=fail smtp.mail=somespammer@example.com"},
			ReportedURI:           []string{"http://example.net/earn_money.html", "mailto:user@example.com"},
			MessageID:             "8787KJKJ3K4J3K4J3K4J3.mail@example.net",
			From:                  "<somespammer@example.net>",
			Subject:               "Earn money",
		}},
		{"Outlook-style with CRLF", strings.ReplaceAll(outlookReport, "\n", "\r\n"), summary{
			FeedbackType:     "abuse",
			UserAgent:        "Microsoft Junk Mail Reporting Program",
			Version:          "0.1",
			OriginalMailFrom: "bounce+7f3a@mail.example.org",
			SourceIP:         "203.0.113.25",
			ArrivalDate:      "2026-10-27 08:59:41",
			MessageID:        "<20261027155938.4f2a@mail.example.org>",
			From:             "Example News <news@example.org>",
			Subject:          "Café news",
		}},
		{"Yahoo-style", yahooReport, summary{
			FeedbackType:          "abuse",
			UserAgent:             "Yahoo!-Mail-Feedback/2.0",
			Version:               "0.1",
			OriginalMailFrom:      "bounces-42@mail.example.org",
			OriginalRcptTo:        []string{"xxxxxxxx@yahoo.example"},
			SourceIP:              "2001:db8::25",
			ArrivalDate:           "2026-10-28 04:10:12",
			ReportedDomain:        []string{"example.org"},
			AuthenticationResults: []string{"mta4012.mail.gq1.yahoo.example from=example.org; domainkeys=neutral (no sig); from=example.org; dkim=pass (ok)"},
			MessageID:             "<digest-2026-43@example.org>",
			From:                  "Example <digest@example.org>",
			Subject:               "Weekly digest",
		}},
		{"headers only, repeated fields", headersOnlyReport, summary{
			FeedbackType:   "not-spam",
			UserAgent:      "ISP-FBL/3.1",
			Version:        "1",
			OriginalRcptTo: []string{"alice@isp.example", "bob@isp.example"},
			ReportedDomain: []string{"example.org", "mail.example.org"},
			HeadersOnly:    true,
			MessageID:      "<hello-1@mail.example.org>",
			From:           "Sender <sender@example.org>",
			Subject:        "Hello",
		}},
		{"base64 headers, mixed-case type", base64Headers, summary{
			FeedbackType: "abuse",
			UserAgent:    "Test/1",
			Version:      "1",
			HeadersOnly:  true,
			MessageID:    "<b64@example.org>",
			From:         "Sender <sender@example.org>",
			Subject:      "Hello world",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			report, err := Parse([]byte(c.raw))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := summarise(report); !reflect.DeepEqual(got, c.want) {
				t.Errorf("Parse\n got %+v\nwant %+v", got, c.want)
			}
			if len(report.Original.Raw) == 0 || report.Original.Header == nil {
				t.Errorf("Original.Raw and Original.Header must be kept")
			}
		})
	}
}

// TestParseKeepsReturnedMessage checks the returned message is kept whole, so
// a later step can verify its DKIM signature.
func TestParseKeepsReturnedMessage(t *testing.T) {
	report, err := Parse([]byte(yahooReport))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	raw := string(report.Original.Raw)
	if !strings.HasPrefix(raw, "From: Example <digest@example.org>\n") || !strings.HasSuffix(raw, "Your weekly digest.") {
		t.Errorf("Original.Raw = %q", raw)
	}
	if got := report.Original.Header.Get("Date"); got != "Wed, 28 Oct 2026 04:10:09 +0000" {
		t.Errorf("Original.Header Date = %q", got)
	}
}

const (
	feedbackType        = "multipart/report; report-type=feedback-report; boundary=b1"
	caseInsensitiveType = "Multipart/Report; Report-Type=Feedback-Report; boundary=b1"
	minimalFields       = "Feedback-Type: Abuse\nUser-Agent: Test/1\nVersion: 1\n"
	headersPart         = "Content-Type: text/rfc822-headers\n\nFrom: a@example.org\nMessage-ID: <1@example.org>\n"
)

// build returns a three-part report with the given top-level Content-Type,
// feedback fields and third part (its own header, a blank line, then content).
func build(contentType, fields, third string) string {
	return "From: fbl@isp.example\nTo: fbl@mail.example.org\nMIME-Version: 1.0\nContent-Type: " + contentType +
		"\n\n--b1\nContent-Type: text/plain\n\nA complaint.\n\n--b1\nContent-Type: message/feedback-report\n\n" + fields +
		"\n--b1\n" + third + "--b1--\n"
}

// TestParseRejects checks that anything other than a well-formed feedback
// report is refused with the matching error, and never half-parsed.
func TestParseRejects(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want error
	}{
		{"plain text", "From: a@example.org\nContent-Type: text/plain\n\nhello\n", ErrNotFeedbackReport},
		{"no content type", "From: a@example.org\n\nhello\n", ErrNotFeedbackReport},
		{"multipart/mixed", build("multipart/mixed; boundary=b1", minimalFields, headersPart), ErrNotFeedbackReport},
		{"delivery status", build("multipart/report; report-type=delivery-status; boundary=b1", minimalFields, headersPart), ErrNotFeedbackReport},
		{"TLS report", build("multipart/report; report-type=tlsrpt; boundary=b1", minimalFields, headersPart), ErrNotFeedbackReport},
		{"no report type", build("multipart/report; boundary=b1", minimalFields, headersPart), ErrNotFeedbackReport},

		{"empty input", "", ErrMalformed},
		{"not a message", "\x00garbage", ErrMalformed},
		{"no boundary", build("multipart/report; report-type=feedback-report", minimalFields, headersPart), ErrMalformed},
		{"broken parameters", build(`multipart/report; report-type=feedback-report; boundary="b1`, minimalFields, headersPart), ErrMalformed},

		{"missing Feedback-Type", build(feedbackType, "User-Agent: Test/1\nVersion: 1\n", headersPart), ErrMalformed},
		{"missing User-Agent", build(feedbackType, "Feedback-Type: abuse\nVersion: 1\n", headersPart), ErrMalformed},
		{"missing Version", build(feedbackType, "Feedback-Type: abuse\nUser-Agent: Test/1\n", headersPart), ErrMalformed},
		{"empty Version", build(feedbackType, "Feedback-Type: abuse\nUser-Agent: Test/1\nVersion:\n", headersPart), ErrMalformed},
		{"Feedback-Type twice", build(feedbackType, minimalFields+"feedback-type: not-spam\n", headersPart), ErrMalformed},
		{"Feedback-Type not a token", build(feedbackType, "Feedback-Type: abuse spam\nUser-Agent: Test/1\nVersion: 1\n", headersPart), ErrMalformed},
		{"Source-IP twice", build(feedbackType, minimalFields+"Source-IP: 192.0.2.1\nSource-IP: 192.0.2.2\n", headersPart), ErrMalformed},
		{"Original-Mail-From twice", build(feedbackType, minimalFields+"Original-Mail-From: <a@example.org>\nOriginal-Mail-From: <b@example.org>\n", headersPart), ErrMalformed},
		{"Arrival-Date twice", build(feedbackType, minimalFields+"Arrival-Date: Wed, 28 Oct 2026 04:10:12 +0000\nArrival-Date: Wed, 28 Oct 2026 04:10:13 +0000\n", headersPart), ErrMalformed},
		{"bad Source-IP", build(feedbackType, minimalFields+"Source-IP: 192.0.2.300\n", headersPart), ErrMalformed},
		{"bad Arrival-Date", build(feedbackType, minimalFields+"Arrival-Date: yesterday\n", headersPart), ErrMalformed},
		{"empty Original-Rcpt-To", build(feedbackType, minimalFields+"Original-Rcpt-To: <a@example.org>\nOriginal-Rcpt-To:\n", headersPart), ErrMalformed},
		{"field line without a colon", build(feedbackType, minimalFields+"not a field\n", headersPart), ErrMalformed},

		{"third part not returned mail", build(feedbackType, minimalFields, "Content-Type: text/plain\n\nhello\n"), ErrMalformed},
		{"third part without a type", build(feedbackType, minimalFields, "\nFrom: a@example.org\n"), ErrMalformed},
		{"empty returned header", build(feedbackType, minimalFields, "Content-Type: text/rfc822-headers\n\n"), ErrMalformed},
		{"malformed returned header", build(feedbackType, minimalFields, "Content-Type: text/rfc822-headers\n\nnot a header line\n"), ErrMalformed},
		{"bad base64", build(feedbackType, minimalFields, "Content-Type: text/rfc822-headers\nContent-Transfer-Encoding: base64\n\n!!!not base64!!!\n"), ErrMalformed},
		{"unknown transfer encoding", build(feedbackType, minimalFields, "Content-Type: text/rfc822-headers\nContent-Transfer-Encoding: x-uuencode\n\nbegin 644 x\n"), ErrMalformed},
		{"second part not feedback fields",
			"Content-Type: " + feedbackType + "\n\n--b1\nContent-Type: text/plain\n\nA complaint.\n--b1\nContent-Type: text/plain\n\n" + minimalFields + "\n--b1\n" + headersPart + "--b1--\n",
			ErrMalformed},
		{"only one part", "Content-Type: " + feedbackType + "\n\n--b1\nContent-Type: text/plain\n\nA complaint.\n--b1--\n", ErrMalformed},
		{"no third part",
			"Content-Type: " + feedbackType + "\n\n--b1\nContent-Type: text/plain\n\nA complaint.\n--b1\nContent-Type: message/feedback-report\n\n" + minimalFields + "\n--b1--\n",
			ErrMalformed},
		{"truncated third part",
			"Content-Type: " + feedbackType + "\n\n--b1\nContent-Type: text/plain\n\nA complaint.\n--b1\nContent-Type: message/feedback-report\n\n" + minimalFields + "\n--b1\n" + headersPart,
			ErrMalformed},
		{"no parts", "Content-Type: " + feedbackType + "\n\nnothing here\n", ErrMalformed},

		{"too many feedback fields", build(feedbackType, minimalFields+strings.Repeat("Original-Rcpt-To: <someone@isp.example>\n", 3000), headersPart), ErrTooLarge},
		{"too large", build(feedbackType, minimalFields, "Content-Type: message/rfc822\n\nFrom: a@example.org\n\n"+strings.Repeat("x", MaxReportBytes)+"\n"), ErrTooLarge},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			report, err := Parse([]byte(c.raw))
			if !errors.Is(err, c.want) {
				t.Fatalf("Parse error = %v, want %v", err, c.want)
			}
			if report != nil {
				t.Errorf("Parse returned a report with error %v", err)
			}
		})
	}
}
