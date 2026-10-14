package dns

// MailRecords is what one domain must publish for a server answering as
// mailHost to receive its mail and have what it sends believed: MX, SPF,
// DMARC, and the DKIM record when the domain has a key. `chore dns:show`
// prints these and `chore dns:check` compares them with what is published.
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
func MailRecords(domain, mailHost, ip, dkimSelector, dkimValue string) []DNSRecord {
	var records []DNSRecord
	if ip != "" {
		records = append(records, DNSRecord{Type: "A", Name: mailHost, Value: ip, TTL: 3600})
	}
	records = append(records,
		DNSRecord{Type: "MX", Name: domain, Value: mailHost, TTL: 3600, Priority: 10},
		DNSRecord{Type: "TXT", Name: domain, Value: "v=spf1 mx -all", TTL: 3600},
		DNSRecord{Type: "TXT", Name: "_dmarc." + domain, Value: "v=DMARC1; p=reject; rua=mailto:postmaster@" + domain, TTL: 3600},
	)
	if dkimSelector != "" && dkimValue != "" {
		records = append(records, DNSRecord{Type: "TXT", Name: dkimSelector + "._domainkey." + domain, Value: dkimValue, TTL: 3600})
	}
	if ip != "" {
		records = append(records, DNSRecord{Type: "PTR", Name: ip, Value: mailHost, TTL: 3600})
	}
	return records
}
