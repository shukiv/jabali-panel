package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reloadCapture counts how many times the nginx reload shim was invoked.
// Tests swap it in for defaultNginxTestAndReload so the agent code paths
// don't need a real nginx binary.
type reloadCapture struct {
	calls int
	fail  error
}

func wireNginxReload(t *testing.T) *reloadCapture {
	t.Helper()
	orig := nginxTestAndReload
	cap := &reloadCapture{}
	nginxTestAndReload = func(_ context.Context) error {
		cap.calls++
		return cap.fail
	}
	t.Cleanup(func() { nginxTestAndReload = orig })
	return cap
}

// wireMailVhostPaths points the sites-available / sites-enabled dirs
// at test temp dirs so the agent writes don't touch the real nginx
// tree. Returns (available, enabled).
func wireMailVhostPaths(t *testing.T) (string, string) {
	t.Helper()
	avail := filepath.Join(t.TempDir(), "sites-available")
	enabl := filepath.Join(t.TempDir(), "sites-enabled")
	if err := os.MkdirAll(avail, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(enabl, 0755); err != nil {
		t.Fatal(err)
	}
	origA, origE := mailVhostSitesAvailable, mailVhostSitesEnabled
	mailVhostSitesAvailable = avail
	mailVhostSitesEnabled = enabl
	t.Cleanup(func() {
		mailVhostSitesAvailable = origA
		mailVhostSitesEnabled = origE
	})
	return avail, enabl
}

func TestWebmailVhostApply_WritesAndReloads(t *testing.T) {
	avail, enabl := wireMailVhostPaths(t)
	cap := wireNginxReload(t)

	params, _ := json.Marshal(webmailVhostApplyParams{
		DomainName:  "example.com",
		SSLCertPath: "/etc/letsencrypt/live/example.com/fullchain.pem",
		SSLKeyPath:  "/etc/letsencrypt/live/example.com/privkey.pem",
	})
	got, err := webmailVhostApplyHandler(context.Background(), params)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	resp := got.(webmailVhostResponse)
	if !resp.Ok || !resp.Changed {
		t.Errorf("expected Ok=true, Changed=true, got %+v", resp)
	}
	// Vhost file exists with expected contents.
	confPath := filepath.Join(avail, "example.com-mail.conf")
	b, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("read vhost: %v", err)
	}
	// GH#132: server_name must cover every mail cert SAN hostname so
	// certbot's HTTP-01 challenge for autodiscover/mta-sts resolves to
	// this vhost (and its ACME webroot) instead of falling to nginx
	// default and 404ing. mail + autoconfig + autodiscover + mta-sts.
	if !strings.Contains(string(b), "server_name mail.example.com autoconfig.example.com autodiscover.example.com mta-sts.example.com;") {
		t.Errorf("vhost missing server_name: %s", string(b))
	}
	if !strings.Contains(string(b), "/etc/letsencrypt/live/example.com/fullchain.pem") {
		t.Errorf("vhost missing cert path substitution: %s", string(b))
	}
	// M25 Step 5: Bulwark moved off TCP 127.0.0.1:3000 onto a Unix
	// socket fronted by the named upstream `jabali_bulwark`.
	if !strings.Contains(string(b), "proxy_pass http://jabali_bulwark/") {
		t.Error("vhost missing Bulwark proxy_pass to jabali_bulwark upstream")
	}
	if !strings.Contains(string(b), "proxy_pass http://127.0.0.1:8446") {
		t.Error("vhost missing Stalwart proxy_pass")
	}
	// Enabled symlink exists and points at sites-available.
	target, err := os.Readlink(filepath.Join(enabl, "example.com-mail.conf"))
	if err != nil {
		t.Fatalf("readlink enabled: %v", err)
	}
	if target != confPath {
		t.Errorf("symlink target = %q, want %q", target, confPath)
	}
	if cap.calls != 1 {
		t.Errorf("nginx reload should fire exactly once, got %d", cap.calls)
	}
}

// TestWebmailVhostApply_EmitsCalDAVCardDAVLocations pins the GH #1039
// follow-up: the mail vhost must route the CalDAV/CardDAV autodiscovery
// endpoints + collections to Stalwart (8446) so Thunderbird / Apple Mail
// can auto-mount calendars and address books. It must NOT expose the
// WebDAV file-storage (/dav/file) or principals (/dav/pal) namespaces.
func TestWebmailVhostApply_EmitsCalDAVCardDAVLocations(t *testing.T) {
	avail, _ := wireMailVhostPaths(t)
	wireNginxReload(t)

	params, _ := json.Marshal(webmailVhostApplyParams{
		DomainName:  "example.com",
		SSLCertPath: "/etc/letsencrypt/live/example.com/fullchain.pem",
		SSLKeyPath:  "/etc/letsencrypt/live/example.com/privkey.pem",
	})
	if _, err := webmailVhostApplyHandler(context.Background(), params); err != nil {
		t.Fatalf("apply: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(avail, "example.com-mail.conf"))
	if err != nil {
		t.Fatalf("read vhost: %v", err)
	}
	s := string(b)

	for _, want := range []string{
		"location = /.well-known/caldav {",
		"location = /.well-known/carddav {",
		"location /dav/cal {",
		"location /dav/card {",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("vhost missing DAV location %q:\n%s", want, s)
		}
	}
	// Security: the file-storage and principals namespaces must NOT be
	// proxied on the mail vhost (match the location block, not the
	// explanatory comment that names the paths).
	for _, forbidden := range []string{"location /dav/file", "location /dav/pal"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("vhost must not expose %q on the mail host:\n%s", forbidden, s)
		}
	}
}

// TestWebmailVhostApply_IdempotentSameContent — second apply with
// identical params must not write the file again AND must not reload
// nginx. This is the reconciler's steady-state case, called every tick.
func TestWebmailVhostApply_IdempotentSameContent(t *testing.T) {
	wireMailVhostPaths(t)
	cap := wireNginxReload(t)

	params, _ := json.Marshal(webmailVhostApplyParams{
		DomainName:  "example.com",
		SSLCertPath: "/ssl/cert", SSLKeyPath: "/ssl/key",
	})
	if _, err := webmailVhostApplyHandler(context.Background(), params); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if _, err := webmailVhostApplyHandler(context.Background(), params); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if cap.calls != 1 {
		t.Errorf("nginx reload should fire only on the first apply, got %d calls", cap.calls)
	}
}

// TestWebmailVhostApply_ReLinksDanglingSymlink — if a prior vhost_remove
// blew away the symlink but the file is still there, the next apply
// restores the symlink and reloads.
func TestWebmailVhostApply_ReLinksDanglingSymlink(t *testing.T) {
	_, enabl := wireMailVhostPaths(t)
	cap := wireNginxReload(t)

	params, _ := json.Marshal(webmailVhostApplyParams{
		DomainName:  "example.com",
		SSLCertPath: "/ssl/cert", SSLKeyPath: "/ssl/key",
	})
	if _, err := webmailVhostApplyHandler(context.Background(), params); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	// Remove the symlink only.
	if err := os.Remove(filepath.Join(enabl, "example.com-mail.conf")); err != nil {
		t.Fatal(err)
	}
	got, err := webmailVhostApplyHandler(context.Background(), params)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if !got.(webmailVhostResponse).Changed {
		t.Error("second apply should report Changed=true to signal the symlink repair")
	}
	if cap.calls != 2 {
		t.Errorf("nginx reload should fire twice (first write + symlink repair), got %d", cap.calls)
	}
	if _, err := os.Lstat(filepath.Join(enabl, "example.com-mail.conf")); err != nil {
		t.Errorf("symlink should have been re-created: %v", err)
	}
}

// TestWebmailVhostApply_RollbackOnNginxFailure — if nginx -t or reload
// fails, the vhost file AND the symlink must be removed so subsequent
// reloads (by other code paths) don't trip over the bad config.
func TestWebmailVhostApply_RollbackOnNginxFailure(t *testing.T) {
	avail, enabl := wireMailVhostPaths(t)
	cap := wireNginxReload(t)
	cap.fail = errors.New("nginx -t: syntax error")

	params, _ := json.Marshal(webmailVhostApplyParams{
		DomainName:  "example.com",
		SSLCertPath: "/ssl/cert", SSLKeyPath: "/ssl/key",
	})
	_, err := webmailVhostApplyHandler(context.Background(), params)
	if err == nil {
		t.Fatal("expected apply to fail when nginx reload fails")
	}
	if _, err := os.Stat(filepath.Join(avail, "example.com-mail.conf")); !os.IsNotExist(err) {
		t.Error("sites-available file must be removed on rollback")
	}
	if _, err := os.Lstat(filepath.Join(enabl, "example.com-mail.conf")); !os.IsNotExist(err) {
		t.Error("sites-enabled symlink must be removed on rollback")
	}
}

// TestWebmailVhostApply_RejectsShellMetaInDomainName — the domain name
// ends up in `server_name` which nginx parses, but it also forms the
// file path. Must go through validateDomainNameForShell to block
// traversal attempts.
func TestWebmailVhostApply_RejectsShellMetaInDomainName(t *testing.T) {
	wireMailVhostPaths(t)
	wireNginxReload(t)

	cases := []string{
		"example.com;rm",
		"../etc/passwd",
		"foo$(bar)",
		"",
		"foo bar.com",
	}
	for _, name := range cases {
		params, _ := json.Marshal(webmailVhostApplyParams{
			DomainName:  name,
			SSLCertPath: "/ssl/cert", SSLKeyPath: "/ssl/key",
		})
		if _, err := webmailVhostApplyHandler(context.Background(), params); err == nil {
			t.Errorf("expected reject for domain %q, got nil", name)
		}
	}
}

// TestWebmailVhostApply_RejectsMissingSSLPaths — agent must not write
// a vhost that would crash nginx with "ssl_certificate path is empty".
func TestWebmailVhostApply_RejectsMissingSSLPaths(t *testing.T) {
	wireMailVhostPaths(t)
	wireNginxReload(t)

	params, _ := json.Marshal(webmailVhostApplyParams{
		DomainName:  "example.com",
		SSLCertPath: "", SSLKeyPath: "/ssl/key",
	})
	if _, err := webmailVhostApplyHandler(context.Background(), params); err == nil {
		t.Error("expected reject when ssl_cert_path empty")
	}
}

// TestWebmailVhostRemove_RemovesBothFilesAndReloads — happy path.
func TestWebmailVhostRemove_RemovesBothFilesAndReloads(t *testing.T) {
	avail, enabl := wireMailVhostPaths(t)
	cap := wireNginxReload(t)

	applyParams, _ := json.Marshal(webmailVhostApplyParams{
		DomainName: "example.com", SSLCertPath: "/ssl/cert", SSLKeyPath: "/ssl/key",
	})
	if _, err := webmailVhostApplyHandler(context.Background(), applyParams); err != nil {
		t.Fatalf("setup apply: %v", err)
	}
	cap.calls = 0

	removeParams, _ := json.Marshal(webmailVhostRemoveParams{DomainName: "example.com"})
	got, err := webmailVhostRemoveHandler(context.Background(), removeParams)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !got.(webmailVhostResponse).Changed {
		t.Error("remove should report Changed=true when files existed")
	}
	if _, err := os.Stat(filepath.Join(avail, "example.com-mail.conf")); !os.IsNotExist(err) {
		t.Error("sites-available file must be gone")
	}
	if _, err := os.Lstat(filepath.Join(enabl, "example.com-mail.conf")); !os.IsNotExist(err) {
		t.Error("sites-enabled symlink must be gone")
	}
	if cap.calls != 1 {
		t.Errorf("nginx reload must fire once on real remove, got %d", cap.calls)
	}
}

// TestWebmailVhostRemove_NoopWhenAbsent — calling remove on a never-
// applied domain returns ok+Changed=false without reloading nginx.
// This matters because the reconciler calls remove on every disabled
// domain every tick; cheap no-op is required.
func TestWebmailVhostRemove_NoopWhenAbsent(t *testing.T) {
	wireMailVhostPaths(t)
	cap := wireNginxReload(t)

	removeParams, _ := json.Marshal(webmailVhostRemoveParams{DomainName: "unused.example.com"})
	got, err := webmailVhostRemoveHandler(context.Background(), removeParams)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got.(webmailVhostResponse).Changed {
		t.Error("remove on absent files should report Changed=false")
	}
	if cap.calls != 0 {
		t.Errorf("nginx reload must NOT fire for no-op remove, got %d", cap.calls)
	}
}

func TestWebmailVhostApply_PanelHostnameEmitsSubFilter(t *testing.T) {
	avail, _ := wireMailVhostPaths(t)
	wireNginxReload(t)

	params, _ := json.Marshal(webmailVhostApplyParams{
		DomainName:    "example.com",
		SSLCertPath:   "/etc/letsencrypt/live/example.com/fullchain.pem",
		SSLKeyPath:    "/etc/letsencrypt/live/example.com/privkey.pem",
		PanelHostname: "mx.jabali-panel.com",
	})
	if _, err := webmailVhostApplyHandler(context.Background(), params); err != nil {
		t.Fatalf("apply: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(avail, "example.com-mail.conf"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`sub_filter "mail.mx.jabali-panel.com" $host;`,
		`sub_filter_types application/json;`,
		`sub_filter_once off;`,
		`proxy_set_header Accept-Encoding "";`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("vhost missing %q\nrendered:\n%s", want, s)
		}
	}
}

func TestWebmailVhostApply_EmptyPanelHostnameOmitsSubFilter(t *testing.T) {
	avail, _ := wireMailVhostPaths(t)
	wireNginxReload(t)

	params, _ := json.Marshal(webmailVhostApplyParams{
		DomainName:  "example.com",
		SSLCertPath: "/etc/letsencrypt/live/example.com/fullchain.pem",
		SSLKeyPath:  "/etc/letsencrypt/live/example.com/privkey.pem",
	})
	if _, err := webmailVhostApplyHandler(context.Background(), params); err != nil {
		t.Fatalf("apply: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(avail, "example.com-mail.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "sub_filter ") {
		t.Errorf("sub_filter must NOT be emitted when PanelHostname is empty:\n%s", string(b))
	}
}

// TestWebmailVhostApply_EmptyDocRootSkipsACMELocation verifies the
// panel-primary (mail-only, no docroot) case. With DocRoot="" the
// template must NOT render `root ;` — that's an invalid nginx
// directive that fails nginx -t. Panel-primary mail certs renew via
// the panel-cert reconciler (ssl.panel.issue), not per-domain ACME,
// so dropping the ACME location entirely is safe. Regression test
// for the 60s reconciler error loop on .14 (2026-06-01).
func TestWebmailVhostApply_EmptyDocRootServesPanelACME(t *testing.T) {
	avail, _ := wireMailVhostPaths(t)
	wireNginxReload(t)

	params, _ := json.Marshal(webmailVhostApplyParams{
		DomainName:  "jabali-panel.local",
		SSLCertPath: "/etc/jabali/tls/panel.crt",
		SSLKeyPath:  "/etc/jabali/tls/panel.key",
		DocRoot:     "", // panel-primary: no docroot
	})
	if _, err := webmailVhostApplyHandler(context.Background(), params); err != nil {
		t.Fatalf("apply: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(avail, "jabali-panel.local-mail.conf"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	// MUST NOT contain the broken `root ;` directive (no docroot fallback).
	if strings.Contains(s, "root ;") || strings.Contains(s, "root  ;") {
		t.Errorf("vhost emitted invalid `root ;` directive:\n%s", s)
	}
	// GH #268: panel-primary mail vhost MUST serve the panel mail cert's
	// HTTP-01 webroot on :80, or ssl.panel.issue kind=mail 404s on the
	// mail.<panel> challenge and the cert never validates.
	if !strings.Contains(s, "/.well-known/acme-challenge/") {
		t.Errorf("panel-primary vhost must serve ACME challenges:\n%s", s)
	}
	if !strings.Contains(s, "root /var/www/jabali-panel-acme;") {
		t.Errorf("panel-primary ACME location must serve /var/www/jabali-panel-acme:\n%s", s)
	}
	// Must NOT emit the docroot fallback location (there is no docroot).
	if strings.Contains(s, "location @acme_docroot {") {
		t.Errorf("panel-primary vhost must not emit @acme_docroot location:\n%s", s)
	}
	// MUST still emit the :80 → :443 redirect for non-ACME plain-HTTP.
	if !strings.Contains(s, "return 301 https://$host$request_uri;") {
		t.Errorf("vhost missing :80 → :443 redirect:\n%s", s)
	}
}

// TestWebmailVhostApply_DocRootServesBothACMEWebroots is the GH #132
// regression guard. When a tenant domain has a docroot, the :80 ACME
// location must serve BOTH the shared panel webroot (/var/www/jabali-
// acme, where the M6.6 mail cert drops its token) AND the docroot
// (where the regular per-domain cert drops its token), via a
// @acme_docroot fallback. Before the fix the location only served the
// docroot, so the M6.6 mail cert HTTP-01 challenge 404'd and the cert
// never issued — Stalwart stayed on its self-signed default on :465.
func TestWebmailVhostApply_DocRootServesBothACMEWebroots(t *testing.T) {
	avail, _ := wireMailVhostPaths(t)
	wireNginxReload(t)

	params, _ := json.Marshal(webmailVhostApplyParams{
		DomainName:  "example.com",
		SSLCertPath: "/etc/letsencrypt/live/example.com/fullchain.pem",
		SSLKeyPath:  "/etc/letsencrypt/live/example.com/privkey.pem",
		DocRoot:     "/home/alice/domains/example.com/public_html",
	})
	if _, err := webmailVhostApplyHandler(context.Background(), params); err != nil {
		t.Fatalf("apply: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(avail, "example.com-mail.conf"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	// Primary ACME root is the shared panel webroot (M6.6 + M32).
	if !strings.Contains(s, "root /var/www/jabali-acme;") {
		t.Errorf("vhost ACME location must root at the shared panel webroot:\n%s", s)
	}
	// Fallback to the tenant docroot via a named location.
	if !strings.Contains(s, "try_files $uri @acme_docroot;") {
		t.Errorf("vhost ACME location must fall back to @acme_docroot:\n%s", s)
	}
	if !strings.Contains(s, "location @acme_docroot {") {
		t.Errorf("vhost must define the @acme_docroot fallback location:\n%s", s)
	}
	if !strings.Contains(s, "root /home/alice/domains/example.com/public_html;") {
		t.Errorf("@acme_docroot must root at the tenant docroot:\n%s", s)
	}
}
