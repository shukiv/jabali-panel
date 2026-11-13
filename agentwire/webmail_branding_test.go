package agentwire

import (
	"strings"
	"testing"
)

func TestWebmailBrandingDomainValidate(t *testing.T) {
	ok := WebmailBrandingDomain{
		Host:         "mail.customer-a.com",
		AppName:      `Bob's "Mail"`,
		WebsiteURL:   "https://customer-a.com/?a=1",
		LogoLightURL: "/jabali-webmail-branding/01HZZZZZZZZZZZZZZZZZZZZZZZ/logo-light.png?v=0123456789ab",
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid entry refused: %v", err)
	}
	for name, change := range map[string]func(d *WebmailBrandingDomain){
		"host not mail.":       func(d *WebmailBrandingDomain) { d.Host = "customer-a.com" },
		"host upper case":      func(d *WebmailBrandingDomain) { d.Host = "mail.Customer-A.com" },
		"host underscore":      func(d *WebmailBrandingDomain) { d.Host = "mail.old_import.com" },
		"host too long":        func(d *WebmailBrandingDomain) { d.Host = "mail" + strings.Repeat(".abcdefghij", 25) },
		"javascript link":      func(d *WebmailBrandingDomain) { d.PrivacyURL = "javascript:alert(1)" },
		"link with user info":  func(d *WebmailBrandingDomain) { d.ImprintURL = "https://user@evil.example/" },
		"picture elsewhere":    func(d *WebmailBrandingDomain) { d.FaviconURL = "https://evil.example/x.png" },
		"picture path escapes": func(d *WebmailBrandingDomain) { d.LogoDarkURL = "/jabali-webmail-branding/../../etc/passwd" },
		"newline in a name":    func(d *WebmailBrandingDomain) { d.AppName = "A\nAPP_NAME=x" },
		"name too long":        func(d *WebmailBrandingDomain) { d.CompanyName = strings.Repeat("x", WebmailBrandingMaxText+1) },
	} {
		d := ok
		change(&d)
		if err := d.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// What CleanWebmailBrandingText returns is always a name the agent takes.
func TestCleanWebmailBrandingText(t *testing.T) {
	for in, want := range map[string]string{
		"Acme Hosting":           "Acme Hosting",
		"Acme\nAPP_NAME=x":       "Acme APP_NAME=x",
		"  Acme\t ":              "Acme",
		strings.Repeat("א", 200): strings.Repeat("א", WebmailBrandingMaxText),
		"bad \xff utf8":          "bad  utf8",
	} {
		got := CleanWebmailBrandingText(in)
		if got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
		if !ValidWebmailBrandingText(got) {
			t.Errorf("Clean(%q) = %q, which the agent refuses", in, got)
		}
	}
}
