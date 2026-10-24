package handlers

import (
	"encoding/xml"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// AutoconfigHandler tells mail apps where to connect. The server is the mail
// host, the name REST Mail answers mail on and the MX points to, never the
// email domain itself (#722): that is usually a website, and the certificate a
// client checks is the mail host's.
type AutoconfigHandler struct {
	db *gorm.DB
	// mailHost is GATEWAY_HOSTNAME, as RESTMAIL discovery uses it; empty when
	// it is not a fully-qualified name, and then there is nothing to advertise.
	mailHost string
}

func NewAutoconfigHandler(db *gorm.DB, gatewayHostname string) *AutoconfigHandler {
	h := &AutoconfigHandler{db: db}
	if host, err := validateDomainName(gatewayHostname); err == nil {
		h.mailHost = host
	}
	return h
}

// requestDomain is the email domain a lookup asks about: the domain of
// emailaddress when the client sends it, else the host the request was made
// to (draft-ietf-mailmaint-autoconfig §4.1, #721): autoconfig.<domain> for
// /mail/config-v1.1.xml, <domain> itself for the .well-known URL. Its A-label
// form, lowercased, as hosted domains are stored.
func requestDomain(r *http.Request, email string) (string, bool) {
	name := ""
	if email != "" {
		at := strings.LastIndex(email, "@")
		if at < 0 {
			return "", false
		}
		name = email[at+1:]
	} else {
		name = r.Host
		if host, _, err := net.SplitHostPort(name); err == nil {
			name = host
		}
		if !strings.HasPrefix(r.URL.Path, "/.well-known/") {
			lower := strings.ToLower(name)
			if !strings.HasPrefix(lower, "autoconfig.") {
				return "", false
			}
			name = name[len("autoconfig."):]
		}
	}
	domain, err := validateDomainName(name)
	if err != nil {
		return "", false
	}
	return strings.ToLower(domain), true
}

// hostedDomain reports whether REST Mail hosts the active domain name.
func (h *AutoconfigHandler) hostedDomain(name string) bool {
	var domain models.Domain
	return h.db.Where("name = ? AND active = ?", name, true).First(&domain).Error == nil
}

// MozillaAutoconfig serves Mozilla Thunderbird autoconfig XML.
// GET /mail/config-v1.1.xml[?emailaddress=user@domain] on autoconfig.<domain>,
// and GET /.well-known/autoconfig/mail/config-v1.1.xml on <domain>.
// The ports rest-mail advertises to mail clients. Client access is implicit TLS only —
// there is no listener on 587, 143 or 110 — and autoconfig is where that has to be
// stated, because whatever a client reads here is what it saves and keeps using.
//
// socketType SSL means "TLS from the first byte". STARTTLS would tell the client to
// connect in the clear and hope the upgrade happens.
const (
	autoconfigIMAPPort = 993
	autoconfigSMTPPort = 465
)

type acServer struct {
	XMLName        xml.Name `xml:"incomingServer"`
	Type           string   `xml:"type,attr"`
	Hostname       string   `xml:"hostname"`
	Port           int      `xml:"port"`
	SocketType     string   `xml:"socketType"`
	Authentication string   `xml:"authentication"`
	Username       string   `xml:"username"`
}

type acOutServer struct {
	XMLName        xml.Name `xml:"outgoingServer"`
	Type           string   `xml:"type,attr"`
	Hostname       string   `xml:"hostname"`
	Port           int      `xml:"port"`
	SocketType     string   `xml:"socketType"`
	Authentication string   `xml:"authentication"`
	Username       string   `xml:"username"`
}

type acEmailProvider struct {
	XMLName     xml.Name    `xml:"emailProvider"`
	ID          string      `xml:"id,attr"`
	Domain      string      `xml:"domain"`
	DisplayName string      `xml:"displayName"`
	Incoming    acServer    `xml:"incomingServer"`
	Outgoing    acOutServer `xml:"outgoingServer"`
}

type acClientConfig struct {
	XMLName  xml.Name        `xml:"clientConfig"`
	Version  string          `xml:"version,attr"`
	Provider acEmailProvider `xml:"emailProvider"`
}

// mozillaClientConfig builds the Thunderbird autoconfig document for a domain served on
// mailHost. Split out from the handler so what it advertises can be asserted without a
// database, since the ports have nothing to do with one.
func mozillaClientConfig(domainName, mailHost string) acClientConfig {
	return acClientConfig{
		Version: "1.1",
		Provider: acEmailProvider{
			ID:          domainName,
			Domain:      domainName,
			DisplayName: domainName + " Mail",
			Incoming: acServer{
				Type:     "imap",
				Hostname: mailHost,
				Port:     autoconfigIMAPPort,
				// The password travels inside TLS; "cleartext" here describes the SASL
				// mechanism, not the connection.
				SocketType:     "SSL",
				Authentication: "password-cleartext",
				Username:       "%EMAILADDRESS%",
			},
			Outgoing: acOutServer{
				Type:           "smtp",
				Hostname:       mailHost,
				Port:           autoconfigSMTPPort,
				SocketType:     "SSL",
				Authentication: "password-cleartext",
				Username:       "%EMAILADDRESS%",
			},
		},
	}
}

func (h *AutoconfigHandler) MozillaAutoconfig(w http.ResponseWriter, r *http.Request) {
	domainName, ok := requestDomain(r, r.URL.Query().Get("emailaddress"))
	if !ok {
		http.Error(w, "no email domain in the address or the host name", http.StatusBadRequest)
		return
	}
	if h.mailHost == "" {
		http.Error(w, "mail host not configured", http.StatusServiceUnavailable)
		return
	}
	if !h.hostedDomain(domainName) {
		http.Error(w, "domain not found", http.StatusNotFound)
		return
	}

	config := mozillaClientConfig(domainName, h.mailHost)

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, xml.Header)
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	_ = enc.Encode(config)
}

// MicrosoftAutodiscover serves Microsoft Outlook Autodiscover XML.
// POST /autodiscover/autodiscover.xml
type adProtocol struct {
	Type       string `xml:"Type"`
	Server     string `xml:"Server"`
	Port       int    `xml:"Port"`
	SSL        string `xml:"SSL,omitempty"`
	Encryption string `xml:"Encryption,omitempty"`
	LoginName  string `xml:"LoginName"`
}

type adAccount struct {
	AccountType string       `xml:"AccountType"`
	Action      string       `xml:"Action"`
	Protocol    []adProtocol `xml:"Protocol"`
}

type adResponse struct {
	XMLName xml.Name  `xml:"Response"`
	Xmlns   string    `xml:"xmlns,attr"`
	Account adAccount `xml:"Account"`
}

type adRoot struct {
	XMLName  xml.Name   `xml:"Autodiscover"`
	Xmlns    string     `xml:"xmlns,attr"`
	Response adResponse `xml:"Response"`
}

// autodiscoverResponse builds the Outlook Autodiscover document. Extracted from the
// handler for the same reason as mozillaClientConfig: the advertised ports are worth
// asserting and have nothing to do with the database.
//
// SSL=on with Encryption=SSL is implicit TLS. Encryption=TLS is how the schema spells
// STARTTLS, which is what this used to send for SMTP on 587.
func autodiscoverResponse(mailHost, email string) adRoot {
	return adRoot{
		Xmlns: "http://schemas.microsoft.com/exchange/autodiscover/responseschema/2006",
		Response: adResponse{
			Xmlns: "http://schemas.microsoft.com/exchange/autodiscover/outlook/responseschema/2006a",
			Account: adAccount{
				AccountType: "email",
				Action:      "settings",
				Protocol: []adProtocol{
					{Type: "IMAP", Server: mailHost, Port: autoconfigIMAPPort, SSL: "on", Encryption: "SSL", LoginName: email},
					{Type: "SMTP", Server: mailHost, Port: autoconfigSMTPPort, SSL: "on", Encryption: "SSL", LoginName: email},
				},
			},
		},
	}
}

func (h *AutoconfigHandler) MicrosoftAutodiscover(w http.ResponseWriter, r *http.Request) {
	// Parse the request to get the email address
	type AutodiscoverRequest struct {
		XMLName xml.Name `xml:"Autodiscover"`
		Request struct {
			EMailAddress string `xml:"EMailAddress"`
		} `xml:"Request"`
	}

	var adReq AutodiscoverRequest
	if err := xml.NewDecoder(r.Body).Decode(&adReq); err != nil {
		// Also accept query param fallback
		email := r.URL.Query().Get("emailaddress")
		if email == "" {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		adReq.Request.EMailAddress = email
	}

	email := adReq.Request.EMailAddress
	if email == "" {
		http.Error(w, "email address required", http.StatusBadRequest)
		return
	}
	domainName, ok := requestDomain(r, email)
	if !ok {
		http.Error(w, "invalid email address", http.StatusBadRequest)
		return
	}
	if h.mailHost == "" {
		http.Error(w, "mail host not configured", http.StatusServiceUnavailable)
		return
	}
	if !h.hostedDomain(domainName) {
		http.Error(w, "domain not found", http.StatusNotFound)
		return
	}

	// Build the Autodiscover response through encoding/xml so the
	// caller-controlled email/local-part is XML-escaped rather than reflected into
	// the response as markup (element injection). The namespaces are carried as
	// explicit xmlns attributes to reproduce the exact two-namespace document
	// shape Outlook expects. Mirrors the safe Mozilla path above.
	resp := autodiscoverResponse(h.mailHost, email)

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, xml.Header)
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	_ = enc.Encode(resp)
}
