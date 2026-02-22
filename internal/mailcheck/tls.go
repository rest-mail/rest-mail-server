package mailcheck

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"strings"
	"time"
)

// TLSCheckResult contains detailed TLS connection info.
type TLSCheckResult struct {
	Check       CheckResult
	TLSVersion  string
	CipherSuite string
	CertExpiry  time.Time
	CertSANs    []string
	CertIssuer  string
}

// tlsVersionName returns a human-readable TLS version string.
func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS10:
		return "TLS 1.0"
	case tls.VersionTLS11:
		return "TLS 1.1"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS13:
		return "TLS 1.3"
	default:
		return fmt.Sprintf("unknown (0x%04x)", v)
	}
}

// ProbeTLS connects to host:port with TLS and inspects the certificate.
func ProbeTLS(host string, port int, timeout time.Duration) TLSCheckResult {
	addr := fmt.Sprintf("%s:%d", host, port)
	start := time.Now()

	result := TLSCheckResult{
		Check: CheckResult{
			Category: "tls",
		},
	}

	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		InsecureSkipVerify: true, // We verify manually to report details
	})
	if err != nil {
		result.Check.Status = StatusFail
		result.Check.Summary = fmt.Sprintf("Cannot connect to %s", addr)
		result.Check.Detail = err.Error()
		result.Check.Duration = time.Since(start)
		return result
	}
	defer conn.Close()

	state := conn.ConnectionState()
	result.TLSVersion = tlsVersionName(state.Version)
	result.CipherSuite = tls.CipherSuiteName(state.CipherSuite)

	if len(state.PeerCertificates) == 0 {
		result.Check.Status = StatusFail
		result.Check.Summary = "No certificate presented"
		result.Check.Duration = time.Since(start)
		return result
	}

	cert := state.PeerCertificates[0]
	result.CertExpiry = cert.NotAfter
	result.CertSANs = cert.DNSNames
	if cert.Issuer.CommonName != "" {
		result.CertIssuer = cert.Issuer.CommonName
	} else if len(cert.Issuer.Organization) > 0 {
		result.CertIssuer = cert.Issuer.Organization[0]
	}

	// Verify the certificate properly
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}

	opts := x509.VerifyOptions{
		DNSName:       host,
		Roots:         roots,
		Intermediates: x509.NewCertPool(),
	}
	for _, ic := range state.PeerCertificates[1:] {
		opts.Intermediates.AddCert(ic)
	}

	_, verifyErr := cert.Verify(opts)

	daysUntilExpiry := int(time.Until(cert.NotAfter).Hours() / 24)

	var details []string
	details = append(details, fmt.Sprintf("TLS %s, %s", result.TLSVersion, result.CipherSuite))
	details = append(details, fmt.Sprintf("Issuer: %s", result.CertIssuer))
	details = append(details, fmt.Sprintf("Expires: %s (%d days)", cert.NotAfter.Format("2006-01-02"), daysUntilExpiry))
	if len(result.CertSANs) > 0 {
		details = append(details, fmt.Sprintf("SANs: %s", strings.Join(result.CertSANs, ", ")))
	}

	if verifyErr != nil {
		result.Check.Status = StatusFail
		result.Check.Summary = fmt.Sprintf("Certificate verification failed for %s", addr)
		result.Check.Fix = "Ensure your certificate is issued by a trusted CA, covers the correct hostname (check SANs), and includes the full chain. Let's Encrypt: certbot certonly -d mail.example.com"
		details = append(details, fmt.Sprintf("Verify error: %s", verifyErr.Error()))
	} else if daysUntilExpiry < 7 {
		result.Check.Status = StatusWarn
		result.Check.Summary = fmt.Sprintf("Certificate expires in %d days!", daysUntilExpiry)
		result.Check.Fix = "Renew your TLS certificate immediately. If using Let's Encrypt: certbot renew. Set up a cron job or systemd timer for automatic renewal."
	} else if state.Version < tls.VersionTLS12 {
		result.Check.Status = StatusWarn
		result.Check.Summary = fmt.Sprintf("Using outdated %s", result.TLSVersion)
		result.Check.Fix = "Upgrade to TLS 1.2 or 1.3. Postfix: smtpd_tls_mandatory_protocols=!SSLv2,!SSLv3,!TLSv1,!TLSv1.1. Dovecot: ssl_min_protocol=TLSv1.2"
	} else {
		result.Check.Status = StatusPass
		result.Check.Summary = fmt.Sprintf("Valid certificate, %s", result.TLSVersion)
	}

	result.Check.Detail = strings.Join(details, "\n")
	result.Check.Duration = time.Since(start)
	return result
}
