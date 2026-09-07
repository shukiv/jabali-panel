package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// domainDeleteParams is the input shape for domain.delete.
type domainDeleteParams struct {
	Domain string `json:"domain"`
}

// domainDeleteResponse is the output shape for domain.delete.
type domainDeleteResponse struct {
	Domain  string `json:"domain"`
	Deleted bool   `json:"deleted"`
}

func domainDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p domainDeleteParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate domain format
	if !domainRegex.MatchString(p.Domain) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid domain %q: must match ^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$", p.Domain),
		}
	}

	// Remove enabled symlink (ignore if missing)
	enabledPath := filepath.Join("/etc/nginx/sites-enabled", p.Domain+".conf")
	os.Remove(enabledPath)

	// Remove available config (ignore if missing)
	availablePath := filepath.Join("/etc/nginx/sites-available", p.Domain+".conf")
	os.Remove(availablePath)

	// Also reap the per-domain mail vhost (mail.<domain>, file
	// `<domain>-mail.conf`). domain.delete is only ever invoked on a FULL
	// domain teardown, so the mail vhost must go with it. reconcileWebmailVhosts
	// only visits domains that still exist, so a deleted domain's mail vhost was
	// otherwise never reaped — it orphaned in sites-available + sites-enabled on
	// every delete path (HTTP via Reconciler.ReconcileDeleted and CLI
	// `jabali user delete` alike). Same `<domain>-mail.conf` convention
	// webmail.vhost_remove uses; the single reload below covers it.
	removeMailVhostFiles(p.Domain)

	// JAB-230: reap the domain's relay credential (and re-point the owner's
	// default.cred). Placed HERE — the shared chokepoint every delete path
	// funnels through — per the #754 lesson, so no panel-side path can skip it.
	removeSendmailCredForDomain(p.Domain)

	// Reload nginx
	reloadCmd := execCommandContext(ctx, "systemctl", "reload", "nginx")
	var reloadOutput bytes.Buffer
	reloadCmd.Stdout = &reloadOutput
	reloadCmd.Stderr = &reloadOutput
	if err := reloadCmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("systemctl reload nginx failed: %s", reloadOutput.String()),
		}
	}

	// #432: reap the per-domain nginx logs (current + rotated/compressed) so a
	// deleted domain doesn't leave orphan logs accumulating in /var/log/nginx.
	// p.Domain is domainRegex-validated (only [a-z0-9-.]), so the glob carries no
	// attacker-controlled metacharacters.
	for _, suffix := range []string{"-access.log", "-error.log"} {
		matches, _ := filepath.Glob("/var/log/nginx/" + p.Domain + suffix + "*")
		for _, m := range matches {
			_ = os.Remove(m)
		}
	}

	// GH #1579: a full teardown must leave no stale SSL artifacts for the name —
	// the self-signed cert dir and any Let's Encrypt lineage. domain.delete is
	// the shared teardown chokepoint, so this covers both a real delete and a
	// rename's old-name teardown. Scoped strictly to p.Domain (domainRegex-
	// validated), so a shared/wildcard cert under a different name is untouched.
	removeDomainCertArtifacts(ctx, p.Domain)

	return domainDeleteResponse{
		Domain:  p.Domain,
		Deleted: true,
	}, nil
}

// removeDomainCertArtifacts deletes the on-disk TLS material a domain teardown
// leaves behind: the self-signed cert directory, the web certbot/LE lineage for
// the exact name, AND the per-domain mail lineage (mail.<domain>) that
// ssl.mail.issue created. Best-effort — a missing path is a no-op — and
// name-scoped, so it never reaches an unrelated (e.g. shared/wildcard)
// certificate. Reaping the mail lineage here matters: left behind it keeps an
// auto-renewing renewal conf pointing at a name that no longer resolves, which
// makes `certbot renew` noisy (and, with broken live files, can abort renew
// box-wide — the #738 scar). domain.delete is the shared teardown chokepoint, so
// this covers both a real delete and a rename's old-name teardown; on a rename
// the reissued cert lands under the distinct mail.<new> lineage, untouched.
func removeDomainCertArtifacts(ctx context.Context, domain string) {
	_ = os.RemoveAll(filepath.Join(baseSelfSignDir, domain))
	cleanupCertbotLineage(ctx, sslLERoot, domain)
	cleanupCertbotLineage(ctx, sslLERoot, "mail."+domain)
}

// removeMailVhostFiles reaps the per-domain mail vhost (`<domain>-mail.conf`)
// from sites-available + sites-enabled, using the same overridable path vars
// webmail.vhost_remove uses. Idempotent; returns true if it removed anything so
// the caller can decide whether an nginx reload is warranted. The caller drives
// the reload (domain.delete reloads once for the main + mail vhost together).
// domain is expected pre-validated (domainRegex) by the caller.
func removeMailVhostFiles(domain string) bool {
	changed := false
	for _, p := range []string{
		filepath.Join(mailVhostSitesEnabled, domain+"-mail.conf"),
		filepath.Join(mailVhostSitesAvailable, domain+"-mail.conf"),
	} {
		if _, err := os.Lstat(p); err != nil {
			continue // absent (or unreadable) — nothing to reap
		}
		if err := os.Remove(p); err == nil {
			changed = true
		}
	}
	return changed
}

func init() {
	Default.Register("domain.delete", domainDeleteHandler)
}
