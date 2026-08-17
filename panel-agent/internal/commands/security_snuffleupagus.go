// Package commands — security_snuffleupagus.go (M41, ADR-0088)
//
// Agent-side handlers for the Snuffleupagus PHP-hardening module.
// snuffleupagus.status — reports loaded .so per PHP minor + active rule SHA.
// snuffleupagus.reload — graceful-reload every per-PHP-version FPM unit
//                        after the panel reconciler rewrites active.rules.
//
// The journalctl-tail incident ingest pipeline lives in a sibling
// long-running goroutine (snuffleupagus_ingest.go); it pushes rows back
// to panel-api via the internal /admin/security/snuffleupagus/_ingest
// endpoint so panel-api owns persistence.
package commands

import (
	"strings"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)


const snuffleupagusActiveRulesPath = "/etc/jabali/snuffleupagus/active.rules"
const snuffleupagusActiveRulesDir = "/etc/jabali/snuffleupagus"

var phpMinorDirRe = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)

type snuffleupagusStatusResponse struct {
	Enabled           bool                          `json:"enabled"`
	Mode              string                        `json:"mode"`
	ActiveRulesSha256 string                        `json:"active_rules_sha256,omitempty"`
	PhpVersionsLoaded []snuffleupagusPhpVersionStat `json:"php_versions_loaded"`
}

type snuffleupagusPhpVersionStat struct {
	Minor       string `json:"minor"`
	ExtensionSO string `json:"extension_so"`
	Loaded      bool   `json:"loaded"`
}

func snuffleupagusStatusHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	resp := snuffleupagusStatusResponse{
		PhpVersionsLoaded: []snuffleupagusPhpVersionStat{},
	}

	// Active rules SHA for change-detection vs DB.
	if data, err := os.ReadFile(snuffleupagusActiveRulesPath); err == nil {
		sum := sha256.Sum256(data)
		resp.ActiveRulesSha256 = hex.EncodeToString(sum[:])
		// Mode read: off is a comment-only (empty) ruleset carrying the
		// `# mode=off` header (GH #718 — `sp.global.enable(0);` fatals FPM and
		// is no longer emitted). The legacy `sp.global.enable(0);` marker is
		// still recognised so a host mid-update reports off correctly.
		switch {
		case bytes.Contains(data, []byte("# mode=off")) || bytes.Contains(data, []byte("sp.global.enable(0);")):
			resp.Mode = "off"
		case bytes.Contains(data, []byte(".simulation();")):
			resp.Mode = "simulation"
		default:
			resp.Mode = "enforce"
		}
		resp.Enabled = resp.Mode != "off"
	}

	// Discover installed PHP minors via /etc/php/<minor>/cli/conf.d.
	entries, err := os.ReadDir("/etc/php")
	if err == nil {
		var minors []string
		for _, e := range entries {
			if !e.IsDir() || !phpMinorDirRe.MatchString(e.Name()) {
				continue
			}
			// /etc/php/<minor> dirs survive package removal sometimes
			// (Sury / Debian apt purge leaves the conf tree). Treat the
			// minor as installed only when the binary actually exists,
			// otherwise UI shows phantom versions like PHP 8.4 on a
			// host that only runs 8.5.
			binPath := "/usr/bin/php" + e.Name()
			if _, err := os.Stat(binPath); err != nil {
				continue
			}
			minors = append(minors, e.Name())
		}
		sort.Strings(minors)
		for _, m := range minors {
			so := filepath.Join("/usr/lib/php/jabali-snuffleupagus", m, "snuffleupagus.so")
			loaded := false
			if _, err := os.Stat(so); err == nil {
				// GH #677: WEB protection comes from the FPM drop-in (Jabali's
				// per-user FPM pools load it), NOT the CLI drop-in. Report loaded
				// only when the FPM drop-in is present, so the panel never claims
				// PHP is protected while FPM requests run unprotected.
				fpmDrop := filepath.Join("/etc/php", m, "fpm/conf.d/30-jabali-snuffleupagus.ini")
				if _, err := os.Stat(fpmDrop); err == nil {
					loaded = true
				}
			}
			resp.PhpVersionsLoaded = append(resp.PhpVersionsLoaded, snuffleupagusPhpVersionStat{
				Minor:       m,
				ExtensionSO: so,
				Loaded:      loaded,
			})
		}
	}

	return resp, nil
}

type snuffleupagusReloadResponse struct {
	Pools []snuffleupagusPoolReloadResult `json:"pools"`
}

type snuffleupagusPoolReloadResult struct {
	Unit   string `json:"unit"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// snuffleupagusReloadHandler issues `systemctl reload-or-restart` against
// every active per-user FPM unit (jabali-fpm@<user>.service, M9.6/M25
// per-user pools — system phpX.Y-fpm is masked). Reload is graceful: in-
// flight requests finish before workers respawn with the new active.rules.
func snuffleupagusReloadHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	resp := snuffleupagusReloadResponse{Pools: []snuffleupagusPoolReloadResult{}}

	listCmd := execCommandContext(ctx,
		"systemctl", "list-units", "jabali-fpm@*.service",
		"--no-legend", "--no-pager", "--state=loaded",
	)
	out, err := listCmd.Output()
	if err != nil {
		return resp, nil
	}
	var units []string
	for _, line := range bytes.Split(out, []byte("\n")) {
		fields := bytes.Fields(line)
		if len(fields) == 0 {
			continue
		}
		// First field can be a UTF-8 bullet ("●"); take the first token
		// that ends with .service.
		for _, f := range fields {
			if bytes.HasSuffix(f, []byte(".service")) {
				units = append(units, string(f))
				break
			}
		}
	}
	sort.Strings(units)

	var failed []string
	for _, unit := range units {
		cmd := execCommandContext(ctx, "systemctl", "reload-or-restart", unit)
		rOut, err := cmd.CombinedOutput()
		if err != nil {
			resp.Pools = append(resp.Pools, snuffleupagusPoolReloadResult{
				Unit:   unit,
				OK:     false,
				Detail: fmt.Sprintf("%v: %s", err, string(rOut)),
			})
			failed = append(failed, unit)
			continue
		}
		resp.Pools = append(resp.Pools, snuffleupagusPoolReloadResult{Unit: unit, OK: true})
	}

	// GH #707: a failed FPM reload means those pools keep serving with STALE
	// Snuffleupagus rules (or PHP Defense effectively off) while the DB/UI would
	// otherwise report the new policy as applied. Surface it as an error so the
	// apply is not marked successful on a partial reload.
	if len(failed) > 0 {
		return resp, fmt.Errorf("%d of %d PHP-FPM pool reload(s) failed — PHP Defense may be stale on: %s", len(failed), len(units), strings.Join(failed, ", "))
	}
	return resp, nil
}

type snuffleupagusApplyParams struct {
	// Body is the full text of the rendered active.rules file. The
	// panel-api's reconciler computes the SHA, diffs vs DB state, and
	// only invokes apply when a write is actually needed — so this
	// handler doesn't dedupe.
	Body string `json:"body"`
}

type snuffleupagusApplyResponse struct {
	Sha256 string                          `json:"sha256"`
	Pools  []snuffleupagusPoolReloadResult `json:"pools"`
}

// snuffleupagusApplyHandler atomically writes the panel-rendered rules
// to /etc/jabali/snuffleupagus/active.rules, then reload-or-restarts
// every per-user FPM unit. The panel-api process is read-only on /etc
// (M25 ProtectSystem=strict) — this delegation keeps the privileged
// write inside the agent profile boundary.
func snuffleupagusApplyHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p snuffleupagusApplyParams
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &p)
	}
	if p.Body == "" {
		return nil, fmt.Errorf("body required")
	}
	if err := os.MkdirAll(snuffleupagusActiveRulesDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(snuffleupagusActiveRulesDir, ".active.rules.*.tmp")
	if err != nil {
		return nil, fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write([]byte(p.Body)); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("write: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpName, snuffleupagusActiveRulesPath); err != nil {
		return nil, fmt.Errorf("rename: %w", err)
	}
	sum := sha256.Sum256([]byte(p.Body))
	resp := snuffleupagusApplyResponse{
		Sha256: hex.EncodeToString(sum[:]),
	}
	// GH #707: propagate the reload failure. The rules file is written, but if
	// the FPM pools did not reload they are serving STALE rules — the apply must
	// NOT report success, or the DB/UI claim a policy that is not live.
	reload, reloadErr := snuffleupagusReloadHandler(ctx, nil)
	if r, ok := reload.(snuffleupagusReloadResponse); ok {
		resp.Pools = r.Pools
	}
	if reloadErr != nil {
		return resp, reloadErr
	}
	return resp, nil
}

func init() {
	Default.Register("snuffleupagus.status", snuffleupagusStatusHandler)
	Default.Register("snuffleupagus.reload", snuffleupagusReloadHandler)
	Default.Register("snuffleupagus.apply_rules", snuffleupagusApplyHandler)
}
