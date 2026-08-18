package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ---------- nginx-config-invalid ----------
//
// Scar (incident 2026-05-15, mx.jabali-panel.com): commit 27ed1030 briefly
// emitted the `http2 on;` directive (valid only on nginx >= 1.25.1). It was
// reverted in d8d42cd1, but hosts that took a build in that window have a
// rendered jabali-default.conf / jabali-panel.conf containing `http2 on;`.
// `jabali update` pulls the corrected templates but NEVER re-renders these
// two server-scope files (the install.sh writers are always-overwrite but
// update does not re-run them) — so `nginx -t` stays broken forever, every
// reload is rejected, and nginx serves a stale config (self-signed default
// cert, :80 returns nothing). This detector self-heals that exact state.
//
// ADR-0077.

var managedNginxConfs = []string{
	"/etc/nginx/sites-available/jabali-default.conf",
	"/etc/nginx/sites-available/jabali-panel.conf",
}

var nginxVerRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// nginxVersionLT1251 reports whether the nginx version string denotes a
// release older than 1.25.1 — i.e. one where the standalone `http2 on;`
// directive is an "unknown directive" error and HTTP/2 must instead be a
// `listen ... http2` parameter. Unparseable input returns false
// (conservative: never auto-rewrite a config we cannot version-gate).
func nginxVersionLT1251(v string) bool {
	m := nginxVerRe.FindStringSubmatch(v)
	if m == nil {
		return false
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	pat, _ := strconv.Atoi(m[3])
	if maj != 1 {
		return maj < 1
	}
	if min != 25 {
		return min < 25
	}
	return pat < 1
}

// isSSLListen reports whether a trimmed line is a `listen ... ssl ...;`
// directive (HTTP/2 is only valid on a TLS listener — plain :80 listens
// must never gain http2).
func isSSLListen(trimmed string) bool {
	if !strings.HasPrefix(trimmed, "listen ") || !strings.HasSuffix(trimmed, ";") {
		return false
	}
	ssl := false
	for _, f := range strings.Fields(strings.TrimSuffix(trimmed, ";")) {
		if f == "ssl" {
			ssl = true
		}
	}
	return ssl
}

// foldHTTP2 rewrites an nginx config so HTTP/2 is expressed via the
// portable `listen ... ssl http2;` parameter instead of the >=1.25.1-only
// standalone `http2 on;` directive: every standalone `http2 on;` line is
// dropped, and every ssl listen line that lacks an http2 token gains one.
// It is idempotent (already-correct input returns changed=false) and
// preserves indentation, ordering, and the trailing newline.
func foldHTTP2(in string) (string, bool) {
	lines := strings.Split(in, "\n")
	out := make([]string, 0, len(lines))
	changed := false
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "http2 on;" {
			changed = true
			continue // drop the unsupported standalone directive
		}
		if isSSLListen(trimmed) && !strings.Contains(trimmed, "http2") {
			indent := ln[:len(ln)-len(strings.TrimLeft(ln, " \t"))]
			out = append(out, indent+strings.TrimSuffix(trimmed, ";")+" http2;")
			changed = true
			continue
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n"), changed
}

func nginxVersionString() string {
	out, _ := exec.Command("nginx", "-v").CombinedOutput()
	return string(out)
}

func detectNginxConfigInvalid(_ repairCtx) (bool, string, error) {
	if _, err := exec.LookPath("nginx"); err != nil {
		return false, "", nil // no nginx on this host — not applicable
	}
	if !nginxVersionLT1251(nginxVersionString()) {
		return false, "", nil // >=1.25.1: `http2 on;` is valid, leave it
	}
	var bad []string
	for _, f := range managedNginxConfs {
		b, err := os.ReadFile(f)
		if err != nil {
			continue // file absent — nothing to heal here
		}
		for _, ln := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(ln) == "http2 on;" {
				bad = append(bad, f)
				break
			}
		}
	}
	if len(bad) == 0 {
		return false, "", nil
	}
	return true, fmt.Sprintf("`http2 on;` (nginx<1.25.1 unknown directive) in: %s — nginx -t fails, reloads rejected", strings.Join(bad, ", ")), nil
}

func fixNginxConfigInvalid(_ repairCtx) error {
	for _, f := range managedNginxConfs {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		folded, changed := foldHTTP2(string(b))
		if !changed {
			continue
		}
		fi, err := os.Stat(f)
		mode := os.FileMode(0o644)
		if err == nil {
			mode = fi.Mode().Perm()
		}
		if err := os.WriteFile(f, []byte(folded), mode); err != nil {
			return fmt.Errorf("rewrite %s: %w", f, err)
		}
	}
	if out, err := exec.Command("nginx", "-t").CombinedOutput(); err != nil {
		return fmt.Errorf("nginx -t still failing after fold (not reloading):\n%s", string(out))
	}
	if out, err := exec.Command("systemctl", "reload", "nginx").CombinedOutput(); err != nil {
		return fmt.Errorf("nginx -t passed but reload failed: %v\n%s", err, string(out))
	}
	return nil
}

// ---------- nginx-missing-includes ----------
//
// Scar (GH #217, webquest-nz): the panel :8443 vhost hard-`include`s two
// optional snippets — phpMyAdmin and Adminer. nginx fails `nginx -t` on a
// missing literal include, so when an optional component's install didn't
// run (phpMyAdmin's CDN was unreachable → its download died), the snippet
// was never written and the WHOLE :8443 vhost failed nginx -t → nothing
// listened on 8443 after a fresh install. install.sh now pre-creates empty
// placeholders (commit 2bbe85fd), but a host installed before that fix — or
// one whose include dir was wiped — stays down, and `jabali update` does not
// re-render the vhost. This detector self-heals that exact state by creating
// the empty placeholder targets the jabali vhost references.
//
// Scope is a fixed allowlist of the two include paths jabali owns, so we can
// never mask a genuinely-broken third-party include. ADR-0077.

var managedNginxIncludePlaceholders = []string{
	"/etc/nginx/sites-available/includes/phpmyadmin.conf",
	"/etc/nginx/snippets/jabali-adminer.conf",
}

const jabaliPanelVhost = "/etc/nginx/sites-available/jabali-panel.conf"

// missingPlaceholderTargets returns the subset of our managed include
// placeholders that the vhost `include`s but that `exists` reports absent.
// Pure (vhost text + exists predicate) so it is unit-testable without
// touching /etc/nginx.
func missingPlaceholderTargets(vhost string, exists func(string) bool) []string {
	var missing []string
	for _, target := range managedNginxIncludePlaceholders {
		if !strings.Contains(vhost, "include "+target+";") {
			continue // vhost doesn't reference it — leave alone
		}
		if !exists(target) {
			missing = append(missing, target)
		}
	}
	return missing
}

// vhostIncludesMissingTargets returns the subset of our managed include
// placeholders that the panel vhost `include`s but that are absent on disk.
func vhostIncludesMissingTargets() []string {
	b, err := os.ReadFile(jabaliPanelVhost)
	if err != nil {
		return nil // no panel vhost — not applicable
	}
	return missingPlaceholderTargets(string(b), func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	})
}

func detectNginxMissingIncludes(_ repairCtx) (bool, string, error) {
	if _, err := exec.LookPath("nginx"); err != nil {
		return false, "", nil // no nginx on this host — not applicable
	}
	missing := vhostIncludesMissingTargets()
	if len(missing) == 0 {
		return false, "", nil
	}
	return true, fmt.Sprintf("panel :8443 vhost includes missing file(s): %s — nginx -t fails, nothing listens on 8443", strings.Join(missing, ", ")), nil
}

func fixNginxMissingIncludes(_ repairCtx) error {
	for _, target := range vhostIncludesMissingTargets() {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("mkdir for %s: %w", target, err)
		}
		// Empty file = a no-op include; install_phpmyadmin / install_adminer
		// overwrite it with the real block when they next run.
		if err := os.WriteFile(target, []byte{}, 0o644); err != nil {
			return fmt.Errorf("create placeholder %s: %w", target, err)
		}
	}
	if out, err := exec.Command("nginx", "-t").CombinedOutput(); err != nil {
		return fmt.Errorf("nginx -t still failing after placeholders (not reloading):\n%s", string(out))
	}
	if out, err := exec.Command("systemctl", "reload", "nginx").CombinedOutput(); err != nil {
		return fmt.Errorf("nginx -t passed but reload failed: %v\n%s", err, string(out))
	}
	return nil
}

// ---------- automation-443-include ----------
//
// GH #1161: the opt-in "Automation API on :443" feature needs the panel-hostname
// :443 vhost (jabali-default.conf's GH#135 server block) to `include` a snippet
// the agent toggles between empty (off) and the /api/v1/automation/ proxy (on).
// The include line ships in install.sh, but `jabali update` never re-renders
// jabali-default.conf (see the http2 scar above), so existing/fleet boxes lack
// it and the Server-Settings toggle would silently do nothing. This detector
// self-heals: it seeds the empty snippet (so nginx -t has a target) and injects
// the include line into the hostname block. It never enables the feature — the
// snippet stays empty until the admin opts in. ADR-0077.

const jabaliDefaultVhost = "/etc/nginx/sites-available/jabali-default.conf"
const automation443SnippetPath = "/etc/nginx/snippets/jabali-automation-443.conf"
const automation443IncludeMarker = "include /etc/nginx/snippets/jabali-automation-443.conf;"

// auto443IncludeLine is inserted with the 4-space indentation the hostname
// vhost uses; auto443Anchor is that vhost's landing `location /`, whose
// try_files body is unique to it (the default_server + :80 blocks use the
// catch-all include instead), so the injection lands in the right server block.
const auto443IncludeLine = "    " + automation443IncludeMarker
const auto443Anchor = "    location / {\n        try_files $uri $uri/ =404;\n    }"

// ensureAutomation443Include injects the automation-443 include line before the
// panel-hostname vhost's landing location, idempotently. Pure (string in/out)
// so it is unit-testable without touching /etc/nginx. Returns changed=false
// when the include is already present or the hostname block is unrecognizable
// (hand-edited / very old box) — never guesses an insertion point.
func ensureAutomation443Include(conf string) (string, bool) {
	if strings.Contains(conf, automation443IncludeMarker) {
		return conf, false
	}
	idx := strings.Index(conf, auto443Anchor)
	if idx < 0 {
		return conf, false
	}
	return conf[:idx] + auto443IncludeLine + "\n\n" + conf[idx:], true
}

func detectAutomation443Include(_ repairCtx) (bool, string, error) {
	if _, err := exec.LookPath("nginx"); err != nil {
		return false, "", nil
	}
	b, err := os.ReadFile(jabaliDefaultVhost)
	if err != nil {
		return false, "", nil // no default vhost — not applicable
	}
	conf := string(b)
	includePresent := strings.Contains(conf, automation443IncludeMarker)
	anchorPresent := strings.Contains(conf, auto443Anchor)
	needInject := !includePresent && anchorPresent
	_, snippetErr := os.Stat(automation443SnippetPath)
	needSnippet := (includePresent || needInject) && snippetErr != nil
	if !needInject && !needSnippet {
		return false, "", nil
	}
	return true, "panel-hostname :443 vhost is missing the GH #1161 automation-API include (admin can't opt into API-on-443 for billing hosts that block outbound 8443)", nil
}

func fixAutomation443Include(_ repairCtx) error {
	// 1. Seed the empty snippet first so the include line always has a target.
	if _, err := os.Stat(automation443SnippetPath); err != nil {
		if err := os.MkdirAll(filepath.Dir(automation443SnippetPath), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(automation443SnippetPath), err)
		}
		body := "# Managed by jabali-agent (nginx.automation_public_set). Empty = API on :8443 only.\n"
		if err := os.WriteFile(automation443SnippetPath, []byte(body), 0o644); err != nil {
			return fmt.Errorf("seed %s: %w", automation443SnippetPath, err)
		}
	}
	// 2. Inject the include line into the hostname vhost if missing.
	b, err := os.ReadFile(jabaliDefaultVhost)
	if err != nil {
		return nil
	}
	out, changed := ensureAutomation443Include(string(b))
	if changed {
		fi, err := os.Stat(jabaliDefaultVhost)
		mode := os.FileMode(0o644)
		if err == nil {
			mode = fi.Mode().Perm()
		}
		if err := os.WriteFile(jabaliDefaultVhost, []byte(out), mode); err != nil {
			return fmt.Errorf("rewrite %s: %w", jabaliDefaultVhost, err)
		}
	}
	// 3. Validate + reload (both the snippet seed and the include edit want it).
	if out, err := exec.Command("nginx", "-t").CombinedOutput(); err != nil {
		return fmt.Errorf("nginx -t failing after automation-443 include heal (not reloading):\n%s", string(out))
	}
	if out, err := exec.Command("systemctl", "reload", "nginx").CombinedOutput(); err != nil {
		return fmt.Errorf("nginx -t passed but reload failed: %v\n%s", err, string(out))
	}
	return nil
}
