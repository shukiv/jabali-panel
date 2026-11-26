// Boot-time apply of the jabali CRS "before" exclusion plugin (GH #594).
//
// CRSPluginBefore() is static, single-sourced in internal/appseccfg. Running it
// at agent boot makes a shipped CRS-exclusion change (GH #404/#594) self-heal on
// the `jabali update` restart, FLEET-WIDE, with no per-server operator step:
// update installs the new binaries + restarts jabali-agent → the NEW agent
// boots → re-renders CRSPluginBeforePath. Write-on-diff (no-op on a clean boot).
//
// This writes ONLY the built-in file (CRSPluginBeforePath). The panel-api
// `appsec render-config` CLI writes the exact same built-in content to that same
// path — so the two writers are byte-identical there and never oscillate — plus
// the operator-managed exclusions to a SEPARATE file (CRSPluginOperatorBeforePath),
// which this agent never touches. The split exists because the agent has no
// database access: when the operator exclusions shared this file, every agent
// restart rewrote it to built-ins-only and dropped them, re-banning users until
// the next render-config (GH #1655). Keep this writer built-ins-only.
package commands

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
)

// ApplyAppSecBeforePlugin writes appseccfg.CRSPluginBefore() to its CRS data
// path and reloads crowdsec on a real diff. Best-effort: logs + returns on any
// error so a boot is never blocked. No-op when crowdsec isn't installed.
func ApplyAppSecBeforePlugin(ctx context.Context, log *slog.Logger) {
	// Gate on the CRS data tree — absent on hosts without crowdsec (CI/dev).
	if _, err := os.Stat("/var/lib/crowdsec/data"); err != nil {
		return
	}
	// The built-in after-plugin (CRSPluginAfter) is static too and written the
	// same way; one reload covers both.
	var changed []string
	for _, f := range []struct{ path, body string }{
		{appseccfg.CRSPluginBeforePath, appseccfg.CRSPluginBefore()},
		{appseccfg.CRSPluginAfterPath, appseccfg.CRSPluginAfter()},
	} {
		if existing, _ := os.ReadFile(f.path); string(existing) == f.body {
			continue // already current — clean-boot no-op
		}
		if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
			log.Warn("appsec crs-plugin: mkdir failed", "path", f.path, "err", err)
			continue
		}
		if err := writeFileAtomically(f.path, []byte(f.body), 0o644); err != nil {
			log.Warn("appsec crs-plugin: write failed", "path", f.path, "err", err)
			continue
		}
		changed = append(changed, f.path)
	}
	if len(changed) == 0 {
		return
	}
	log.Info("appsec crs-plugins re-rendered on boot — reloading crowdsec", "paths", changed)
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if out, err := crowdsecReload(rctx); err != nil {
		if out2, err2 := execCommandContext(rctx, "systemctl", "restart", "crowdsec").CombinedOutput(); err2 != nil {
			log.Warn("appsec before-plugin: crowdsec reload+restart failed",
				"reload_err", err, "reload_out", string(out),
				"restart_err", err2, "restart_out", string(out2))
		}
	}
}
