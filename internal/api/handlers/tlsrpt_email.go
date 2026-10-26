package handlers

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strings"
	"time"

	"github.com/rest-mail/go-dkim/pkg/dkim"
	"github.com/restmail/restmail/internal/api/endpoint"
	"gorm.io/gorm"
)

// TLSReportEmailReceiver consumes RFC 8460 reports at one existing local mailbox.
// Ordinary mail is delivered normally. Recognised reports are consumed, including
// invalid or unsigned reports, which must not populate the report table (§3).
type TLSReportEmailReceiver struct {
	address  string
	handler  *TLSReportHandler
	resolver dkim.TXTResolver
}

func NewTLSReportEmailReceiver(db *gorm.DB, address string) *TLSReportEmailReceiver {
	return &TLSReportEmailReceiver{address: address, handler: NewTLSReportHandler(db), resolver: net.DefaultResolver.LookupTXT}
}

const tlsRPTMaxEmailBytes = 1 << 20

func (h *TLSReportEmailReceiver) receive(ctx context.Context, recipient, raw string) (bool, error) {
	if h == nil || h.address == "" || !strings.EqualFold(recipient, h.address) || raw == "" {
		return false, nil
	}
	// Bound the whole MIME envelope, in addition to the compressed and inflated
	// JSON bounds. A base64-encoded report fits within twice the JSON limit.
	message, err := mail.ReadMessage(io.LimitReader(strings.NewReader(raw), tlsRPTMaxEmailBytes+1))
	if err != nil {
		return false, nil
	}
	mediaType, parameters, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "multipart/report") || !strings.EqualFold(parameters["report-type"], "tlsrpt") {
		return false, nil
	}
	if len(raw) > tlsRPTMaxEmailBytes {
		return true, nil
	}
	report, body, err := readTLSReportEmail(message, parameters["boundary"])
	if err != nil {
		return true, nil
	}
	submitter := strings.TrimSpace(message.Header.Get("TLS-Report-Submitter"))
	if len(message.Header["Tls-Report-Submitter"]) != 1 || len(message.Header["Tls-Report-Domain"]) != 1 || submitter == "" {
		return true, nil
	}
	contact := strings.TrimPrefix(report.ContactInfo, "mailto:")
	address, err := mail.ParseAddress(contact)
	if err != nil {
		return true, nil
	}
	_, contactDomain, ok := strings.Cut(address.Address, "@")
	if !ok || !strings.EqualFold(contactDomain, submitter) {
		return true, nil
	}
	if !h.authenticated(ctx, raw, submitter) {
		return true, nil
	}
	// Use precisely the same validation and persistence as HTTPS; invalid and
	// unrelated reports are ignored, while storage failures remain retryable.
	err = h.handler.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, err := NewTLSReportHandler(tx).storeReport(report, body)
		return err
	})
	var refusal *endpoint.Error
	if errors.As(err, &refusal) {
		return true, nil
	}
	return true, err
}

func readTLSReportEmail(message *mail.Message, boundary string) (*TLSRPTReport, []byte, error) {
	if boundary == "" {
		return nil, nil, errors.New("missing report boundary")
	}
	parts := multipart.NewReader(message.Body, boundary)
	for i := 0; i < 16; i++ {
		part, err := parts.NextPart()
		if err != nil {
			return nil, nil, err
		}
		mediaType, _, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if err != nil {
			return nil, nil, err
		}
		if mediaType != "application/tlsrpt+json" && mediaType != "application/tlsrpt+gzip" {
			continue
		}
		var reader io.Reader = part
		switch strings.ToLower(strings.TrimSpace(part.Header.Get("Content-Transfer-Encoding"))) {
		case "base64":
			reader = base64.NewDecoder(base64.StdEncoding, part)
		case "quoted-printable":
			reader = quotedprintable.NewReader(part)
		case "", "7bit", "8bit", "binary":
		default:
			return nil, nil, errors.New("unsupported report encoding")
		}
		body, err := io.ReadAll(io.LimitReader(reader, tlsRPTMaxBodyBytes+1))
		if err != nil || int64(len(body)) > tlsRPTMaxBodyBytes {
			return nil, nil, errors.New("report exceeds body limit")
		}
		if mediaType == "application/tlsrpt+gzip" {
			body, _, err = gunzipReport(body)
			if err != nil {
				return nil, nil, err
			}
		}
		report := new(TLSRPTReport)
		if err := endpoint.BindBody(report, body); err != nil {
			return nil, nil, err
		}
		return report, body, nil
	}
	return nil, nil, errors.New("too many MIME parts")
}

func (h *TLSReportEmailReceiver) authenticated(ctx context.Context, raw, submitter string) bool {
	// A message's Authentication-Results header is never evidence of a valid
	// signature. Verify the original signed bytes, and require a full-body
	// signature by the reporter (§3 forbids l= even when it covers today's body).
	headers, body := dkim.SplitMessage([]byte(raw))
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	count := 0
	for _, header := range headers {
		if !strings.EqualFold(header.Name, "DKIM-Signature") {
			continue
		}
		count++
		if count > 10 {
			break
		}
		tags, err := dkim.ParseTagListStrict(header.Value)
		if err != nil {
			continue
		}
		if _, limited := tags["l"]; limited || !strings.EqualFold(tags["d"], submitter) {
			continue
		}
		result := dkim.VerifySignature(ctx, header, headers, body, h.lookupReportKey)
		if result.Result == dkim.ResultPass {
			return true
		}
	}
	return false
}

func (h *TLSReportEmailReceiver) lookupReportKey(ctx context.Context, name string) ([]string, error) {
	records, err := h.resolver(ctx, name)
	if err != nil {
		return nil, err
	}
	result := append([]string(nil), records...)
	for i, record := range result {
		tags, err := dkim.ParseTagListStrict(record)
		if err != nil {
			continue
		}
		// go-dkim verifies the email service. RFC 8460 gives these messages their
		// own key service, tlsrpt; permit it without relaxing any signature policy.
		// An email or wildcard key also works (§3 makes s=tlsrpt a SHOULD).
		allowed := false
		for _, service := range strings.Split(tags["s"], ":") {
			if strings.TrimSpace(service) == "tlsrpt" {
				allowed = true
			}
		}
		if !allowed {
			continue
		}
		fields := strings.Split(record, ";")
		for j, field := range fields {
			key, _, _ := strings.Cut(strings.TrimSpace(field), "=")
			if key == "s" {
				fields[j] = "s=email"
			}
		}
		result[i] = strings.Join(fields, ";")
	}
	return result, nil
}
