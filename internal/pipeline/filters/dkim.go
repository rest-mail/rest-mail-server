package filters

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	restcrypto "github.com/restmail/restmail/internal/crypto"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/dkim"
	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

// dkimVerifyFilter verifies DKIM signatures on inbound messages.
// This adds Authentication-Results headers to the email.
type dkimVerifyFilter struct{}

func init() {
	pipeline.DefaultRegistry.Register("dkim_verify", NewDKIMVerify)
	// dkim_sign is registered in routes.go with DB access
}

func NewDKIMVerify(_ []byte) (pipeline.Filter, error) {
	return &dkimVerifyFilter{}, nil
}

func (f *dkimVerifyFilter) Name() string             { return "dkim_verify" }
func (f *dkimVerifyFilter) Type() pipeline.FilterType { return pipeline.FilterTypeTransform }

func (f *dkimVerifyFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	modified := *email

	// DKIM must be verified against the exact signed bytes. The raw message is
	// threaded through the pipeline as metadata by the inbound handlers; a
	// parsed/reconstructed EmailJSON cannot reproduce the signer's
	// canonicalization, so verification requires the raw source.
	raw := ""
	if email.Metadata != nil {
		raw = email.Metadata["raw_message"]
	}

	result := dkim.ResultNone
	detail := "no DKIM signature present"
	authResults := "restmail; dkim=none"

	if raw != "" {
		results := dkim.Verify(ctx, []byte(raw), nil)
		if len(results) > 0 {
			result, detail, authResults = summarizeDKIM(results)
		}
	} else if hasRawHeader(email.Headers.Raw, "Dkim-Signature") {
		// Signature present but we never received the raw source to verify it.
		result = dkim.ResultNeutral
		detail = "DKIM signature present but raw message unavailable for verification"
		authResults = "restmail; dkim=neutral"
	}

	// Add Authentication-Results header
	if modified.Headers.Extra == nil {
		modified.Headers.Extra = make(map[string]string)
	}
	modified.Headers.Extra["Authentication-Results"] = authResults

	if modified.Headers.Raw == nil {
		modified.Headers.Raw = make(map[string][]string)
	}
	modified.Headers.Raw["Authentication-Results"] = append(
		modified.Headers.Raw["Authentication-Results"],
		authResults,
	)

	return &pipeline.FilterResult{
		Type:    pipeline.FilterTypeTransform,
		Action:  pipeline.ActionContinue,
		Message: &modified,
		Log: pipeline.FilterLog{
			Filter: "dkim_verify",
			Result: result,
			Detail: detail,
		},
	}, nil
}

// summarizeDKIM reduces one-or-more signature verdicts to an overall result,
// a log detail, and an RFC 8601 Authentication-Results value (one dkim= entry
// per signature). The overall result is the strongest: pass > fail > temperror
// > permerror > neutral > none.
func summarizeDKIM(results []dkim.VerifyResult) (overall, detail, authResults string) {
	rank := map[string]int{
		dkim.ResultNone: 0, dkim.ResultNeutral: 1, dkim.ResultPermError: 2,
		dkim.ResultTempError: 3, dkim.ResultFail: 4, dkim.ResultPass: 5,
	}
	overall = dkim.ResultNone
	var entries, details []string
	for _, r := range results {
		entry := "dkim=" + r.Result
		if r.Domain != "" {
			entry += " header.d=" + r.Domain
		}
		entries = append(entries, entry)
		details = append(details, fmt.Sprintf("d=%s s=%s %s: %s", r.Domain, r.Selector, r.Result, r.Reason))
		if rank[r.Result] > rank[overall] {
			overall = r.Result
		}
	}
	authResults = "restmail; " + strings.Join(entries, "; ")
	detail = strings.Join(details, " | ")
	return overall, detail, authResults
}

// hasRawHeader reports whether a canonical (Header-Case) header name has a
// non-empty value in the parsed raw-header map.
func hasRawHeader(raw map[string][]string, name string) bool {
	if raw == nil {
		return false
	}
	vals, ok := raw[name]
	return ok && len(vals) > 0
}

// dkimSignFilter signs outbound messages with the domain's DKIM key.
type dkimSignFilter struct {
	db        *gorm.DB
	masterKey string
}

// NewDKIMSign returns a FilterFactory that creates dkimSignFilter instances
// backed by the given database connection (for domain key lookups).
func NewDKIMSign(db *gorm.DB, masterKey string) pipeline.FilterFactory {
	return func(config []byte) (pipeline.Filter, error) {
		return &dkimSignFilter{db: db, masterKey: masterKey}, nil
	}
}

func (f *dkimSignFilter) Name() string             { return "dkim_sign" }
func (f *dkimSignFilter) Type() pipeline.FilterType { return pipeline.FilterTypeTransform }

func (f *dkimSignFilter) Execute(_ context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	modified := *email

	// Extract sender domain
	senderDomain := ""
	if from := email.Envelope.MailFrom; from != "" {
		if idx := strings.LastIndex(from, "@"); idx >= 0 {
			senderDomain = from[idx+1:]
		}
	}

	if senderDomain == "" {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeTransform,
			Action: pipeline.ActionContinue,
			Message: &modified,
			Log: pipeline.FilterLog{
				Filter: "dkim_sign",
				Result: "skipped",
				Detail: "no sender domain",
			},
		}, nil
	}

	// Look up domain DKIM config
	var domain models.Domain
	if err := f.db.Where("name = ?", senderDomain).First(&domain).Error; err != nil || domain.DKIMPrivateKey == "" || domain.DKIMSelector == "" {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeTransform,
			Action: pipeline.ActionContinue,
			Message: &modified,
			Log: pipeline.FilterLog{
				Filter: "dkim_sign",
				Result: "skipped",
				Detail: "no DKIM key configured for domain " + senderDomain,
			},
		}, nil
	}

	// Decrypt private key if master key is configured
	privateKeyPEM := domain.DKIMPrivateKey
	if f.masterKey != "" {
		decrypted, err := restcrypto.DecryptString(privateKeyPEM, f.masterKey)
		if err != nil {
			// Fall back to plaintext in case key was stored before encryption was enabled
			decrypted = privateKeyPEM
		}
		privateKeyPEM = decrypted
	}

	// Parse private key
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return skipResult("failed to decode DKIM private key PEM"), nil
	}

	privKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		// Try PKCS8
		key, err2 := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err2 != nil {
			return skipResult("failed to parse DKIM private key: " + err.Error()), nil
		}
		var ok bool
		privKey, ok = key.(*rsa.PrivateKey)
		if !ok {
			return skipResult("DKIM key is not RSA"), nil
		}
	}

	// Build canonical body (relaxed)
	bodyContent := email.Body.Content
	if bodyContent == "" && len(email.Body.Parts) > 0 {
		bodyContent = email.Body.Parts[0].Content
	}
	canonBody := relaxedBody(bodyContent)
	bodyHash := sha256.Sum256([]byte(canonBody))
	bh := base64.StdEncoding.EncodeToString(bodyHash[:])

	// Build signed headers
	signedHeaders := "from:to:subject:date:message-id"
	headerValues := buildCanonicalHeaders(email, signedHeaders)

	// Build DKIM-Signature without b= value
	now := time.Now()
	dkimHeader := fmt.Sprintf(
		"v=1; a=rsa-sha256; c=relaxed/relaxed; d=%s; s=%s; t=%d; h=%s; bh=%s; b=",
		senderDomain, domain.DKIMSelector, now.Unix(), signedHeaders, bh,
	)

	// Add DKIM-Signature to the headers to sign
	signData := headerValues + "dkim-signature:" + relaxedHeaderValue(dkimHeader)

	// Sign
	hashed := sha256.Sum256([]byte(signData))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privKey, crypto.SHA256, hashed[:])
	if err != nil {
		return skipResult("DKIM signing failed: " + err.Error()), nil
	}

	dkimSig := "v=1; a=rsa-sha256; c=relaxed/relaxed; d=" + senderDomain +
		"; s=" + domain.DKIMSelector +
		"; t=" + fmt.Sprintf("%d", now.Unix()) +
		"; h=" + signedHeaders +
		"; bh=" + bh +
		"; b=" + base64.StdEncoding.EncodeToString(signature)

	if modified.Headers.Extra == nil {
		modified.Headers.Extra = make(map[string]string)
	}
	modified.Headers.Extra["DKIM-Signature"] = dkimSig

	return &pipeline.FilterResult{
		Type:    pipeline.FilterTypeTransform,
		Action:  pipeline.ActionContinue,
		Message: &modified,
		Log: pipeline.FilterLog{
			Filter: "dkim_sign",
			Result: "signed",
			Detail: fmt.Sprintf("d=%s s=%s", senderDomain, domain.DKIMSelector),
		},
	}, nil
}

func skipResult(detail string) *pipeline.FilterResult {
	return &pipeline.FilterResult{
		Type:   pipeline.FilterTypeTransform,
		Action: pipeline.ActionContinue,
		Log: pipeline.FilterLog{
			Filter: "dkim_sign",
			Result: "skipped",
			Detail: detail,
		},
	}
}

// relaxedBody implements DKIM relaxed body canonicalization.
func relaxedBody(body string) string {
	lines := strings.Split(body, "\n")
	var result []string
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		// Reduce sequences of WSP to single SP
		line = strings.Join(strings.Fields(line), " ")
		line = strings.TrimRight(line, " ")
		result = append(result, line)
	}
	// Remove trailing empty lines
	for len(result) > 0 && result[len(result)-1] == "" {
		result = result[:len(result)-1]
	}
	canonical := strings.Join(result, "\r\n")
	if canonical != "" {
		canonical += "\r\n"
	}
	return canonical
}

// relaxedHeaderValue implements relaxed header value canonicalization.
func relaxedHeaderValue(value string) string {
	// Unfold (remove CRLF followed by WSP)
	value = strings.ReplaceAll(value, "\r\n ", " ")
	value = strings.ReplaceAll(value, "\r\n\t", " ")
	// Reduce WSP sequences to single SP
	value = strings.Join(strings.Fields(value), " ")
	return strings.TrimSpace(value)
}

// buildCanonicalHeaders builds the canonicalized header string for DKIM signing.
func buildCanonicalHeaders(email *pipeline.EmailJSON, headerList string) string {
	// Map header names to values from the email
	headerMap := map[string]string{}
	if len(email.Headers.From) > 0 {
		from := email.Headers.From[0]
		if from.Name != "" {
			headerMap["from"] = fmt.Sprintf("%s <%s>", from.Name, from.Address)
		} else {
			headerMap["from"] = from.Address
		}
	}
	if len(email.Headers.To) > 0 {
		var addrs []string
		for _, a := range email.Headers.To {
			addrs = append(addrs, a.Address)
		}
		headerMap["to"] = strings.Join(addrs, ", ")
	}
	headerMap["subject"] = email.Headers.Subject
	headerMap["date"] = email.Headers.Date
	headerMap["message-id"] = email.Headers.MessageID

	var result string
	for _, name := range strings.Split(headerList, ":") {
		name = strings.TrimSpace(name)
		if val, ok := headerMap[name]; ok {
			result += strings.ToLower(name) + ":" + relaxedHeaderValue(val) + "\r\n"
		}
	}
	return result
}
