package dns

// ClientPorts are the ports mail clients reach the server on, as its listeners
// are configured: submission and IMAP and POP3, each with implicit TLS. There
// are no cleartext client listeners to name. A port of 0 is a service that is
// not offered.
type ClientPorts struct {
	Submission int // submissions, RFC 8314: SMTP_PORT_SUBMISSION_TLS
	IMAP       int // imaps: IMAP_TLS_PORT
	POP3       int // pop3s: POP3_TLS_PORT
}

// MailRecords is what one domain must publish for a server answering as
// mailHost to receive its mail and have what it sends believed: MX, SPF,
// DMARC, and the DKIM record when the domain has a key; and the SRV records a
// mail client finds the server by (RFC 6186). `chore dns:show` prints these
// and `chore dns:check` compares them with what is published.
//
// ip is the server's public address, which the server does not know about
// itself — it comes from wherever it is exposed, so the caller passes it.
// Without one, the records that need it (the mail host's A record and the
// reverse record) are left out rather than guessed.
//
// SPF names the MX rather than an address, so moving the server changes one
// A record and nothing else. The PTR is listed for completeness: it is set at
// whoever owns the address, not in the domain's zone, and is the record most
// often missing when a provider rejects mail that passes everything else.
//
// The DMARC record asks for dmarcPolicy (none, quarantine or reject; empty is none) and sends
// the aggregate reports to postmaster@ the domain. A domain starts at none: RFC 9989 §7.4 asks
// one whose users write ordinary mail to move to reject only after reading those reports, and
// not to at all when its users post to mailing lists (#729).
//
// The SRV records point clients at the implicit-TLS services on the mail host
// (RFC 8314 §5.1 names submissions), and say outright that the cleartext ones
// are not offered, with the target "." (RFC 6186 §3.4), so a client does not
// try ports nothing listens on.
func MailRecords(domain, mailHost, ip, dkimSelector, dkimValue, dmarcPolicy string, ports ClientPorts) []DNSRecord {
	if dmarcPolicy == "" {
		dmarcPolicy = "none"
	}
	var records []DNSRecord
	if ip != "" {
		records = append(records, DNSRecord{Type: "A", Name: mailHost, Value: ip, TTL: 3600})
	}
	records = append(records,
		DNSRecord{Type: "MX", Name: domain, Value: mailHost, TTL: 3600, Priority: 10},
		DNSRecord{Type: "TXT", Name: domain, Value: "v=spf1 mx -all", TTL: 3600},
		DNSRecord{Type: "TXT", Name: "_dmarc." + domain, Value: "v=DMARC1; p=" + dmarcPolicy + "; rua=mailto:postmaster@" + domain, TTL: 3600},
	)
	if dkimSelector != "" && dkimValue != "" {
		records = append(records, DNSRecord{Type: "TXT", Name: dkimSelector + "._domainkey." + domain, Value: dkimValue, TTL: 3600})
	}
	srv := func(service string, port int) DNSRecord {
		if port <= 0 {
			return DNSRecord{Type: "SRV", Name: service + "._tcp." + domain, Value: ".", TTL: 3600}
		}
		return DNSRecord{Type: "SRV", Name: service + "._tcp." + domain, Value: mailHost, TTL: 3600, Weight: 1, Port: port}
	}
	records = append(records,
		srv("_submissions", ports.Submission),
		srv("_imaps", ports.IMAP),
		srv("_pop3s", ports.POP3),
		srv("_submission", 0),
		srv("_imap", 0),
		srv("_pop3", 0),
	)
	if ip != "" {
		records = append(records, DNSRecord{Type: "PTR", Name: ip, Value: mailHost, TTL: 3600})
	}
	return records
}

// MTASTSRecord is the TXT record that tells senders a domain has an MTA-STS policy, and which
// one (RFC 8461 §3.1): "v=STSv1; id=<id>", id being the policy's own (MTASTSPolicy.RecordID),
// so a sender that cached the policy fetches it again exactly when it has changed.
func MTASTSRecord(domain, id string) DNSRecord {
	return DNSRecord{Type: "TXT", Name: "_mta-sts." + domain, Value: "v=STSv1; id=" + id, TTL: 3600}
}
