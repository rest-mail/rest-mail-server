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

	type Server struct {
		XMLName        xml.Name `xml:"incomingServer"`
		Type           string   `xml:"type,attr"`
		Hostname       string   `xml:"hostname"`
		Port           int      `xml:"port"`
		SocketType     string   `xml:"socketType"`
		Authentication string   `xml:"authentication"`
		Username       string   `xml:"username"`
	}
	type OutServer struct {
		XMLName        xml.Name `xml:"outgoingServer"`
		Type           string   `xml:"type,attr"`
		Hostname       string   `xml:"hostname"`
		Port           int      `xml:"port"`
		SocketType     string   `xml:"socketType"`
		Authentication string   `xml:"authentication"`
		Username       string   `xml:"username"`
	}
	type EmailProvider struct {
		XMLName     xml.Name  `xml:"emailProvider"`
		ID          string    `xml:"id,attr"`
		Domain      string    `xml:"domain"`
		DisplayName string    `xml:"displayName"`
		Incoming    Server    `xml:"incomingServer"`
		Outgoing    OutServer `xml:"outgoingServer"`
	}
	type ClientConfig struct {
		XMLName  xml.Name      `xml:"clientConfig"`
		Version  string        `xml:"version,attr"`
		Provider EmailProvider `xml:"emailProvider"`
	}

	config := ClientConfig{
		Version: "1.1",
		Provider: EmailProvider{
			ID:          domainName,
			Domain:      domainName,
			DisplayName: domainName + " Mail",
			Incoming: Server{
				Type:           "imap",
				Hostname:       domainName,
				Port:           993,
				SocketType:     "SSL",
				Authentication: "password-cleartext",
				Username:       "%EMAILADDRESS%",
			},
			Outgoing: OutServer{
				Type:           "smtp",
				Hostname:       domainName,
				Port:           587,
				SocketType:     "STARTTLS",
				Authentication: "password-cleartext",
				Username:       "%EMAILADDRESS%",
			},
		},
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, xml.Header)
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	enc.Encode(config)
}

// MicrosoftAutodiscover serves Microsoft Outlook Autodiscover XML.
// POST /autodiscover/autodiscover.xml
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

	// Build Autodiscover response
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="utf-8"?>
<Autodiscover xmlns="http://schemas.microsoft.com/exchange/autodiscover/responseschema/2006">
  <Response xmlns="http://schemas.microsoft.com/exchange/autodiscover/outlook/responseschema/2006a">
    <Account>
      <AccountType>email</AccountType>
      <Action>settings</Action>
      <Protocol>
        <Type>IMAP</Type>
        <Server>%s</Server>
        <Port>993</Port>
        <SSL>on</SSL>
        <LoginName>%s</LoginName>
      </Protocol>
      <Protocol>
        <Type>SMTP</Type>
        <Server>%s</Server>
        <Port>587</Port>
        <Encryption>TLS</Encryption>
        <LoginName>%s</LoginName>
      </Protocol>
    </Account>
  </Response>
</Autodiscover>
`, domainName, email, domainName, email)
}
