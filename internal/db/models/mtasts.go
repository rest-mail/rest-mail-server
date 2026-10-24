package models

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/mail"
)

// MTASTSPolicy stores an MTA-STS (RFC 8461) policy for a domain.
// The policy is served at /.well-known/mta-sts.txt based on the Host header.
type MTASTSPolicy struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	DomainID  uint      `gorm:"uniqueIndex;not null" json:"domain_id"`
	Mode      string    `gorm:"size:20;not null;default:'testing'" json:"mode"` // none, testing, enforce
	MXHosts   string    `gorm:"type:text" json:"mx_hosts"`                      // comma-separated
	MaxAge    int       `gorm:"not null;default:604800" json:"max_age"`         // seconds
	Active    bool      `gorm:"default:true" json:"active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Domain    Domain    `gorm:"foreignKey:DomainID" json:"domain,omitempty"`
}

// Text is the policy file /.well-known/mta-sts.txt serves (RFC 8461 §3.2): its version, its
// mode, one mx line per pattern, each a DNS name and so an internationalised one in A-label
// form, and its max_age. A pattern that is not a host name is an error rather than left out:
// a policy that silently allows fewer hosts is worse than a fetch that fails, after which a
// sending MTA keeps the policy it has cached.
func (p MTASTSPolicy) Text() (string, error) {
	var sb strings.Builder
	sb.WriteString("version: STSv1\n")
	fmt.Fprintf(&sb, "mode: %s\n", p.Mode)
	for _, mx := range strings.Split(p.MXHosts, ",") {
		mx = strings.TrimSpace(mx)
		if mx == "" {
			continue
		}
		pattern, err := mail.MXPatternASCII(mx)
		if err != nil {
			return "", fmt.Errorf("mx %q: %w", mx, err)
		}
		fmt.Fprintf(&sb, "mx: %s\n", pattern)
	}
	fmt.Fprintf(&sb, "max_age: %d\n", p.MaxAge)
	return sb.String(), nil
}

// RecordID is the id the domain's _mta-sts TXT record carries (RFC 8461 §3.1), which "MUST
// uniquely identify a given instance of a policy": the first 20 hex digits of the SHA-256 of
// Text, so it changes exactly when what senders fetch does, and the operator never picks one
// (#727). An id is 1 to 32 letters and digits.
func (p MTASTSPolicy) RecordID() (string, error) {
	text, err := p.Text()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:20], nil
}
