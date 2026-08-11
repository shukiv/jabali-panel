package api

import (
	"encoding/xml"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Mail-client autoconfiguration (GH #1039). Jabali publishes the DNS side
// (autoconfig/autodiscover CNAMEs + the RFC 6186 SRVs) and the cert SANs, but
// served no config document, so Thunderbird's
//   GET  autoconfig.<domain>/mail/config-v1.1.xml
// fell through the mail vhost's `location /` to Bulwark webmail (a login page,
// not XML), and Outlook's
//   POST autodiscover.<domain>/autodiscover/autodiscover.xml
// was explicitly 404'd. This handler renders both from the domain's real mail
// settings so a fresh IMAP client configures itself with no manual entry.
//
// Servers advertised match the Stalwart listeners + the RFC 6186 SRVs
// (email_records.go): host mail.<domain>, IMAPS 993, SMTPS 465 — both implicit
// TLS, password auth. The mail host is always taken from the DB row (trusted),
// never from the request, so a spoofed Host header cannot inject a hostname.
//
// The endpoints are unauthenticated by design (a mail client has no panel
// session when it first probes) and are reachable only via the mail vhost,
// whose hostnames (mail./autoconfig./autodiscover.<domain>) are already on the
// CrowdSec-AppSec exempt list (webmail_reconcile.go), so the autodiscover XML
// POST body is not WAF-inspected.

const (
	// mailAutoconfigIMAPSPort / SMTPSPort are the implicit-TLS submission
	// ports Stalwart listens on (apply-plan.json.tmpl imap-993 /
	// smtp-submissions-465), matching the _imaps._tcp / _submissions._tcp SRVs.
	mailAutoconfigIMAPSPort = 993
	mailAutoconfigSMTPSPort = 465
)

// MailAutoconfigHandlerConfig wires the public mail-autoconfig endpoints.
type MailAutoconfigHandlerConfig struct {
	Domains repository.DomainRepository
	Log     *slog.Logger
}

type mailAutoconfigHandler struct{ cfg MailAutoconfigHandlerConfig }

// RegisterMailAutoconfigRoutes mounts the discovery endpoints on the engine
// root (not /api/v1) — they are served from the mail vhost, which proxies these
// exact paths to panel-api, and carry no API prefix or Kratos session. Both the
// lower-case (Thunderbird/Apple) and capitalised (Outlook) autodiscover paths
// are registered because gin routing is case-sensitive and the nginx location
// forwards the request URI verbatim.
func RegisterMailAutoconfigRoutes(r gin.IRouter, cfg MailAutoconfigHandlerConfig) {
	h := &mailAutoconfigHandler{cfg: cfg}
	// Thunderbird / Mozilla autoconfig.
	r.GET("/mail/config-v1.1.xml", h.mozillaConfig)
	r.GET("/.well-known/autoconfig/mail/config-v1.1.xml", h.mozillaConfig)
	// Outlook / Exchange autodiscover (POST is the spec path; some clients GET).
	r.POST("/autodiscover/autodiscover.xml", h.autodiscover)
	r.GET("/autodiscover/autodiscover.xml", h.autodiscover)
	r.POST("/Autodiscover/Autodiscover.xml", h.autodiscover)
	r.GET("/Autodiscover/Autodiscover.xml", h.autodiscover)
}

// resolveMailDomain returns the hosted, jabali-mail domain this request is
// asking about, or ("", false) if it isn't one we serve mail for. Preference:
// an explicit email address (query for Thunderbird, POST body for Outlook) wins
// over the Host, because a client may probe autoconfig.<a> for an address
// @<b>; otherwise the autoconfig/autodiscover/mail/mta-sts host prefix is
// stripped. The returned name is the canonical DB value, not the raw input.
func (h *mailAutoconfigHandler) resolveMailDomain(c *gin.Context, emailHint string) (*models.Domain, bool) {
	candidate := domainFromEmail(emailHint)
	if candidate == "" {
		candidate = stripMailHostPrefix(hostWithoutPort(c.Request.Host))
	}
	if candidate == "" {
		return nil, false
	}
	d, err := h.cfg.Domains.FindByName(c.Request.Context(), candidate)
	if err != nil || d == nil {
		return nil, false
	}
	// Only advertise mail.<domain> for domains whose mail we actually host.
	if !d.EmailEnabled || (d.MailProvider != "" && d.MailProvider != "jabali") {
		return nil, false
	}
	return d, true
}

func (h *mailAutoconfigHandler) mozillaConfig(c *gin.Context) {
	d, ok := h.resolveMailDomain(c, c.Query("emailaddress"))
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	mailHost := "mail." + d.Name
	cfg := mozClientConfig{
		Version: "1.1",
		Provider: mozEmailProvider{
			ID:               d.Name,
			Domain:           d.Name,
			DisplayName:      d.Name + " Mail",
			DisplayShortName: d.Name,
			Incoming: mozServer{
				Type: "imap", Hostname: mailHost, Port: mailAutoconfigIMAPSPort,
				SocketType: "SSL", Authentication: "password-cleartext", Username: "%EMAILADDRESS%",
			},
			Outgoing: mozServer{
				Type: "smtp", Hostname: mailHost, Port: mailAutoconfigSMTPSPort,
				SocketType: "SSL", Authentication: "password-cleartext", Username: "%EMAILADDRESS%",
			},
		},
	}
	writeXML(c, cfg)
}

func (h *mailAutoconfigHandler) autodiscover(c *gin.Context) {
	email := c.Query("emailaddress")
	if email == "" {
		if body, err := io.ReadAll(io.LimitReader(c.Request.Body, 64<<10)); err == nil {
			email = autodiscoverRequestEmail(body)
		}
	}
	d, ok := h.resolveMailDomain(c, email)
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	mailHost := "mail." + d.Name
	// LoginName echoes the requested address when we have it (Outlook fills the
	// account form from it); fall back to the Mozilla-style placeholder so the
	// document is still valid when the client GETs without an address.
	login := email
	if login == "" {
		login = "%EMAILADDRESS%"
	}
	resp := adAutodiscover{
		Response: adResponse{
			Account: adAccount{
				AccountType: "email",
				Action:      "settings",
				Protocols: []adProtocol{
					{Type: "IMAP", Server: mailHost, Port: mailAutoconfigIMAPSPort, SSL: "on", SPA: "off", Encryption: "SSL", AuthRequired: "on", LoginName: login},
					{Type: "SMTP", Server: mailHost, Port: mailAutoconfigSMTPSPort, SSL: "on", SPA: "off", Encryption: "SSL", AuthRequired: "on", LoginName: login},
				},
			},
		},
	}
	writeXML(c, resp)
}

// writeXML marshals v with the XML prolog and the application/xml content type
// mail clients expect. encoding/xml escapes every interpolated value, so a
// crafted email address in a LoginName cannot break out of the document.
func writeXML(c *gin.Context, v any) {
	out, err := xml.MarshalIndent(v, "", "  ")
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	c.Header("Content-Type", "application/xml; charset=utf-8")
	c.Header("Cache-Control", "public, max-age=3600")
	_, _ = c.Writer.Write([]byte(xml.Header))
	_, _ = c.Writer.Write(out)
}

// --- helpers -------------------------------------------------------------

func hostWithoutPort(host string) string {
	if i := strings.IndexByte(host, ':'); i >= 0 {
		return host[:i]
	}
	return host
}

// stripMailHostPrefix removes the mail-vhost label (autoconfig/autodiscover/
// mail/mta-sts) so "autoconfig.example.com" resolves the "example.com" row.
func stripMailHostPrefix(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, p := range []string{"autoconfig.", "autodiscover.", "mail.", "mta-sts."} {
		if strings.HasPrefix(host, p) {
			return host[len(p):]
		}
	}
	return host
}

// domainFromEmail returns the lower-cased domain part of an email address, or
// "" if the input isn't a single well-formed address.
func domainFromEmail(email string) string {
	email = strings.TrimSpace(email)
	at := strings.LastIndexByte(email, '@')
	if at <= 0 || at == len(email)-1 {
		return ""
	}
	dom := strings.ToLower(email[at+1:])
	if strings.ContainsAny(dom, " \t\r\n<>\"") || !strings.Contains(dom, ".") {
		return ""
	}
	return dom
}

// autodiscoverRequestEmail pulls <EMailAddress> out of an Outlook autodiscover
// request body without a full schema unmarshal (the request namespace differs
// across Outlook versions).
func autodiscoverRequestEmail(body []byte) string {
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		if se, ok := tok.(xml.StartElement); ok && strings.EqualFold(se.Name.Local, "EMailAddress") {
			var v string
			if dec.DecodeElement(&v, &se) == nil {
				return strings.TrimSpace(v)
			}
			return ""
		}
	}
}

// --- Mozilla autoconfig (clientConfig 1.1) document ----------------------

type mozClientConfig struct {
	XMLName  xml.Name         `xml:"clientConfig"`
	Version  string           `xml:"version,attr"`
	Provider mozEmailProvider `xml:"emailProvider"`
}

type mozEmailProvider struct {
	ID               string    `xml:"id,attr"`
	Domain           string    `xml:"domain"`
	DisplayName      string    `xml:"displayName"`
	DisplayShortName string    `xml:"displayShortName"`
	Incoming         mozServer `xml:"incomingServer"`
	Outgoing         mozServer `xml:"outgoingServer"`
}

type mozServer struct {
	Type           string `xml:"type,attr"`
	Hostname       string `xml:"hostname"`
	Port           int    `xml:"port"`
	SocketType     string `xml:"socketType"`
	Authentication string `xml:"authentication"`
	Username       string `xml:"username"`
}

// --- Outlook autodiscover (Outlook responseschema 2006a) document --------

type adAutodiscover struct {
	XMLName  xml.Name   `xml:"http://schemas.microsoft.com/exchange/autodiscover/responseschema/2006 Autodiscover"`
	Response adResponse `xml:"http://schemas.microsoft.com/exchange/autodiscover/outlook/responseschema/2006a Response"`
}

type adResponse struct {
	Account adAccount `xml:"Account"`
}

type adAccount struct {
	AccountType string       `xml:"AccountType"`
	Action      string       `xml:"Action"`
	Protocols   []adProtocol `xml:"Protocol"`
}

type adProtocol struct {
	Type         string `xml:"Type"`
	Server       string `xml:"Server"`
	Port         int    `xml:"Port"`
	SSL          string `xml:"SSL"`
	SPA          string `xml:"SPA"`
	Encryption   string `xml:"Encryption"`
	AuthRequired string `xml:"AuthRequired"`
	LoginName    string `xml:"LoginName"`
}
