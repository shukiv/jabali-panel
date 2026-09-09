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

// domain.web_teardown removes ONLY the WEB facet of a domain — the nginx web
// vhost (sites-available + sites-enabled) and the web TLS lineage — while
// leaving the MAIL facet (the per-domain mail vhost, its relay credential, the
// mail.<domain> certificate) and the DNS zone intact (GH #1603, johnnyq).
//
// It is the primitive the panel uses for a facet-preserving delete: "delete the
// Web Domain, keep the Mail Domain and/or DNS Zone". Neither of the existing
// verbs fits — domain.disable only unlinks the enabled symlink (a reversible
// pause), and domain.delete reaps the mail vhost, the relay cred, and BOTH the
// web AND mail cert lineages (a full teardown). This one is scoped strictly to
// the web parts.
//
// Idempotent: a missing file is a no-op, so a retry after a mid-run failure
// converges. The domain row survives web-off; the reconciler already skips the
// web render for a WebDisabled row, so it never re-creates what this removed.
type domainWebTeardownParams struct {
	Domain string `json:"domain"`
}

type domainWebTeardownResponse struct {
	Domain   string `json:"domain"`
	TornDown bool   `json:"torn_down"`
}

func domainWebTeardownHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p domainWebTeardownParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	if !domainRegex.MatchString(p.Domain) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid domain %q: must match ^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$", p.Domain),
		}
	}

	// JAB-71: serialize the unlink→reload against every other nginx op.
	nginxOpMu.Lock()
	defer nginxOpMu.Unlock()

	// Remove the WEB vhost only (sites-enabled symlink + sites-available conf).
	// The per-domain MAIL vhost (<domain>-mail.conf) is deliberately left alone —
	// that is the difference from domain.delete.
	os.Remove(filepath.Join("/etc/nginx/sites-enabled", p.Domain+".conf"))
	os.Remove(filepath.Join("/etc/nginx/sites-available", p.Domain+".conf"))

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

	// #432: reap the per-domain nginx WEB logs (current + rotated).
	for _, suffix := range []string{"-access.log", "-error.log"} {
		matches, _ := filepath.Glob("/var/log/nginx/" + p.Domain + suffix + "*")
		for _, m := range matches {
			_ = os.Remove(m)
		}
	}

	// Remove the WEB TLS artifacts for the exact name only — the self-signed dir
	// and the web certbot/LE lineage. The mail.<domain> lineage is NOT touched
	// (removeDomainCertArtifacts reaps both; here we keep mail). Best-effort +
	// name-scoped (domainRegex-validated), so a shared/wildcard cert is untouched.
	_ = os.RemoveAll(filepath.Join(baseSelfSignDir, p.Domain))
	cleanupCertbotLineage(ctx, sslLERoot, p.Domain)

	return domainWebTeardownResponse{Domain: p.Domain, TornDown: true}, nil
}

func init() {
	Default.Register("domain.web_teardown", domainWebTeardownHandler)
}
