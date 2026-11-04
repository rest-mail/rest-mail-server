// Package domainrecords is the one list of what a domain must publish in DNS.
// `restmail-api dns` prints it (and `chore dns:show` and `chore dns:check` read that), and the
// API's DNS check shows it on the domain page and compares it with what a lookup finds, so the
// two cannot disagree about a value (#1703).
package domainrecords

import (
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/dkimrotate"
	"github.com/rest-mail/rest-mail-server/internal/dns"
	"github.com/rest-mail/rest-mail-server/internal/mail"
)

// List is what one domain must publish.
type List struct {
	Domain  string          `json:"domain"`
	Records []dns.DNSRecord `json:"records"`
	// Problem says why a record is missing or publication needs care: no DKIM key,
	// unreadable material, an invalid DNS name, or unverified policy-host HTTPS.
	Problem string `json:"problem,omitempty"`
}

// ForDomain lists one domain's records under its A-label name, so an
// internationalized domain's records can be pasted into a zone and checked
// with dig. The heading keeps the name as the domain was added.
func ForDomain(d models.Domain, mailHost, ip, masterKey string, ports dns.ClientPorts) List {
	entry := List{Domain: d.Name, Records: []dns.DNSRecord{}}
	name, err := mail.DomainASCII(d.Name)
	if err != nil {
		entry.Problem = "not a valid domain name: it cannot be published in DNS"
		return entry
	}
	var dkimValue string
	switch {
	case d.DKIMPrivateKey == "" || d.DKIMSelector == "":
		entry.Problem = "no DKIM key: mail from this domain goes out unsigned"
	default:
		value, err := dkimrotate.PublicRecord(d.DKIMPrivateKey, masterKey)
		if err != nil {
			entry.Problem = "DKIM key unreadable: " + err.Error()
		}
		dkimValue = value
	}
	entry.Records = dns.MailRecords(name, mailHost, ip, d.DKIMSelector, dkimValue, d.DMARCPolicy, ports)
	return entry
}

// AddMTASTS adds the policy-derived TXT and policy-host CNAME for an active policy
// (#727). It cannot verify ingress or certificate readiness, so publication carries a note.
// A policy with an invalid mx host gets neither record, and the problem says why.
func AddMTASTS(entry *List, d models.Domain, policy models.MTASTSPolicy, mailHost string) {
	name, err := mail.DomainASCII(d.Name)
	if err != nil {
		return // the entry already says the name cannot be published
	}
	id, err := policy.RecordID()
	if err != nil {
		problem := "MTA-STS policy cannot be served (" + err.Error() + "): its record is left out"
		if entry.Problem != "" {
			problem = entry.Problem + "; " + problem
		}
		entry.Problem = problem
		return
	}
	entry.Records = append(entry.Records, dns.MTASTSRecord(name, id), dns.DNSRecord{
		Type: "CNAME", Name: "mta-sts." + name, Value: mailHost, TTL: 3600,
	})
	if entry.Problem != "" {
		entry.Problem += "; "
	}
	entry.Problem += "MTA-STS policy host mta-sts." + name + ": HTTPS route and certificate are unverified; do not publish _mta-sts until both are ready"
}

// Rotation is the records a key rotation needs beside the signing key's (#728): the
// staged key's, to publish before signing moves to it, and the previous key's, kept until it
// retires so mail it signed before the switch still verifies. A key this server cannot read
// is left out: its rotation stops until it is reconciled.
func Rotation(d models.Domain, keys []models.DKIMRotationKey, masterKey string) []dns.DNSRecord {
	name, err := mail.DomainASCII(d.Name)
	if err != nil {
		return nil
	}
	var records []dns.DNSRecord
	for _, key := range keys {
		value, err := dkimrotate.RotationRecord(key, masterKey)
		if err != nil {
			continue
		}
		records = append(records, dns.DNSRecord{Type: "TXT", Name: key.Selector + "._domainkey." + name,
			Value: value, TTL: int(key.TTLSeconds)})
	}
	return records
}
