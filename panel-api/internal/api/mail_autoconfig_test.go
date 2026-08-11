package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type acDomainRepo struct {
	repository.DomainRepository
	byName map[string]*models.Domain
}

func (r *acDomainRepo) FindByName(_ context.Context, name string) (*models.Domain, error) {
	if d, ok := r.byName[name]; ok {
		return d, nil
	}
	return nil, repository.ErrNotFound
}

func newAutoconfigRouter(repo repository.DomainRepository) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterMailAutoconfigRoutes(r, MailAutoconfigHandlerConfig{Domains: repo})
	return r
}

func mailRepo() *acDomainRepo {
	return &acDomainRepo{byName: map[string]*models.Domain{
		"example.com": {Name: "example.com", EmailEnabled: true, MailProvider: "jabali"},
		// mail disabled — must NOT be advertised
		"nomail.com": {Name: "nomail.com", EmailEnabled: false, MailProvider: "jabali"},
		// external MX — jabali doesn't host its mail
		"ext.com": {Name: "ext.com", EmailEnabled: true, MailProvider: "m365"},
	}}
}

func acDo(r *gin.Engine, method, path, host, body string) *httptest.ResponseRecorder {
	var rd *strings.Reader
	if body != "" {
		rd = strings.NewReader(body)
	} else {
		rd = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rd)
	if host != "" {
		req.Host = host
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestMozillaConfig_ServesXMLForHostedDomain(t *testing.T) {
	r := newAutoconfigRouter(mailRepo())
	w := acDo(r, http.MethodGet, "/mail/config-v1.1.xml", "autoconfig.example.com", "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/xml") {
		t.Errorf("content-type = %q", ct)
	}
	b := w.Body.String()
	for _, want := range []string{
		`<clientConfig version="1.1">`,
		`<emailProvider id="example.com">`,
		`<hostname>mail.example.com</hostname>`,
		`<incomingServer type="imap">`, `<port>993</port>`,
		`<outgoingServer type="smtp">`, `<port>465</port>`,
		`<socketType>SSL</socketType>`,
		`<username>%EMAILADDRESS%</username>`,
	} {
		if !strings.Contains(b, want) {
			t.Errorf("body missing %q\n---\n%s", want, b)
		}
	}
}

func TestMozillaConfig_EmailAddressQueryWinsOverHost(t *testing.T) {
	r := newAutoconfigRouter(mailRepo())
	// Host is the panel's own; the emailaddress param names the real domain.
	w := acDo(r, http.MethodGet, "/mail/config-v1.1.xml?emailaddress=bob@example.com", "panel.other.tld", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "<hostname>mail.example.com</hostname>") {
		t.Errorf("expected example.com mail host, got:\n%s", w.Body.String())
	}
}

func TestMozillaConfig_404ForUnknownDisabledOrExternal(t *testing.T) {
	r := newAutoconfigRouter(mailRepo())
	for _, host := range []string{"autoconfig.unknown.com", "autoconfig.nomail.com", "autoconfig.ext.com"} {
		w := acDo(r, http.MethodGet, "/mail/config-v1.1.xml", host, "")
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", host, w.Code)
		}
	}
}

func TestAutodiscover_EchoesPostedEmail(t *testing.T) {
	r := newAutoconfigRouter(mailRepo())
	body := `<?xml version="1.0"?>
<Autodiscover xmlns="http://schemas.microsoft.com/exchange/autodiscover/outlook/requestschema/2006">
  <Request><EMailAddress>alice@example.com</EMailAddress>
  <AcceptableResponseSchema>http://schemas.microsoft.com/exchange/autodiscover/outlook/responseschema/2006a</AcceptableResponseSchema></Request>
</Autodiscover>`
	w := acDo(r, http.MethodPost, "/autodiscover/autodiscover.xml", "autodiscover.example.com", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", w.Code, w.Body.String())
	}
	b := w.Body.String()
	for _, want := range []string{
		"responseschema/2006a",
		"<Type>IMAP</Type>", "<Server>mail.example.com</Server>", "<Port>993</Port>",
		"<Type>SMTP</Type>", "<Port>465</Port>",
		"<LoginName>alice@example.com</LoginName>",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("body missing %q\n---\n%s", want, b)
		}
	}
}

func TestAutodiscover_CapitalisedPathAndGET(t *testing.T) {
	r := newAutoconfigRouter(mailRepo())
	// Outlook uses the capitalised path; GET with an emailaddress query.
	w := acDo(r, http.MethodGet, "/Autodiscover/Autodiscover.xml?emailaddress=bob@example.com", "autodiscover.example.com", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "<Server>mail.example.com</Server>") {
		t.Errorf("expected mail.example.com:\n%s", w.Body.String())
	}
}

// A hostile local-part must be XML-escaped in LoginName, never break the doc.
func TestAutodiscover_EscapesHostileLoginName(t *testing.T) {
	r := newAutoconfigRouter(mailRepo())
	body := `<Autodiscover><Request><EMailAddress>a"&lt;x&gt;@example.com</EMailAddress></Request></Autodiscover>`
	w := acDo(r, http.MethodPost, "/autodiscover/autodiscover.xml", "autodiscover.example.com", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	b := w.Body.String()
	if strings.Contains(b, "<x>") {
		t.Errorf("unescaped angle brackets leaked into LoginName:\n%s", b)
	}
	if !strings.Contains(b, "example.com") {
		t.Errorf("domain lost:\n%s", b)
	}
}

func TestStripMailHostPrefix(t *testing.T) {
	cases := map[string]string{
		"autoconfig.example.com":   "example.com",
		"autodiscover.example.com": "example.com",
		"mail.example.com":         "example.com",
		"mta-sts.example.com":      "example.com",
		"example.com":              "example.com",
		"AUTOCONFIG.Example.COM":   "example.com",
	}
	for in, want := range cases {
		if got := stripMailHostPrefix(in); got != want {
			t.Errorf("stripMailHostPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDomainFromEmail(t *testing.T) {
	ok := map[string]string{
		"bob@example.com":      "example.com",
		"a.b+c@Sub.Example.io": "sub.example.io",
	}
	for in, want := range ok {
		if got := domainFromEmail(in); got != want {
			t.Errorf("domainFromEmail(%q) = %q, want %q", in, got, want)
		}
	}
	// The domain part is what matters; a garbage local part still yields a
	// clean domain (that's the caller's lookup key, and LoginName is escaped).
	for _, bad := range []string{"", "no-at", "@nope", "trailing@", "nodot@localhost", "bob@dom ain.com"} {
		if got := domainFromEmail(bad); got != "" {
			t.Errorf("domainFromEmail(%q) = %q, want empty", bad, got)
		}
	}
}
