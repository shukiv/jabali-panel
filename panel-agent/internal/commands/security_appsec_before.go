// Boot-time apply of the jabali CRS "before" exclusion plugin (GH #594).
//
// CRSPluginBefore() is static, single-sourced in internal/appseccfg — the SAME
// content the panel-api `appsec render-config` CLI writes. Running it at agent
// boot makes a shipped CRS-exclusion change (GH #404/#594) self-heal on the
// `jabali update` restart, FLEET-WIDE, with no per-server operator step:
// update installs the new binaries + restarts jabali-agent → the NEW agent
// boots → re-renders before.conf. Write-on-diff (no-op on a clean boot) and
// byte-identical to the CLI writer, so the two never oscillate.
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
	body := appseccfg.CRSPluginBefore()
	path := appseccfg.CRSPluginBeforePath
	if existing, _ := os.ReadFile(path); string(existing) == body {
		return // already current — clean-boot no-op
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Warn("appsec before-plugin: mkdir failed", "err", err)
		return
	}
	if err := writeFileAtomically(path, []byte(body), 0o644); err != nil {
		log.Warn("appsec before-plugin: write failed", "path", path, "err", err)
		return
	}
	log.Info("appsec before-plugin re-rendered on boot — reloading crowdsec", "path", path)
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if out, err := execCommandContext(rctx, "systemctl", "reload", "crowdsec").CombinedOutput(); err != nil {
		if out2, err2 := execCommandContext(rctx, "systemctl", "restart", "crowdsec").CombinedOutput(); err2 != nil {
			log.Warn("appsec before-plugin: crowdsec reload+restart failed",
				"reload_err", err, "reload_out", string(out),
				"restart_err", err2, "restart_out", string(out2))
		}
	}
}
