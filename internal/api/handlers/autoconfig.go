package handlers

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail/internal/api/endpoint"
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
func (h *AutoconfigHandler) hostedDomain(ctx context.Context, name string) (bool, error) {
	var domain models.Domain
	err := h.db.WithContext(ctx).Where("name = ? AND active = ?", name, true).First(&domain).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("look up autoconfig domain: %w", err)
	}
	return true, nil
}

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

// AutodiscoverMaxBody caps an Autodiscover request, a few hundred bytes of XML, so the public
// route cannot be made to read an unbounded body.
const AutodiscoverMaxBody = 64 << 10

// AutoconfigRouteChecks are what the discovery routes are registered behind: the body cap of
// the Autodiscover POST.
type AutoconfigRouteChecks struct {
	AutodiscoverBody endpoint.Middleware
}

// Register mounts the documents a mail client fetches to find the server (#631): Mozilla's
// autoconfig at autoconfig.<domain> and at the domain's .well-known address, and Outlook's
// Autodiscover. None needs a credential. Their refusals are plain text, as they always were.
func (h *AutoconfigHandler) Register(rs endpoint.Routes, checks AutoconfigRouteChecks) {
	tags := []string{"System"}
	text := func(status int, description string) oapifly.EndpointResponse {
		return oapifly.EndpointResponse{Status: status, Description: description, Type: oapifly.TypeOf[string](), MediaType: "text/plain"}
	}
	unconfigured := text(http.StatusServiceUnavailable, "The mail host (GATEWAY_HOSTNAME) is not configured")
	autoconfig := []oapifly.EndpointResponse{
		text(http.StatusBadRequest, "No email domain in the address or the host name"),
		text(http.StatusNotFound, "Domain not found"),
		unconfigured,
	}
	const document = "Autoconfig XML document. The incoming and outgoing servers are the mail host " +
		"(GATEWAY_HOSTNAME), on IMAP 993 and SMTP 465 with implicit TLS."
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/mail/config-v1.1.xml",
		OperationID: "mozillaAutoconfig", Summary: "Mozilla/Thunderbird autoconfiguration", Tags: tags,
		Description: document, MediaType: "application/xml", Security: oapifly.Public, Responses: autoconfig}, h.mozillaAutoconfig)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/.well-known/autoconfig/mail/config-v1.1.xml",
		OperationID: "mozillaAutoconfigWellKnown", Summary: "Mozilla/Thunderbird autoconfiguration (well-known path)", Tags: tags,
		Description: document, MediaType: "application/xml", Security: oapifly.Public, Responses: autoconfig}, h.mozillaAutoconfig)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/autodiscover/autodiscover.xml",
		OperationID: "microsoftAutodiscover", Summary: "Microsoft Outlook autodiscover", Tags: tags,
		MediaType: "application/xml", Security: oapifly.Public,
		Responses: []oapifly.EndpointResponse{
			text(http.StatusBadRequest, "No usable email address, refused before any domain is looked up. A body that is "+
				"not Autodiscover XML falls back to the emailaddress query parameter: with neither, \"invalid request\". "+
				"An Autodiscover request without an EMailAddress gets \"email address required\", and an address "+
				"without an @ gets \"invalid email address\". Plain text, ending in a newline."),
			text(http.StatusNotFound, "Domain not found"),
			unconfigured,
		},
		Middleware: []endpoint.Middleware{checks.AutodiscoverBody}}, h.microsoftAutodiscover)
}

// autoconfigRequest is a Mozilla autoconfig lookup. emailaddress is optional
// (draft-ietf-mailmaint-autoconfig §4.1): without it, the email domain is the host the
// request was made to.
type autoconfigRequest struct {
	EmailAddress string `query:"emailaddress" description:"Optional (draft-ietf-mailmaint-autoconfig §4.1). Without it, the email domain is the host the request is made to."`
}

// mozillaAutoconfig answers Thunderbird's autoconfig document for a hosted domain, at
// /mail/config-v1.1.xml on autoconfig.<domain> and at /.well-known/autoconfig/mail/config-v1.1.xml
// on the domain itself.
func (h *AutoconfigHandler) mozillaAutoconfig(r *http.Request, in autoconfigRequest) (endpoint.Document, error) {
	domainName, ok := requestDomain(r, in.EmailAddress)
	if !ok {
		return endpoint.Document{}, endpoint.RefuseText(http.StatusBadRequest, "no email domain in the address or the host name")
	}
	if h.mailHost == "" {
		return endpoint.Document{}, endpoint.RefuseText(http.StatusServiceUnavailable, "mail host not configured")
	}
	hosted, err := h.hostedDomain(r.Context(), domainName)
	if err != nil {
		return endpoint.Document{}, err
	}
	if !hosted {
		return endpoint.Document{}, endpoint.RefuseText(http.StatusNotFound, "domain not found")
	}
	return xmlDocument(mozillaClientConfig(domainName, h.mailHost)), nil
}

// autodiscoverRequest is an Outlook Autodiscover request: its XML names the address, and with
// no body, or one that is not Autodiscover XML, the emailaddress query parameter does.
type autodiscoverRequest struct {
	EmailAddress string  `query:"emailaddress" description:"The address to look up when the body is not Autodiscover XML."`
	Body         *string `body:"application/xml"`
}

// microsoftAutodiscover answers Outlook's Autodiscover document for an address at a hosted
// domain.
func (h *AutoconfigHandler) microsoftAutodiscover(r *http.Request, in autodiscoverRequest) (endpoint.Document, error) {
	var adReq struct {
		XMLName xml.Name `xml:"Autodiscover"`
		Request struct {
			EMailAddress string `xml:"EMailAddress"`
		} `xml:"Request"`
	}
	var body []byte
	if in.Body != nil {
		body = []byte(*in.Body)
	}
	if err := xml.Unmarshal(body, &adReq); err != nil {
		if in.EmailAddress == "" {
			return endpoint.Document{}, endpoint.RefuseText(http.StatusBadRequest, "invalid request")
		}
		adReq.Request.EMailAddress = in.EmailAddress
	}

	email := adReq.Request.EMailAddress
	if email == "" {
		return endpoint.Document{}, endpoint.RefuseText(http.StatusBadRequest, "email address required")
	}
	domainName, ok := requestDomain(r, email)
	if !ok {
		return endpoint.Document{}, endpoint.RefuseText(http.StatusBadRequest, "invalid email address")
	}
	if h.mailHost == "" {
		return endpoint.Document{}, endpoint.RefuseText(http.StatusServiceUnavailable, "mail host not configured")
	}
	hosted, err := h.hostedDomain(r.Context(), domainName)
	if err != nil {
		return endpoint.Document{}, err
	}
	if !hosted {
		return endpoint.Document{}, endpoint.RefuseText(http.StatusNotFound, "domain not found")
	}

	// Built through encoding/xml so the caller-controlled address is XML-escaped rather
	// than reflected into the response as markup (element injection). The namespaces are
	// carried as explicit xmlns attributes to reproduce the exact two-namespace document
	// shape Outlook expects.
	return xmlDocument(autodiscoverResponse(h.mailHost, email)), nil
}

// xmlDocument is v written as an indented XML document, with its declaration.
func xmlDocument(v any) endpoint.Document {
	var body bytes.Buffer
	body.WriteString(xml.Header)
	enc := xml.NewEncoder(&body)
	enc.Indent("", "  ")
	_ = enc.Encode(v)
	return endpoint.Document{ContentType: "application/xml; charset=utf-8", Body: body.Bytes()}
}
