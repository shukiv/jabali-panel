package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A request naming the panel (its host name or main address, bare or with
// :8443) is refused by every domain-side vhost, so it can never reach a
// domain, not even one bound to an address of its own.

const testPanelGuard = `^(?:panel\.example\.com|panel\.example\.com:8443|203\.0\.113\.5|203\.0\.113\.5:8443)$`

const testPanelGuardLine = `if ($http_host ~* "` + testPanelGuard + `") { return 421; }`

func withPanelScope(t *testing.T, host string, addrs ...string) {
	t.Helper()
	prev := vhostPanelScope
	vhostPanelScope = func() (string, []string) { return host, addrs }
	t.Cleanup(func() { vhostPanelScope = prev })
}

// guardedBlocks checks that every server block in out refuses the panel's
// names before its first location, and returns how many blocks there are.
func guardedBlocks(t *testing.T, out string) int {
	t.Helper()
	blocks := strings.Split(out, "server {")[1:]
	for _, b := range blocks {
		guard := strings.Index(b, testPanelGuardLine)
		if guard < 0 {
			t.Fatalf("a server block does not refuse the panel's names:\nserver {%s", b)
		}
		if loc := strings.Index(b, "location "); loc >= 0 && loc < guard {
			t.Fatalf("the guard must come before the first location:\nserver {%s", b)
		}
	}
	return len(blocks)
}

func TestPanelHostGuard(t *testing.T) {
	withPanelScope(t, "panel.example.com", "203.0.113.5")
	for name, tc := range map[string]struct {
		got, want string
	}{
		"a domain":                       {panelHostGuard("shop.example.com", "www.shop.example.com"), testPanelGuard},
		"a vhost naming the panel":       {panelHostGuard("shop.example.com", " Panel.Example.com "), ""},
		"domain with aliases":            {domainPanelHostGuard("shop.example.com", " brand.io www.brand.io"), testPanelGuard},
		"the panel's own domain":         {domainPanelHostGuard("panel.example.com", ""), ""},
		"an alias naming the panel":      {domainPanelHostGuard("shop.example.com", " brand.io panel.example.com"), ""},
		"the panel host's parent domain": {domainPanelHostGuard("example.com", ""), testPanelGuard},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", name, tc.got, tc.want)
		}
	}

	withPanelScope(t, "", "203.0.113.5")
	if got := panelHostGuard("shop.example.com"); got != "" {
		t.Errorf("without the panel's name: got %q, want no guard", got)
	}
}

// Every server block of a domain's vhost refuses the panel's names: the
// real-name :80 and :443 blocks and the preview URL's two.
func TestVhostTemplate_PanelHostGuardInEveryServerBlock(t *testing.T) {
	vd := previewVD()
	vd.ServeHTTPS = true
	vd.PreviewCertPath = "/etc/jabali/ssl/preview/fullchain.pem"
	vd.PreviewKeyPath = "/etc/jabali/ssl/preview/privkey.pem"
	vd.PanelHostGuard = testPanelGuard
	if n := guardedBlocks(t, mustRenderVhost(t, vd)); n != 4 {
		t.Fatalf("server blocks = %d, want 4", n)
	}

	vd.PanelHostGuard = ""
	if out := mustRenderVhost(t, vd); strings.Contains(out, "return 421") {
		t.Fatalf("no guard asked for, one rendered:\n%s", out)
	}
}

// writeVhost puts the domain's guard into the vhost it renders.
func TestWriteVhost_SetsThePanelHostGuard(t *testing.T) {
	src, err := os.ReadFile("domain_create.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "PanelHostGuard:             domainPanelHostGuard(domain, aliasServerNames),") {
		t.Fatal("writeVhost does not set PanelHostGuard from domainPanelHostGuard")
	}
}

// A tenant's mail vhost refuses the panel's names in both blocks; the
// panel-primary one, which is the panel's own, does not.
func TestWebmailVhostApply_RefusesThePanelsNames(t *testing.T) {
	withPanelScope(t, "panel.example.com", "203.0.113.5")
	avail, _ := wireMailVhostPaths(t)
	wireNginxReload(t)

	apply := func(p webmailVhostApplyParams) string {
		t.Helper()
		raw, _ := json.Marshal(p)
		if _, err := webmailVhostApplyHandler(context.Background(), raw); err != nil {
			t.Fatalf("apply %s: %v", p.DomainName, err)
		}
		b, err := os.ReadFile(filepath.Join(avail, p.DomainName+"-mail.conf"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	tenant := apply(webmailVhostApplyParams{
		DomainName:  "shop.example.com",
		SSLCertPath: "/etc/letsencrypt/live/shop.example.com/fullchain.pem",
		SSLKeyPath:  "/etc/letsencrypt/live/shop.example.com/privkey.pem",
		ListenIPv4:  "198.51.100.9",
	})
	if n := guardedBlocks(t, tenant); n != 2 {
		t.Fatalf("server blocks = %d, want 2", n)
	}
	primary := apply(webmailVhostApplyParams{
		DomainName:     "panel.example.com",
		SSLCertPath:    "/etc/jabali/tls/panel.crt",
		SSLKeyPath:     "/etc/jabali/tls/panel.key",
		IsPanelPrimary: true,
	})
	if strings.Contains(primary, "return 421") {
		t.Fatalf("the panel-primary mail vhost refuses the panel's names:\n%s", primary)
	}
}

// A docker app's vhost refuses the panel's names in both blocks.
func TestDockerAppVhostApply_RefusesThePanelsNames(t *testing.T) {
	withPanelScope(t, "panel.example.com", "203.0.113.5")
	av, _ := withDockerAppVhostTestDirs(t)
	raw, _ := json.Marshal(dockerAppVhostApplyParams{
		DomainName:  "forum.example.com",
		Upstream:    "http://127.0.0.1:10000",
		SSLCertPath: "/etc/ssl/x/fullchain.pem",
		SSLKeyPath:  "/etc/ssl/x/privkey.pem",
		ListenIPv4:  "198.51.100.9",
	})
	if _, err := dockerAppVhostApplyHandler(context.Background(), raw); err != nil {
		t.Fatalf("apply: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(av, "forum.example.com-dockerapp.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if n := guardedBlocks(t, string(b)); n != 2 {
		t.Fatalf("server blocks = %d, want 2", n)
	}
}
