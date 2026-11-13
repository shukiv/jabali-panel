package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// systemdUnquote reads a double-quoted EnvironmentFile value the way systemd
// does: a backslash before ", \, ` or $ gives that character, any other
// backslash stays.
func systemdUnquote(t *testing.T, v string) string {
	t.Helper()
	if len(v) < 2 || v[0] != '"' || v[len(v)-1] != '"' {
		t.Fatalf("value %q is not double-quoted", v)
	}
	v = v[1 : len(v)-1]
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '\\' && i+1 < len(v) && strings.ContainsRune("\"\\`$", rune(v[i+1])) {
			i++
		} else if v[i] == '"' {
			t.Fatalf("unescaped double quote in %q", v)
		}
		b.WriteByte(v[i])
	}
	return b.String()
}

func envValues(t *testing.T, body string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("line %q is not KEY=value", line)
		}
		out[k] = systemdUnquote(t, v)
	}
	return out
}

const testPicture = "/jabali-webmail-branding/01HZZZZZZZZZZZZZZZZZZZZZZZ/logo-light.png?v=0123456789ab"

func testBrandingDomain() webmailDomainBranding {
	return webmailDomainBranding{
		Host:         "mail.customer-a.com",
		AppName:      `Bob's "Mail" $HOME \n`,
		CompanyName:  "חברה בע\"מ",
		WebsiteURL:   "https://customer-a.com/?a=1&b=$x",
		LogoLightURL: testPicture,
		LogoDarkURL:  testPicture,
	}
}

// What bulwark-branding.env says is what Bulwark reads: every value survives
// systemd's quoting, DOMAIN_BRANDING is Bulwark's JSON with its own names.
func TestRenderWebmailBrandingEnv(t *testing.T) {
	body, err := renderWebmailBrandingEnv(`Acme "Hosting"`, "Acme's", "/branding/jabali-mail-light.svg", "", []webmailDomainBranding{testBrandingDomain()})
	if err != nil {
		t.Fatal(err)
	}
	v := envValues(t, body)
	if v["APP_NAME"] != `Acme "Hosting"` || v["LOGIN_COMPANY_NAME"] != "Acme's" {
		t.Fatalf("server-wide = %q / %q", v["APP_NAME"], v["LOGIN_COMPANY_NAME"])
	}
	if v["APP_LOGO_LIGHT_URL"] != "/branding/jabali-mail-light.svg" {
		t.Fatalf("APP_LOGO_LIGHT_URL = %q", v["APP_LOGO_LIGHT_URL"])
	}
	if _, ok := v["APP_LOGO_DARK_URL"]; ok {
		t.Fatal("an empty logo URL was written, which would hide bulwark.env's default")
	}
	var entries []map[string]string
	if err := json.Unmarshal([]byte(v["DOMAIN_BRANDING"]), &entries); err != nil {
		t.Fatalf("DOMAIN_BRANDING %q: %v", v["DOMAIN_BRANDING"], err)
	}
	d := testBrandingDomain()
	want := map[string]string{
		"host": d.Host, "appName": d.AppName, "loginCompanyName": d.CompanyName, "loginWebsiteUrl": d.WebsiteURL,
		"loginLogoLightUrl": testPicture, "loginLogoDarkUrl": testPicture, "appLogoLightUrl": testPicture, "appLogoDarkUrl": testPicture,
	}
	if len(entries) != 1 || len(entries[0]) != len(want) {
		t.Fatalf("entries = %v, want %v", entries, want)
	}
	for k, w := range want {
		if entries[0][k] != w {
			t.Errorf("%s = %q, want %q", k, entries[0][k], w)
		}
	}

	empty, _ := renderWebmailBrandingEnv("Jabali Mail", "host", "", "", nil)
	if envValues(t, empty)["DOMAIN_BRANDING"] != "[]" {
		t.Fatalf("no domains: DOMAIN_BRANDING = %q, want []", envValues(t, empty)["DOMAIN_BRANDING"])
	}
}

func TestWebmailBrandingValidate(t *testing.T) {
	ok := webmailBrandingApplyParams{BrandText: "Acme", Domains: []webmailDomainBranding{testBrandingDomain()}}
	if err := ok.validate(); err != nil {
		t.Fatalf("valid params refused: %v", err)
	}
	for name, change := range map[string]func(d *webmailDomainBranding){
		"host not mail.":       func(d *webmailDomainBranding) { d.Host = "customer-a.com" },
		"host upper case":      func(d *webmailDomainBranding) { d.Host = "mail.Customer-A.com" },
		"javascript link":      func(d *webmailDomainBranding) { d.PrivacyURL = "javascript:alert(1)" },
		"picture elsewhere":    func(d *webmailDomainBranding) { d.FaviconURL = "https://evil.example/x.png" },
		"picture path escapes": func(d *webmailDomainBranding) { d.LogoDarkURL = "/jabali-webmail-branding/../../etc/passwd" },
		"newline in a name":    func(d *webmailDomainBranding) { d.AppName = "A\nAPP_NAME=x" },
	} {
		d := testBrandingDomain()
		change(&d)
		p := webmailBrandingApplyParams{Domains: []webmailDomainBranding{d}}
		if err := p.validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	dup := webmailBrandingApplyParams{Domains: []webmailDomainBranding{testBrandingDomain(), testBrandingDomain()}}
	if err := dup.validate(); err == nil {
		t.Error("the same host twice was accepted")
	}
}

// The file is written and webmail restarted (try-restart: a webmail that
// isn't running isn't started) only when the branding changed.
func TestWebmailBrandingApply_RestartsOnlyOnChange(t *testing.T) {
	tmp := t.TempDir()
	origPublic, origPanel, origEnv, origCtl := webmailPublicBranding, panelBrandingDir, webmailBrandingEnvFile, runSystemctl
	t.Cleanup(func() {
		webmailPublicBranding, panelBrandingDir, webmailBrandingEnvFile, runSystemctl = origPublic, origPanel, origEnv, origCtl
	})
	webmailPublicBranding = filepath.Join(tmp, "public")
	panelBrandingDir = filepath.Join(tmp, "panel")
	webmailBrandingEnvFile = filepath.Join(tmp, "bulwark-branding.env")
	var restarts [][]string
	runSystemctl = func(_ context.Context, args ...string) ([]byte, error) {
		restarts = append(restarts, args)
		return nil, nil
	}
	apply := func(p webmailBrandingApplyParams) {
		t.Helper()
		raw, _ := json.Marshal(p)
		if _, err := webmailBrandingApplyHandler(context.Background(), raw); err != nil {
			t.Fatal(err)
		}
	}
	p := webmailBrandingApplyParams{BrandText: "Acme", Domains: []webmailDomainBranding{testBrandingDomain()}}

	apply(p) // webmail not installed: nothing to do
	if _, err := os.Stat(webmailBrandingEnvFile); !os.IsNotExist(err) || len(restarts) != 0 {
		t.Fatalf("wrote or restarted with webmail not installed (%v, %d restarts)", err, len(restarts))
	}

	if err := os.MkdirAll(webmailPublicBranding, 0o755); err != nil {
		t.Fatal(err)
	}
	apply(p)
	apply(p)
	if len(restarts) != 1 || strings.Join(restarts[0], " ") != "try-restart jabali-webmail.service" {
		t.Fatalf("restarts = %v, want one try-restart", restarts)
	}
	if fi, err := os.Stat(webmailBrandingEnvFile); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("env file = %v, %v", fi, err)
	}

	p.Domains = nil
	apply(p)
	if len(restarts) != 2 {
		t.Fatalf("removing a domain's branding did not restart webmail (%d)", len(restarts))
	}

	raw, _ := json.Marshal(webmailBrandingApplyParams{Domains: []webmailDomainBranding{{Host: "evil.com"}}})
	if _, err := webmailBrandingApplyHandler(context.Background(), raw); err == nil {
		t.Fatal("invalid params were applied")
	}
}
