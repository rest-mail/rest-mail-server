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

// arcSealFilter adds ARC (Authenticated Received Chain) headers when a message
// is being forwarded, using the domain's DKIM key for signing per RFC 8617.
// It constructs all three ARC header set components:
//   - ARC-Authentication-Results (copy of current Authentication-Results)
//   - ARC-Message-Signature (DKIM-like signature over headers + body)
//   - ARC-Seal (signature over ARC header sets)
type arcSealFilter struct {
	db        *gorm.DB
	masterKey string
}

// NewARCSeal returns a FilterFactory that creates arcSealFilter instances
// backed by the given database connection (for domain key lookups).
// This is registered in routes.go with DB access, not via init().
func NewARCSeal(db *gorm.DB, masterKey string) pipeline.FilterFactory {
	return func(config []byte) (pipeline.Filter, error) {
		return &arcSealFilter{db: db, masterKey: masterKey}, nil
	}
}

func (f *arcSealFilter) Name() string             { return "arc_seal" }
func (f *arcSealFilter) Type() pipeline.FilterType { return pipeline.FilterTypeTransform }

func (f *arcSealFilter) Execute(_ context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	modified := *email

	// Ensure maps are initialised
	if modified.Headers.Raw == nil {
		modified.Headers.Raw = make(map[string][]string)
	}
	if modified.Headers.Extra == nil {
		modified.Headers.Extra = make(map[string]string)
	}
	if modified.Metadata == nil {
		modified.Metadata = make(map[string]string)
	}

	// Determine the next ARC instance number
	existingSeals := email.Headers.Raw["Arc-Seal"]
	nextInstance := len(existingSeals) + 1

	// Limit ARC chain length per RFC 8617 (max 50 sets)
	if nextInstance > 50 {
		return arcSealSkipResult("ARC chain too long (max 50 sets)"), nil
	}

	// Extract sender domain for DKIM key lookup
	senderDomain := ""
	if from := email.Envelope.MailFrom; from != "" {
		if idx := strings.LastIndex(from, "@"); idx >= 0 {
			senderDomain = from[idx+1:]
		}
	}
	if senderDomain == "" {
		return arcSealSkipResult("no sender domain"), nil
	}

	// Look up domain DKIM config
	var domain models.Domain
	if err := f.db.Where("name = ?", senderDomain).First(&domain).Error; err != nil || domain.DKIMPrivateKey == "" || domain.DKIMSelector == "" {
		return arcSealSkipResult("no DKIM key configured for domain " + senderDomain), nil
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
		return arcSealSkipResult("failed to decode DKIM private key PEM"), nil
	}

	privKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		// Try PKCS8
		key, err2 := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err2 != nil {
			return arcSealSkipResult("failed to parse DKIM private key: " + err.Error()), nil
		}
		var ok bool
		privKey, ok = key.(*rsa.PrivateKey)
		if !ok {
			return arcSealSkipResult("DKIM key is not RSA"), nil
		}
	}

	now := time.Now()
	iStr := fmt.Sprintf("%d", nextInstance)

	// Determine the cv= value for the ARC-Seal
	cv := "none"
	if nextInstance > 1 {
		// Check previous chain validity from metadata or headers
		arcStatus := ""
		if email.Metadata != nil {
			arcStatus = email.Metadata["arc_status"]
		}
		if arcStatus == "pass" || arcStatus == "" {
			// If arc_verify ran and passed, or wasn't run (no prior ARC), use pass
			if len(existingSeals) > 0 {
				cv = "pass"
			}
		} else {
			cv = "fail"
		}
	}

	// 1. Build ARC-Authentication-Results
	// Copy current Authentication-Results as the basis
	currentAuthResults := ""
	if email.Headers.Extra != nil {
		currentAuthResults = email.Headers.Extra["Authentication-Results"]
	}
	if currentAuthResults == "" {
		currentAuthResults = "restmail; none"
	}

	arcAAR := fmt.Sprintf("i=%s; %s", iStr, currentAuthResults)

	// 2. Build ARC-Message-Signature (DKIM-like signature over headers + body)
	bodyContent := email.Body.Content
	if bodyContent == "" && len(email.Body.Parts) > 0 {
		bodyContent = email.Body.Parts[0].Content
	}
	canonBody := relaxedBody(bodyContent)
	bodyHash := sha256.Sum256([]byte(canonBody))
	bh := base64.StdEncoding.EncodeToString(bodyHash[:])

	signedHeaders := "from:to:subject:date:message-id"
	headerValues := buildCanonicalHeaders(email, signedHeaders)

	// Build ARC-Message-Signature without b= value for signing
	amsHeaderValue := fmt.Sprintf(
		"i=%s; a=rsa-sha256; c=relaxed/relaxed; d=%s; s=%s; t=%d; h=%s; bh=%s; b=",
		iStr, senderDomain, domain.DKIMSelector, now.Unix(), signedHeaders, bh,
	)

	// Sign ARC-Message-Signature
	amsSignData := headerValues + "arc-message-signature:" + relaxedHeaderValue(amsHeaderValue)
	amsHashed := sha256.Sum256([]byte(amsSignData))
	amsSignature, err := rsa.SignPKCS1v15(rand.Reader, privKey, crypto.SHA256, amsHashed[:])
	if err != nil {
		return arcSealSkipResult("ARC-Message-Signature signing failed: " + err.Error()), nil
	}

	arcAMS := fmt.Sprintf(
		"i=%s; a=rsa-sha256; c=relaxed/relaxed; d=%s; s=%s; t=%d; h=%s; bh=%s; b=%s",
		iStr, senderDomain, domain.DKIMSelector, now.Unix(), signedHeaders, bh,
		base64.StdEncoding.EncodeToString(amsSignature),
	)

	// 3. Build ARC-Seal (sign over the ARC header sets)
	// The ARC-Seal signs over all ARC-Authentication-Results, ARC-Message-Signature,
	// and ARC-Seal headers in the chain, plus the new AAR and AMS.
	arcSealHeaderValue := fmt.Sprintf(
		"i=%s; a=rsa-sha256; d=%s; s=%s; t=%d; cv=%s; b=",
		iStr, senderDomain, domain.DKIMSelector, now.Unix(), cv,
	)

	// Build the data to sign for ARC-Seal:
	// Previous ARC headers (in order) + new AAR + new AMS + new ARC-Seal (without b=)
	var sealSignData string

	// Include existing ARC headers from previous sets
	existingAAR := email.Headers.Raw["Arc-Authentication-Results"]
	existingAMS := email.Headers.Raw["Arc-Message-Signature"]

	for i := 0; i < len(existingSeals); i++ {
		if i < len(existingAAR) {
			sealSignData += "arc-authentication-results:" + relaxedHeaderValue(existingAAR[i]) + "\r\n"
		}
		if i < len(existingAMS) {
			sealSignData += "arc-message-signature:" + relaxedHeaderValue(existingAMS[i]) + "\r\n"
		}
		sealSignData += "arc-seal:" + relaxedHeaderValue(existingSeals[i]) + "\r\n"
	}

	// Add new AAR and AMS
	sealSignData += "arc-authentication-results:" + relaxedHeaderValue(arcAAR) + "\r\n"
	sealSignData += "arc-message-signature:" + relaxedHeaderValue(arcAMS) + "\r\n"
	// Add the new ARC-Seal header without b= value
	sealSignData += "arc-seal:" + relaxedHeaderValue(arcSealHeaderValue)

	sealHashed := sha256.Sum256([]byte(sealSignData))
	sealSignature, err := rsa.SignPKCS1v15(rand.Reader, privKey, crypto.SHA256, sealHashed[:])
	if err != nil {
		return arcSealSkipResult("ARC-Seal signing failed: " + err.Error()), nil
	}

	arcAS := fmt.Sprintf(
		"i=%s; a=rsa-sha256; d=%s; s=%s; t=%d; cv=%s; b=%s",
		iStr, senderDomain, domain.DKIMSelector, now.Unix(), cv,
		base64.StdEncoding.EncodeToString(sealSignature),
	)

	// Add all three ARC headers to the email
	modified.Headers.Raw["Arc-Authentication-Results"] = append(
		modified.Headers.Raw["Arc-Authentication-Results"], arcAAR,
	)
	modified.Headers.Raw["Arc-Message-Signature"] = append(
		modified.Headers.Raw["Arc-Message-Signature"], arcAMS,
	)
	modified.Headers.Raw["Arc-Seal"] = append(
		modified.Headers.Raw["Arc-Seal"], arcAS,
	)

	// Also store in Extra for easy access to the most recent set
	modified.Headers.Extra["ARC-Authentication-Results"] = arcAAR
	modified.Headers.Extra["ARC-Message-Signature"] = arcAMS
	modified.Headers.Extra["ARC-Seal"] = arcAS

	return &pipeline.FilterResult{
		Type:    pipeline.FilterTypeTransform,
		Action:  pipeline.ActionContinue,
		Message: &modified,
		Log: pipeline.FilterLog{
			Filter: "arc_seal",
			Result: "sealed",
			Detail: fmt.Sprintf("i=%s d=%s s=%s cv=%s", iStr, senderDomain, domain.DKIMSelector, cv),
		},
	}, nil
}

// arcSealSkipResult returns a skip result for the arc_seal filter.
func arcSealSkipResult(detail string) *pipeline.FilterResult {
	return &pipeline.FilterResult{
		Type:   pipeline.FilterTypeTransform,
		Action: pipeline.ActionContinue,
		Log: pipeline.FilterLog{
			Filter: "arc_seal",
			Result: "skipped",
			Detail: detail,
		},
	}
}
