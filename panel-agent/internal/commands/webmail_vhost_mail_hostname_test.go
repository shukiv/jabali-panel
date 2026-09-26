package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// JAB-390: the shared panel mail hostname may be any FQDN, not only
// mail.<panel-hostname>. Two parts of every mail vhost depend on it:
//   - the sub_filter that rewrites Bulwark's JMAP URL (bulwark.env
//     JMAP_SERVER_URL = https://<panel mail hostname>) to the requested $host,
//     so tenant webmail stays same-origin;
//   - on the panel-primary row, server_name must also answer the custom name,
//     or https://<custom name>/ falls to the default vhost.
// The panel now sends panel_mail_hostname and, for the panel-primary row,
// extra_server_names. Both reach nginx config verbatim, so the agent refuses
// anything that is not a plain DNS name.

func applyMailVhost(t *testing.T, p webmailVhostApplyParams) (string, error) {
	t.Helper()
	avail, _ := wireMailVhostPaths(t)
	wireNginxReload(t)
	params, _ := json.Marshal(p)
	if _, err := webmailVhostApplyHandler(context.Background(), params); err != nil {
		if _, statErr := os.Stat(filepath.Join(avail, p.DomainName+"-mail.conf")); statErr == nil {
			t.Errorf("a refused apply must not write the vhost")
		}
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(avail, p.DomainName+"-mail.conf"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b), nil
}

func mailVhostParams(domain string) webmailVhostApplyParams {
	return webmailVhostApplyParams{
		DomainName:  domain,
		SSLCertPath: "/etc/letsencrypt/live/" + domain + "/fullchain.pem",
		SSLKeyPath:  "/etc/letsencrypt/live/" + domain + "/privkey.pem",
	}
}

func TestWebmailVhostApply_PanelMailHostnameDrivesSubFilter(t *testing.T) {
	p := mailVhostParams("example.com")
	p.PanelHostname = "mx.jabali-panel.com"
	p.PanelMailHostname = "MX.Example.NET"
	s, err := applyMailVhost(t, p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(s, `sub_filter "mx.example.net" $host;`) {
		t.Errorf("sub_filter must rewrite the (normalized) panel mail hostname:\n%s", s)
	}
	if strings.Contains(s, `sub_filter "mail.mx.jabali-panel.com"`) {
		t.Errorf("sub_filter must not rewrite the derived name when a panel mail hostname is given:\n%s", s)
	}
}

// A panel that sends the derived name renders byte-for-byte what an older
// panel (panel_hostname only) rendered — no vhost changes on upgrade.
func TestWebmailVhostApply_DerivedPanelMailHostnameIsByteIdentical(t *testing.T) {
	old := mailVhostParams("example.com")
	old.PanelHostname = "mx.jabali-panel.com"
	want, err := applyMailVhost(t, old)
	if err != nil {
		t.Fatalf("apply (panel_hostname only): %v", err)
	}
	cur := old
	cur.PanelMailHostname = "mail.mx.jabali-panel.com"
	got, err := applyMailVhost(t, cur)
	if err != nil {
		t.Fatalf("apply (with panel_mail_hostname): %v", err)
	}
	if got != want {
		t.Errorf("render changed for the derived panel mail hostname\n--- want\n%s\n--- got\n%s", want, got)
	}
}

func TestWebmailVhostApply_PanelPrimaryExtraServerNames(t *testing.T) {
	p := mailVhostParams("mx.jabali-panel.com")
	p.PanelHostname = "mx.jabali-panel.com"
	p.PanelMailHostname = "mx.example.net"
	p.IsPanelPrimary = true
	p.ExtraServerNames = []string{"MX.example.net", "mail.mx.jabali-panel.com"}
	s, err := applyMailVhost(t, p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := "  server_name mail.mx.jabali-panel.com autoconfig.mx.jabali-panel.com autodiscover.mx.jabali-panel.com mta-sts.mx.jabali-panel.com mx.example.net;\n"
	if n := strings.Count(s, want); n != 2 {
		t.Errorf("both server blocks (:443 and :80) must answer the extra name exactly once, with duplicates of the derived names dropped; found %d of %q in:\n%s", n, want, s)
	}
}

func TestWebmailVhostApply_ExtraServerNamesOnlyForPanelPrimary(t *testing.T) {
	p := mailVhostParams("tenant.com")
	p.PanelHostname = "mx.jabali-panel.com"
	p.ExtraServerNames = []string{"mx.example.net"}
	_, err := applyMailVhost(t, p)
	assertInvalidArgument(t, err, "a tenant mail vhost must never answer extra names")
}

func TestWebmailVhostApply_RefusesUnsafeMailHostnames(t *testing.T) {
	bad := []string{
		"mx.example.net; return 301 https://evil",
		`mx.example.net" $host; sub_filter "x`,
		"mx.example.net\nserver {",
		"mx.example.net#",
		"https://mx.example.net/",
		"localhost",
		"-mx.example.net",
		"mx..example.net",
		"*.example.net",
		strings.Repeat("a", 64) + ".net",                       // label over 63
		strings.Repeat(strings.Repeat("a", 60)+".", 5) + "net", // name over 253
	}
	for _, h := range bad {
		p := mailVhostParams("example.com")
		p.PanelHostname = "mx.jabali-panel.com"
		p.PanelMailHostname = h
		_, err := applyMailVhost(t, p)
		assertInvalidArgument(t, err, "panel_mail_hostname "+h)

		p = mailVhostParams("mx.jabali-panel.com")
		p.IsPanelPrimary = true
		p.ExtraServerNames = []string{"mx.example.net", h}
		_, err = applyMailVhost(t, p)
		assertInvalidArgument(t, err, "extra_server_names "+h)
	}
}

func assertInvalidArgument(t *testing.T, err error, what string) {
	t.Helper()
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
		t.Errorf("%s: want CodeInvalidArgument, got %v", what, err)
	}
}
