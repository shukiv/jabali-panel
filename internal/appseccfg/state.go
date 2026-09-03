package appseccfg

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// WebmailHostsPath is the canonical state file that lists every
// webmail/autoconfig FQDN the panel manages. It is written by the
// panel-api reconciler (write-on-diff each pass) and read by both
// `jabali-panel appsec render-config` and the panel-agent geoblock
// handler when assembling the AppSec config — so both renderers see
// the same allowlist. Missing or empty file → no on_match webmail
// allowlist emitted (and CrowdSec WAF stays fully active on webmail
// vhosts, the safe default).
//
// Format: one lower-case FQDN per line. Lines starting with '#' and
// blank lines are ignored. Anything failing sanitizeWebmailHosts is
// dropped silently — operators must not edit this file by hand.
// Lives under /var/lib/jabali-panel (owned by the panel's service user)
// rather than /etc/jabali-panel (root-owned config dir): the reconciler
// runs as the unprivileged service user and could not create the file
// (or its tmp) in the root-owned config dir — "permission denied" every
// pass — and widening that dir would have made the root-owned secrets
// (kratos.yml, *.env) group-writable. The agent reads via this same
// const, so both writer + readers follow the move.
const WebmailHostsPath = "/var/lib/jabali-panel/webmail-hosts.list"

// BotExemptHostsPath is the state file listing the FQDNs whose domains the
// operator has marked exempt from the AppSec bot-detection challenge
// (per-domain opt-out). Written by the panel reconciler (write-on-diff each
// pass) and read by RenderBotExempt so those Hosts get an ExemptFromChallenge
// pre_eval alongside the built-in exemptions. Same location + safe-default
// semantics as WebmailHostsPath. Missing/empty → no per-domain exemptions.
const BotExemptHostsPath = "/var/lib/jabali-panel/bot-exempt-hosts.list"

// LoadBotExemptHosts reads + sanitizes the per-domain bot-challenge exempt
// list. Missing file is not an error (nil, nil) — the safe default is "no
// per-domain exemptions", exactly like LoadWebmailHosts.
func LoadBotExemptHosts(path string) ([]string, error) {
	return loadHostList(path)
}

// WriteBotExemptHosts writes the sanitized, sorted exempt-host list atomically.
// Returns (changed, error); write-on-diff so a caller can gate a reload.
func WriteBotExemptHosts(path string, hosts []string) (changed bool, err error) {
	return writeHostList(
		path,
		"# Managed by jabali-panel reconciler — DO NOT hand-edit.\n"+
			"# FQDNs exempt from the AppSec bot-detection challenge (per-domain\n"+
			"# opt-out). Read by internal/appseccfg.RenderBotExempt. One FQDN per line.\n",
		hosts,
	)
}

// LoadWebmailHosts reads and sanitizes the state file at the given
// path. Missing file is NOT an error (returns nil, nil) — fresh
// installs before the first reconciler pass have no allowlist, which
// is the right safe default. Anything failing sanitize is dropped.
func LoadWebmailHosts(path string) ([]string, error) {
	return loadHostList(path)
}

// loadHostList reads a one-FQDN-per-line state file, ignoring blanks and
// comments, and returns the sanitized list. Missing file → (nil, nil): the
// safe default for every AppSec host-list feature.
func loadHostList(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("appseccfg: open %s: %w", path, err)
	}
	defer f.Close()
	var raw []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		raw = append(raw, line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("appseccfg: read %s: %w", path, err)
	}
	return sanitizeWebmailHosts(raw), nil
}

// WriteWebmailHosts writes the sanitized, dedup'd, sorted FQDN list to
// path as a managed-by-jabali state file. Atomic via tmp+rename in the
// same directory. Returns (changed, error). When the on-disk content
// already matches what would be written, this is a no-op and changed
// is false — callers can gate a downstream reload on changed=true.
func WriteWebmailHosts(path string, hosts []string) (changed bool, err error) {
	return writeHostList(
		path,
		"# Managed by jabali-panel reconciler — DO NOT hand-edit.\n"+
			"# Source of truth for the CrowdSec AppSec webmail-vhost allowlist\n"+
			"# (internal/appseccfg.Render). Re-rendered every reconciler pass\n"+
			"# from the domain repo + panel-primary row. One FQDN per line.\n",
		hosts,
	)
}

// writeHostList writes a sanitized, sorted host list atomically (tmp+rename)
// with the given managed-by header. Returns (changed, error); a no-op with
// changed=false when the on-disk content already matches, so callers can gate
// a downstream reload on changed=true.
func writeHostList(path, header string, hosts []string) (changed bool, err error) {
	clean := sanitizeWebmailHosts(hosts)
	var b strings.Builder
	b.WriteString(header)
	for _, h := range clean {
		b.WriteString(h + "\n")
	}
	body := b.String()

	existing, readErr := os.ReadFile(path)
	if readErr == nil && string(existing) == body {
		return false, nil
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("appseccfg: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "webmail-hosts-*.tmp")
	if err != nil {
		return false, fmt.Errorf("appseccfg: tempfile: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if _, err := io.WriteString(tmp, body); err != nil {
		tmp.Close()
		return false, fmt.Errorf("appseccfg: write tmp: %w", err)
	}
	// 0644 root:root — readable by render-config CLI (root) and the
	// panel-agent geoblock handler (root). The file is never private.
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return false, fmt.Errorf("appseccfg: chmod tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("appseccfg: close tmp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return false, fmt.Errorf("appseccfg: rename: %w", err)
	}
	return true, nil
}
