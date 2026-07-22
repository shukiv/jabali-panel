package commands

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// GH #522: CyberPanel's account home is /home/<domain> (dots), so the src_account
// validation must ACCEPT a dotted domain while still blocking "/" and "..".
// A dotted account with a valid in-home src_path must get PAST validation — the
// only error left is the downstream connect/rsync (no real source here). This
// guards the fix that changed ContainsAny("/.") to reject just "/" and "..".
func TestRsyncRemoteHome_AllowsDottedDomainAccount(t *testing.T) {
	base := map[string]any{
		"job_id": "j1", "host": "src.example.com", "ssh_user": "root",
		"secret_path": "/etc/jabali-panel/migration-secrets/j1.env",
		"dest_path":   "/home/clpe2e/domains/d/public_html", "dest_user": "clpe2e",
	}
	// valid: dotted domain account + src under its home
	base["src_account"] = "smoke.jabalitest.com"
	base["src_path"] = "/home/smoke.jabalitest.com/public_html"
	raw, _ := json.Marshal(base)
	_, err := migrationRsyncRemoteHomeHandler(context.Background(), raw)
	// It will fail downstream (no reachable source), but NOT on the src_account
	// / src_path validation — those must have passed for the dotted domain.
	if err != nil {
		if strings.Contains(err.Error(), "bare account name") ||
			strings.Contains(err.Error(), "under /home/<src_account>") ||
			strings.Contains(err.Error(), "resolves outside /home") {
			t.Errorf("dotted domain account was wrongly rejected by validation: %v", err)
		}
	}

	// still rejected: ".." traversal in the account
	base["src_account"] = "a..b"
	base["src_path"] = "/home/a..b/public_html"
	raw, _ = json.Marshal(base)
	if _, err := migrationRsyncRemoteHomeHandler(context.Background(), raw); err == nil ||
		!strings.Contains(err.Error(), "bare account name") {
		t.Errorf("'..' in src_account must be rejected, got %v", err)
	}
}
