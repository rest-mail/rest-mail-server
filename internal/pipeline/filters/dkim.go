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

func (f *dkimVerifyFilter) Execute(_ context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	modified := *email

	// Check for DKIM-Signature header
	dkimSig := ""
	if raw := email.Headers.Raw; raw != nil {
		if sigs, ok := raw["Dkim-Signature"]; ok && len(sigs) > 0 {
			dkimSig = sigs[0]
		}
	}

	result := "none"
	detail := "no DKIM signature present"

	if dkimSig != "" {
		// In production, this would perform full DKIM verification:
		// 1. Parse the DKIM-Signature header (d=, s=, b=, bh=, h=)
		// 2. Look up the public key via DNS (selector._domainkey.domain TXT)
		// 3. Verify the body hash
		// 4. Verify the signature over canonicalized headers
		//
		// For now, we mark it as present but unverified.
		result = "neutral"
		detail = "DKIM signature present (verification pending crypto implementation)"
	}

	// Add Authentication-Results header
	if modified.Headers.Extra == nil {
		modified.Headers.Extra = make(map[string]string)
	}
	modified.Headers.Extra["Authentication-Results"] = "restmail; dkim=" + result

	if modified.Headers.Raw == nil {
		modified.Headers.Raw = make(map[string][]string)
	}
	modified.Headers.Raw["Authentication-Results"] = append(
		modified.Headers.Raw["Authentication-Results"],
		"restmail; dkim="+result,
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
		decrypted, err := restcrypto.Decrypt(privateKeyPEM, f.masterKey)
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
