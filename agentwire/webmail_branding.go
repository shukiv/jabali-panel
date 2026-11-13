package agentwire

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Webmail branding (GH #2502). panel-api's reconciler sends
// webmail.branding.apply the server-wide brand text and one
// WebmailBrandingDomain per domain whose branding applies; the agent writes
// them to webmail's environment. The agent refuses the whole call over one
// value it doesn't take, which would freeze every domain's branding, so
// panel-api leaves out an entry that fails Validate before sending. Both
// sides use the rules below, so they cannot drift apart.

const (
	// WebmailBrandingMaxDomains is the most entries one call may carry.
	WebmailBrandingMaxDomains = 10000
	// WebmailBrandingMaxText is the most characters of a name.
	WebmailBrandingMaxText = 128
	// WebmailBrandingMaxLink is the most bytes of a link.
	WebmailBrandingMaxLink = 512
	// WebmailBrandingMaxHost is the most bytes of a host.
	WebmailBrandingMaxHost = 253
)

var (
	// webmailBrandingHostRe is a domain's webmail address, mail.<domain>,
	// lower case.
	webmailBrandingHostRe = regexp.MustCompile(`^mail(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?){2,}$`)
	// webmailBrandingPictureRe is a picture panel-api stored, as the webmail
	// vhost serves it (webmailbranding.PictureURL).
	webmailBrandingPictureRe = regexp.MustCompile(`^/jabali-webmail-branding/[0-9A-HJKMNP-TV-Z]{26}/(logo-light|logo-dark|favicon)\.png\?v=[0-9a-f]{1,64}$`)
)

// WebmailBrandingDomain is one entry of webmail.branding.apply's domains.
// Empty fields keep the server-wide branding.
type WebmailBrandingDomain struct {
	Host         string `json:"host"`
	AppName      string `json:"app_name,omitempty"`
	CompanyName  string `json:"company_name,omitempty"`
	WebsiteURL   string `json:"website_url,omitempty"`
	PrivacyURL   string `json:"privacy_url,omitempty"`
	ImprintURL   string `json:"imprint_url,omitempty"`
	LogoLightURL string `json:"logo_light_url,omitempty"`
	LogoDarkURL  string `json:"logo_dark_url,omitempty"`
	FaviconURL   string `json:"favicon_url,omitempty"`
}

// Validate reports the first value of d the agent does not take.
func (d WebmailBrandingDomain) Validate() error {
	if len(d.Host) > WebmailBrandingMaxHost || !webmailBrandingHostRe.MatchString(d.Host) {
		return fmt.Errorf("host: %q is not a webmail address", d.Host)
	}
	for _, f := range []struct{ name, v string }{{"app_name", d.AppName}, {"company_name", d.CompanyName}} {
		if !ValidWebmailBrandingText(f.v) {
			return fmt.Errorf("%s: not a valid name", f.name)
		}
	}
	for _, f := range []struct{ name, v string }{{"website_url", d.WebsiteURL}, {"privacy_url", d.PrivacyURL}, {"imprint_url", d.ImprintURL}} {
		if !validWebmailBrandingLink(f.v) {
			return fmt.Errorf("%s: not an http(s) address", f.name)
		}
	}
	for _, f := range []struct{ name, v string }{{"logo_light_url", d.LogoLightURL}, {"logo_dark_url", d.LogoDarkURL}, {"favicon_url", d.FaviconURL}} {
		if f.v != "" && !webmailBrandingPictureRe.MatchString(f.v) {
			return fmt.Errorf("%s: not a stored branding picture", f.name)
		}
	}
	return nil
}

// ValidWebmailBrandingText: a name webmail shows, at most
// WebmailBrandingMaxText characters, no control characters.
func ValidWebmailBrandingText(s string) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > WebmailBrandingMaxText {
		return false
	}
	return strings.IndexFunc(s, unicode.IsControl) < 0
}

// CleanWebmailBrandingText makes s a name ValidWebmailBrandingText takes:
// control characters become spaces and it is cut to WebmailBrandingMaxText
// characters.
func CleanWebmailBrandingText(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	if r := []rune(s); len(r) > WebmailBrandingMaxText {
		s = string(r[:WebmailBrandingMaxText])
	}
	return strings.TrimSpace(s)
}

// validWebmailBrandingLink: empty, or an http(s) URL with a host and no
// user info.
func validWebmailBrandingLink(s string) bool {
	if s == "" {
		return true
	}
	if len(s) > WebmailBrandingMaxLink || strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil
}
