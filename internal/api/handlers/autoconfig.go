package handlers

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

type AutoconfigHandler struct {
	db *gorm.DB
}

func NewAutoconfigHandler(db *gorm.DB) *AutoconfigHandler {
	return &AutoconfigHandler{db: db}
}

// MozillaAutoconfig serves Mozilla Thunderbird autoconfig XML.
// GET /mail/config-v1.1.xml?emailaddress=user@domain
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

// mozillaClientConfig builds the Thunderbird autoconfig document for a domain. Split out
// from the handler so what it advertises can be asserted without a database, since the
// ports have nothing to do with one.
func mozillaClientConfig(domainName string) acClientConfig {
	return acClientConfig{
		Version: "1.1",
		Provider: acEmailProvider{
			ID:          domainName,
			Domain:      domainName,
			DisplayName: domainName + " Mail",
			Incoming: acServer{
				Type:     "imap",
				Hostname: domainName,
				Port:     autoconfigIMAPPort,
				// The password travels inside TLS; "cleartext" here describes the SASL
				// mechanism, not the connection.
				SocketType:     "SSL",
				Authentication: "password-cleartext",
				Username:       "%EMAILADDRESS%",
			},
			Outgoing: acOutServer{
				Type:           "smtp",
				Hostname:       domainName,
				Port:           autoconfigSMTPPort,
				SocketType:     "SSL",
				Authentication: "password-cleartext",
				Username:       "%EMAILADDRESS%",
			},
		},
	}
}

func (h *AutoconfigHandler) MozillaAutoconfig(w http.ResponseWriter, r *http.Request) {
	email := r.URL.Query().Get("emailaddress")
	if email == "" {
		http.Error(w, "emailaddress parameter required", http.StatusBadRequest)
		return
	}

	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 {
		http.Error(w, "invalid email address", http.StatusBadRequest)
		return
	}
	domainName := parts[1]

	var domain models.Domain
	if err := h.db.Where("name = ? AND active = ?", domainName, true).First(&domain).Error; err != nil {
		http.Error(w, "domain not found", http.StatusNotFound)
		return
	}

	config := mozillaClientConfig(domainName)

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
func autodiscoverResponse(domainName, email string) adRoot {
	return adRoot{
		Xmlns: "http://schemas.microsoft.com/exchange/autodiscover/responseschema/2006",
		Response: adResponse{
			Xmlns: "http://schemas.microsoft.com/exchange/autodiscover/outlook/responseschema/2006a",
			Account: adAccount{
				AccountType: "email",
				Action:      "settings",
				Protocol: []adProtocol{
					{Type: "IMAP", Server: domainName, Port: autoconfigIMAPPort, SSL: "on", Encryption: "SSL", LoginName: email},
					{Type: "SMTP", Server: domainName, Port: autoconfigSMTPPort, SSL: "on", Encryption: "SSL", LoginName: email},
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

	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 {
		http.Error(w, "invalid email address", http.StatusBadRequest)
		return
	}
	domainName := parts[1]

	var domain models.Domain
	if err := h.db.Where("name = ? AND active = ?", domainName, true).First(&domain).Error; err != nil {
		http.Error(w, "domain not found", http.StatusNotFound)
		return
	}

	// Build the Autodiscover response through encoding/xml so the
	// caller-controlled email/local-part is XML-escaped rather than reflected into
	// the response as markup (element injection). The namespaces are carried as
	// explicit xmlns attributes to reproduce the exact two-namespace document
	// shape Outlook expects. Mirrors the safe Mozilla path above.
	resp := autodiscoverResponse(domainName, email)

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, xml.Header)
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	_ = enc.Encode(resp)
}
