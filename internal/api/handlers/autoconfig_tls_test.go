package handlers

import (
	"encoding/xml"
	"strings"
	"testing"
)

// Autoconfig is where advertising a cleartext port does lasting damage: whatever a mail
// client reads here is what it saves and keeps using. So this asserts on the documents
// themselves rather than on the listeners.
//
// RED before this change: SMTP was advertised on 587 with socketType STARTTLS (and
// Encryption=TLS in the Outlook schema), which tells a client to connect in the clear and
// hope the upgrade happens. There is no listener there any more either way.
func TestAutoconfigDocumentsAdvertiseOnlyImplicitTLS(t *testing.T) {
	const domain = "example.test"

	for _, c := range []struct {
		name string
		doc  any
	}{
		{"mozilla", mozillaClientConfig(domain)},
		{"autodiscover", autodiscoverResponse(domain, "alice@"+domain)},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, err := xml.MarshalIndent(c.doc, "", "  ")
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			body := string(out)

			// Wrapped in tags so a port number cannot be matched inside a hostname or
			// namespace URL.
			for _, port := range []string{"587", "143", "110", "25"} {
				if strings.Contains(body, ">"+port+"<") {
					t.Errorf("advertises cleartext-capable port %s:\n%s", port, body)
				}
			}
			if strings.Contains(strings.ToUpper(body), "STARTTLS") {
				t.Errorf("advertises STARTTLS:\n%s", body)
			}
			// Encryption=TLS is how the Outlook schema spells STARTTLS; implicit TLS is
			// SSL. The Mozilla document has no such element, so this only bites there.
			if strings.Contains(body, "<Encryption>TLS</Encryption>") {
				t.Errorf("Encryption=TLS means STARTTLS in this schema:\n%s", body)
			}

			for _, port := range []string{"465", "993"} {
				if !strings.Contains(body, ">"+port+"<") {
					t.Errorf("does not advertise implicit-TLS port %s:\n%s", port, body)
				}
			}
		})
	}
}

// Both documents must name the same ports: a client that reads one and a client that
// reads the other should end up configured identically.
func TestAutoconfigDocumentsAgreeOnPorts(t *testing.T) {
	const domain = "example.test"

	moz := mozillaClientConfig(domain)
	ad := autodiscoverResponse(domain, "alice@"+domain)

	if moz.Provider.Incoming.Port != autoconfigIMAPPort || moz.Provider.Outgoing.Port != autoconfigSMTPPort {
		t.Errorf("mozilla ports = imap %d / smtp %d, want %d / %d",
			moz.Provider.Incoming.Port, moz.Provider.Outgoing.Port, autoconfigIMAPPort, autoconfigSMTPPort)
	}

	byType := map[string]adProtocol{}
	for _, p := range ad.Response.Account.Protocol {
		byType[p.Type] = p
	}
	if got := byType["IMAP"].Port; got != autoconfigIMAPPort {
		t.Errorf("autodiscover IMAP port = %d, want %d", got, autoconfigIMAPPort)
	}
	if got := byType["SMTP"].Port; got != autoconfigSMTPPort {
		t.Errorf("autodiscover SMTP port = %d, want %d", got, autoconfigSMTPPort)
	}
	for _, p := range ad.Response.Account.Protocol {
		if p.SSL != "on" {
			t.Errorf("%s: SSL = %q, want on", p.Type, p.SSL)
		}
	}
}
