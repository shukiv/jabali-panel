package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// webmail_branding.go — webmail's branding (GH #200, GH #2502). Bulwark is a
// single instance serving every domain. Its server-wide branding mirrors the
// panel's operator branding (the logo the admin uploaded in Server Settings
// and the brand text): the logo is copied into Bulwark's public/branding dir,
// with jabali's own mail logo and "Jabali Mail" when there is none. Each
// domain can also have its own branding for its web address,
// mail.<domain>, which Bulwark matches on the browser's host
// (DOMAIN_BRANDING); its pictures are served by nginx from panel-api's dir.
// panel-api's reconciler sends all of it in one call.
//
// It is written to its own environment file, bulwark-branding.env, which
// jabali-webmail.service loads after bulwark.env, so its keys win over the
// template's defaults there. install.sh renders bulwark.env from its
// template on every update and never writes this file: the branding used to
// be appended to bulwark.env, and every update dropped it.
var (
	panelBrandingDir       = "/var/lib/jabali-panel/branding"
	webmailPublicBranding  = "/opt/jabali-webmail/public/branding"
	webmailBrandingEnvFile = "/etc/jabali-panel/bulwark-branding.env"
)

// webmailEnvFile is webmail's main environment file, rendered by install.sh.
const webmailEnvFile = "/etc/jabali-panel/bulwark.env"

// webmailDomainBranding is one domain's branding as panel-api sends it;
// its rules are agentwire's, which panel-api checks before sending.
type webmailDomainBranding = agentwire.WebmailBrandingDomain

type webmailBrandingApplyParams struct {
	BrandText string                  `json:"brand_text"`
	Domains   []webmailDomainBranding `json:"domains"`
}

// bulwarkDomainBranding is one DOMAIN_BRANDING entry, in Bulwark's names. A
// field left out falls back to the server-wide value.
type bulwarkDomainBranding struct {
	Host                  string `json:"host"`
	AppName               string `json:"appName,omitempty"`
	LoginCompanyName      string `json:"loginCompanyName,omitempty"`
	LoginWebsiteURL       string `json:"loginWebsiteUrl,omitempty"`
	LoginPrivacyPolicyURL string `json:"loginPrivacyPolicyUrl,omitempty"`
	LoginImprintURL       string `json:"loginImprintUrl,omitempty"`
	LoginLogoLightURL     string `json:"loginLogoLightUrl,omitempty"`
	LoginLogoDarkURL      string `json:"loginLogoDarkUrl,omitempty"`
	AppLogoLightURL       string `json:"appLogoLightUrl,omitempty"`
	AppLogoDarkURL        string `json:"appLogoDarkUrl,omitempty"`
	FaviconURL            string `json:"faviconUrl,omitempty"`
}

// validate checks every value again: panel-api checked them when they were
// saved and before sending, and each one ends up in webmail's environment
// and pages.
func (p *webmailBrandingApplyParams) validate() error {
	if !agentwire.ValidWebmailBrandingText(p.BrandText) {
		return fmt.Errorf("brand_text: not a valid name")
	}
	if len(p.Domains) > agentwire.WebmailBrandingMaxDomains {
		return fmt.Errorf("domains: more than %d", agentwire.WebmailBrandingMaxDomains)
	}
	seen := make(map[string]bool, len(p.Domains))
	for i, d := range p.Domains {
		if err := d.Validate(); err != nil {
			return fmt.Errorf("domains[%d].%w", i, err)
		}
		if seen[d.Host] {
			return fmt.Errorf("domains[%d].host: %q twice", i, d.Host)
		}
		seen[d.Host] = true
	}
	return nil
}

func webmailBrandingApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p webmailBrandingApplyParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
		}
	}
	if err := p.validate(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	// Bulwark not installed on this host → nothing to do (idempotent).
	if _, err := os.Stat(webmailPublicBranding); err != nil {
		return okBody{Ok: true}, nil
	}

	appName := strings.TrimSpace(p.BrandText)
	if appName == "" {
		appName = "Jabali Mail"
	}
	company := strings.TrimSpace(p.BrandText)
	if company == "" {
		if h, err := os.Hostname(); err == nil {
			company = h
		} else {
			company = "Jabali Mail"
		}
	}
	body, err := renderWebmailBrandingEnv(appName, company, brandingLogoURL(ctx, "light"), brandingLogoURL(ctx, "dark"), p.Domains)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	changed, err := writeFileIfChanged(webmailBrandingEnvFile, []byte(body), 0o644)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if changed {
		// try-restart: a webmail that isn't running (no mail domain yet)
		// reads the file when it starts, and isn't started by a branding
		// change.
		if out, sErr := runSystemctl(ctx, "try-restart", "jabali-webmail.service"); sErr != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("restart jabali-webmail: %v (%s)", sErr, bytesTrim(out))}
		}
	}
	return okBody{Ok: true}, nil
}

// renderWebmailBrandingEnv is the whole bulwark-branding.env: the
// server-wide keys, and DOMAIN_BRANDING with one entry per domain. A logo
// URL that is "" is left out, so bulwark.env's default applies.
func renderWebmailBrandingEnv(appName, company, lightURL, darkURL string, domains []webmailDomainBranding) (string, error) {
	var b strings.Builder
	b.WriteString("# Webmail branding, written by the panel (webmail.branding.apply).\n")
	b.WriteString("# Do not edit: the panel rewrites it. Server-wide branding is set in\n")
	b.WriteString("# Server Settings, each domain's in its mail settings (GH #2502).\n")
	for _, kv := range [][2]string{
		{"APP_NAME", appName},
		{"LOGIN_COMPANY_NAME", company},
		{"LOGIN_LOGO_LIGHT_URL", lightURL},
		{"LOGIN_LOGO_DARK_URL", darkURL},
		{"APP_LOGO_LIGHT_URL", lightURL},
		{"APP_LOGO_DARK_URL", darkURL},
	} {
		if kv[1] != "" {
			b.WriteString(kv[0] + "=" + systemdEnvQuote(kv[1]) + "\n")
		}
	}
	entries := make([]bulwarkDomainBranding, 0, len(domains))
	for _, d := range domains {
		entries = append(entries, bulwarkDomainBranding{
			Host:                  d.Host,
			AppName:               d.AppName,
			LoginCompanyName:      d.CompanyName,
			LoginWebsiteURL:       d.WebsiteURL,
			LoginPrivacyPolicyURL: d.PrivacyURL,
			LoginImprintURL:       d.ImprintURL,
			LoginLogoLightURL:     d.LogoLightURL,
			LoginLogoDarkURL:      d.LogoDarkURL,
			AppLogoLightURL:       d.LogoLightURL,
			AppLogoDarkURL:        d.LogoDarkURL,
			FaviconURL:            d.FaviconURL,
		})
	}
	var js bytes.Buffer
	enc := json.NewEncoder(&js)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(entries); err != nil {
		return "", fmt.Errorf("encode DOMAIN_BRANDING: %w", err)
	}
	b.WriteString("DOMAIN_BRANDING=" + systemdEnvQuote(strings.TrimSpace(js.String())) + "\n")
	return b.String(), nil
}

// systemdEnvQuote is a value for a systemd EnvironmentFile line: in double
// quotes, with the backslash, the double quote, $ and ` escaped. systemd
// reads what is inside double quotes as is apart from those escapes, so a
// JSON value (full of double quotes) or a name with an apostrophe arrives
// unchanged. Values never hold a newline (validate refuses control
// characters, and JSON encodes them).
func systemdEnvQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "`", "\\`").Replace(s) + `"`
}

// writeFileIfChanged writes data to path through a temporary file, unless
// the file already holds exactly that. It reports whether it wrote.
func writeFileIfChanged(path string, data []byte, mode os.FileMode) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return false, nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return false, fmt.Errorf("chmod %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return false, fmt.Errorf("rename %s: %w", path, err)
	}
	return true, nil
}

// brandingLogoURL returns the Bulwark /branding URL for a variant: the
// operator's uploaded panel logo (copied into Bulwark's public dir) when
// present, else jabali's default mail logo. Empty string only if neither
// exists (shouldn't happen — install ships the defaults).
func brandingLogoURL(ctx context.Context, variant string) string {
	// Operator logo: /var/lib/jabali-panel/branding/{variant}.{svg,png}
	for _, ext := range []string{".svg", ".png"} {
		src := filepath.Join(panelBrandingDir, variant+ext)
		if _, err := os.Stat(src); err == nil {
			dst := filepath.Join(webmailPublicBranding, "jabali-brand-"+variant+ext)
			if err := copyBrandingFile(src, dst); err == nil {
				// Remove a stale copy at the other extension.
				other := ".png"
				if ext == ".png" {
					other = ".svg"
				}
				_ = os.Remove(filepath.Join(webmailPublicBranding, "jabali-brand-"+variant+other))
				return "/branding/jabali-brand-" + variant + ext
			}
		}
	}
	// No operator logo → jabali default (shipped by install_bulwark). Drop
	// any stale operator copy so a cleared logo reverts cleanly.
	_ = os.Remove(filepath.Join(webmailPublicBranding, "jabali-brand-"+variant+".svg"))
	_ = os.Remove(filepath.Join(webmailPublicBranding, "jabali-brand-"+variant+".png"))
	def := filepath.Join(webmailPublicBranding, "jabali-mail-"+variant+".svg")
	if _, err := os.Stat(def); err == nil {
		return "/branding/jabali-mail-" + variant + ".svg"
	}
	return ""
}

func copyBrandingFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		return err
	}
	if u, err := user.Lookup("jabali-webmail"); err == nil {
		uid, _ := strconv.Atoi(u.Uid)
		gid, _ := strconv.Atoi(u.Gid)
		_ = os.Chown(dst, uid, gid)
	}
	return nil
}

func init() {
	Default.Register("webmail.branding.apply", webmailBrandingApplyHandler)
}
