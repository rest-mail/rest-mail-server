// Package arf reads Abuse Reporting Format feedback reports (RFC 5965): the
// complaint a mailbox provider sends back when a recipient marks a message as
// spam. It only parses a report. Matching it to sent mail and acting on it are
// left to the caller.
package arf

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/netip"
	"net/textproto"
	"strings"
	"time"
)

// MaxReportBytes caps a whole report. Providers return the original message,
// attachments included, so the cap is far above what the fields themselves need.
const MaxReportBytes = 10 << 20

// maxFieldsBytes caps the message/feedback-report part, which holds only
// header-style fields.
const maxFieldsBytes = 64 << 10

var (
	// ErrNotFeedbackReport means the message is not
	// multipart/report; report-type=feedback-report.
	ErrNotFeedbackReport = errors.New("arf: not a feedback report")
	// ErrMalformed means the message claims to be a feedback report but does
	// not follow RFC 5965, or could not be read as a message at all.
	ErrMalformed = errors.New("arf: malformed feedback report")
	// ErrTooLarge means a feedback report, or one of its parts, exceeds its cap.
	ErrTooLarge = errors.New("arf: feedback report too large")
)

// Feedback types registered with IANA (RFC 5965 §7.3, RFC 6430, RFC 6591).
const (
	TypeAbuse       = "abuse"
	TypeFraud       = "fraud"
	TypeVirus       = "virus"
	TypeOther       = "other"
	TypeNotSpam     = "not-spam"
	TypeAuthFailure = "auth-failure"
)

// Report is the parsed message/feedback-report part of an ARF report, with the
// original message or header the provider returned.
type Report struct {
	// FeedbackType is lower-cased, such as "abuse" or "not-spam". Unregistered
	// types are kept as sent.
	FeedbackType string
	UserAgent    string
	// Version is kept as sent. RFC 5965 fixes it at 1, but drafts before the
	// RFC used 0.1 and some programmes still send it.
	Version string

	// OriginalMailFrom and OriginalRcptTo have any enclosing angle brackets
	// removed. Providers often redact or omit the recipient (RFC 6590).
	OriginalMailFrom string
	OriginalRcptTo   []string
	// SourceIP is the zero Addr when the field is absent.
	SourceIP netip.Addr
	// ArrivalDate is the zero Time when the field is absent.
	ArrivalDate           time.Time
	ReportedDomain        []string
	AuthenticationResults []string
	ReportedURI           []string

	Original Original
}

// Original is the third part of a report: the message that was complained
// about (message/rfc822), or only its header (text/rfc822-headers).
type Original struct {
	HeadersOnly bool
	// Raw is the part's content after transfer decoding.
	Raw    []byte
	Header mail.Header
	// MessageID and From are as written. Subject has RFC 2047 encoded words
	// decoded when they can be.
	MessageID string
	From      string
	Subject   string
}

// Parse reads raw as an ARF report. It returns ErrNotFeedbackReport for any
// other message, ErrTooLarge past MaxReportBytes, and ErrMalformed for a report
// that breaks RFC 5965 §2 or §3. The type is checked before the size, so a
// large message that is not a report is still reported as not a report.
func Parse(raw []byte) (*Report, error) {
	message, err := mail.ReadMessage(io.LimitReader(bytes.NewReader(raw), MaxReportBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: reading the message header: %v", ErrMalformed, err)
	}
	mediaType, parameters, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if mediaType != "multipart/report" {
		return nil, fmt.Errorf("%w: content type %q is not multipart/report", ErrNotFeedbackReport, mediaType)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: content type: %v", ErrMalformed, err)
	}
	if !strings.EqualFold(parameters["report-type"], "feedback-report") {
		return nil, fmt.Errorf("%w: report type %q is not feedback-report", ErrNotFeedbackReport, parameters["report-type"])
	}
	if len(raw) > MaxReportBytes {
		return nil, fmt.Errorf("%w: %d bytes exceeds %d", ErrTooLarge, len(raw), MaxReportBytes)
	}
	boundary := parameters["boundary"]
	if boundary == "" {
		return nil, fmt.Errorf("%w: missing MIME boundary", ErrMalformed)
	}

	// RFC 5965 §2: a human-readable part, then the machine-readable fields,
	// then the original message or its header. Any later parts are ignored.
	parts := multipart.NewReader(message.Body, boundary)
	if _, err := parts.NextRawPart(); err != nil {
		return nil, fmt.Errorf("%w: reading the first part: %v", ErrMalformed, err)
	}
	part, err := parts.NextRawPart()
	if err != nil {
		return nil, fmt.Errorf("%w: reading the second part: %v", ErrMalformed, err)
	}
	if mediaType := partType(part); mediaType != "message/feedback-report" {
		return nil, fmt.Errorf("%w: second part is %q, not message/feedback-report", ErrMalformed, mediaType)
	}
	data, err := readPart(part, maxFieldsBytes)
	if err != nil {
		return nil, err
	}
	fields, err := readHeader(data)
	if err != nil {
		return nil, fmt.Errorf("%w: reading the feedback fields: %v", ErrMalformed, err)
	}
	report, err := parseFields(fields)
	if err != nil {
		return nil, err
	}

	part, err = parts.NextRawPart()
	if err != nil {
		return nil, fmt.Errorf("%w: reading the third part: %v", ErrMalformed, err)
	}
	switch mediaType := partType(part); mediaType {
	case "message/rfc822":
	case "text/rfc822-headers":
		report.Original.HeadersOnly = true
	default:
		return nil, fmt.Errorf("%w: third part is %q, not message/rfc822 or text/rfc822-headers", ErrMalformed, mediaType)
	}
	data, err = readPart(part, MaxReportBytes)
	if err != nil {
		return nil, err
	}
	header, err := readHeader(data)
	if err != nil {
		return nil, fmt.Errorf("%w: reading the returned header: %v", ErrMalformed, err)
	}
	if len(header) == 0 {
		return nil, fmt.Errorf("%w: the returned header is empty", ErrMalformed)
	}
	report.Original.Raw = data
	report.Original.Header = header
	report.Original.MessageID = header.Get("Message-ID")
	report.Original.From = header.Get("From")
	report.Original.Subject = header.Get("Subject")
	if decoded, err := new(mime.WordDecoder).DecodeHeader(report.Original.Subject); err == nil {
		report.Original.Subject = decoded
	}
	return report, nil
}

// parseFields applies RFC 5965 §3.1 and §3.2: which fields are required, which
// may appear only once, and which may repeat. Field names are matched without
// regard to case.
func parseFields(fields mail.Header) (*Report, error) {
	report := new(Report)
	var err error
	if report.FeedbackType, err = single(fields, "Feedback-Type", true); err != nil {
		return nil, err
	}
	report.FeedbackType = strings.ToLower(report.FeedbackType)
	if !isToken(report.FeedbackType) {
		return nil, fmt.Errorf("%w: Feedback-Type %q is not a token", ErrMalformed, report.FeedbackType)
	}
	if report.UserAgent, err = single(fields, "User-Agent", true); err != nil {
		return nil, err
	}
	if report.Version, err = single(fields, "Version", true); err != nil {
		return nil, err
	}

	mailFrom, err := single(fields, "Original-Mail-From", false)
	if err != nil {
		return nil, err
	}
	report.OriginalMailFrom = path(mailFrom)
	sourceIP, err := single(fields, "Source-IP", false)
	if err != nil {
		return nil, err
	}
	if sourceIP != "" {
		if report.SourceIP, err = netip.ParseAddr(sourceIP); err != nil {
			return nil, fmt.Errorf("%w: Source-IP: %v", ErrMalformed, err)
		}
	}
	arrivalDate, err := single(fields, "Arrival-Date", false)
	if err != nil {
		return nil, err
	}
	if arrivalDate != "" {
		if report.ArrivalDate, err = mail.ParseDate(arrivalDate); err != nil {
			return nil, fmt.Errorf("%w: Arrival-Date: %v", ErrMalformed, err)
		}
	}
	for _, name := range []string{"Original-Envelope-Id", "Reporting-MTA", "Incidents"} {
		if _, err := single(fields, name, false); err != nil {
			return nil, err
		}
	}

	recipients, err := multiple(fields, "Original-Rcpt-To")
	if err != nil {
		return nil, err
	}
	for _, recipient := range recipients {
		report.OriginalRcptTo = append(report.OriginalRcptTo, path(recipient))
	}
	if report.ReportedDomain, err = multiple(fields, "Reported-Domain"); err != nil {
		return nil, err
	}
	if report.AuthenticationResults, err = multiple(fields, "Authentication-Results"); err != nil {
		return nil, err
	}
	if report.ReportedURI, err = multiple(fields, "Reported-URI"); err != nil {
		return nil, err
	}
	return report, nil
}

// single returns a field that may appear at most once. Present but empty is
// malformed, as is a second appearance.
func single(fields mail.Header, name string, required bool) (string, error) {
	values := fields[textproto.CanonicalMIMEHeaderKey(name)]
	switch {
	case len(values) > 1:
		return "", fmt.Errorf("%w: %s appears %d times", ErrMalformed, name, len(values))
	case len(values) == 0 && required:
		return "", fmt.Errorf("%w: missing %s", ErrMalformed, name)
	case len(values) == 0:
		return "", nil
	}
	value := strings.TrimSpace(values[0])
	if value == "" {
		return "", fmt.Errorf("%w: empty %s", ErrMalformed, name)
	}
	return value, nil
}

// multiple returns every appearance of a field that may repeat, or nil.
func multiple(fields mail.Header, name string) ([]string, error) {
	var out []string
	for _, value := range fields[textproto.CanonicalMIMEHeaderKey(name)] {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("%w: empty %s", ErrMalformed, name)
		}
		out = append(out, value)
	}
	return out, nil
}

// partType returns a part's lower-cased media type, or "" when it has none or
// it cannot be read.
func partType(part *multipart.Part) string {
	mediaType, _, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
	if err != nil {
		return ""
	}
	return mediaType
}

// readPart returns a part's content after transfer decoding, up to limit bytes.
func readPart(part *multipart.Part, limit int64) ([]byte, error) {
	var body io.Reader = part
	switch encoding := strings.ToLower(strings.TrimSpace(part.Header.Get("Content-Transfer-Encoding"))); encoding {
	case "", "7bit", "8bit", "binary":
	case "base64":
		body = base64.NewDecoder(base64.StdEncoding, part)
	case "quoted-printable":
		body = quotedprintable.NewReader(part)
	default:
		return nil, fmt.Errorf("%w: unsupported transfer encoding %q", ErrMalformed, encoding)
	}
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: reading a part: %v", ErrMalformed, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: a part exceeds %d bytes", ErrTooLarge, limit)
	}
	return data, nil
}

// readHeader reads the header block at the start of data. The feedback fields
// and a text/rfc822-headers part may end without the blank line that closes a
// header, so one is supplied.
func readHeader(data []byte) (mail.Header, error) {
	message, err := mail.ReadMessage(io.MultiReader(bytes.NewReader(data), strings.NewReader("\r\n\r\n")))
	if err != nil {
		return nil, err
	}
	return message.Header, nil
}

// path removes the angle brackets around an SMTP path such as <user@example.com>.
func path(value string) string {
	if len(value) >= 2 && value[0] == '<' && value[len(value)-1] == '>' {
		return strings.TrimSpace(value[1 : len(value)-1])
	}
	return value
}

// isToken reports whether value is an RFC 2045 token.
func isToken(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c <= ' ' || c >= 0x7f || strings.IndexByte(`()<>@,;:\"/[]?=`, c) >= 0 {
			return false
		}
	}
	return true
}
