// Package reconciler — Snuffleupagus active.rules renderer (M41, ADR-0088).
//
// Reads:  snuffleupagus_state.mode + snuffleupagus_rule_overrides
// Writes: /etc/jabali/snuffleupagus/active.rules (atomic temp-file rename)
// Calls:  agent snuffleupagus.reload (graceful FPM pool reload) on change.
//
// Triggered:
//   - on every panel boot (idempotent — no-op if file SHA matches DB sha)
//   - whenever an admin POSTs /admin/security/snuffleupagus/mode or
//     toggles a rule
package reconciler

import (
	"bytes"
	"regexp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

var simulationRe = regexp.MustCompile(`\.drop\(\)(?:\.simulation\(\))?;`)

const (
	snufActiveRulesPath = "/etc/jabali/snuffleupagus/active.rules"
	snufBundleDir       = "/usr/share/jabali/snuffleupagus/rules"
	snufFallbackBundle  = "/opt/jabali-panel/install/snuffleupagus/rules"
)

// SnuffleupagusReconciler renders the active.rules file from DB state.
type SnuffleupagusReconciler struct {
	Repo  repository.SnuffleupagusRepository
	Agent agent.AgentInterface
}

// Reconcile renders the active rules file based on state + overrides.
// No-op if the rendered SHA matches last_applied_sha256.
func (r *SnuffleupagusReconciler) Reconcile(ctx context.Context) error {
	state, err := r.Repo.GetState(ctx)
	if err != nil {
		return fmt.Errorf("get state: %w", err)
	}
	overrides, err := r.Repo.ListOverrides(ctx)
	if err != nil {
		return fmt.Errorf("list overrides: %w", err)
	}

	rendered, err := renderActiveRules(state.Mode, overrides)
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}
	sum := sha256.Sum256(rendered)
	sha := hex.EncodeToString(sum[:])

	if state.LastAppliedSha256 != nil && *state.LastAppliedSha256 == sha {
		// DB says already applied — but verify the file on disk still matches.
		// If someone edited active.rules manually (or the file is missing),
		// we must re-apply regardless of the DB SHA.
		if diskSHAMatches(snufActiveRulesPath, sha) {
			return nil
		}
	}

	// panel-api is ProtectSystem=strict (M25); /etc is read-only here.
	// Delegate the write + FPM reload to the agent — same trust boundary
	// as every other privileged config write (nft, audit rules, ...).
	if r.Agent == nil {
		return fmt.Errorf("agent not configured; cannot apply rules")
	}
	actx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := r.Agent.Call(actx, "snuffleupagus.apply_rules", map[string]any{
		"body": string(rendered),
	}); err != nil {
		return fmt.Errorf("agent apply_rules: %w", err)
	}

	now := time.Now().UTC()
	if err := r.Repo.UpdateState(ctx, state.Mode, &now, &sha); err != nil {
		return fmt.Errorf("update state: %w", err)
	}
	return nil
}

// renderActiveRules composes the rule bundle for the given mode.
//   - mode=off:        comment-only (empty ruleset); no directive —
//                      `sp.global.enable` is invalid in v0.13 and fatals FPM (GH #718).
//   - mode=simulation: concat the bundle, wrap each rule with .simulation()
//                      where the directive supports it (drop, default action).
//   - mode=enforce:    concat the bundle as-is.
// In all non-off modes the override list disables individual rules by
// commenting them out at the end of the file.
// diskSHAMatches returns true if the file at path exists and its SHA256
// hex matches expected. Any read error (missing file, permission denied)
// returns false so the caller re-renders.
func diskSHAMatches(path, expected string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) == expected
}

func renderActiveRules(mode models.SnuffleupagusMode, overrides []models.SnuffleupagusRuleOverride) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("# Jabali Snuffleupagus active rules -- RENDERED, do not edit.\n")
	buf.WriteString(fmt.Sprintf("# mode=%s rendered_at=%s\n\n", mode, time.Now().UTC().Format(time.RFC3339)))

	if mode == models.SnuffleupagusModeOff {
		// Snuffleupagus v0.13 has NO master switch: `sp.global.enable(0)` is not
		// a valid directive — it fatals "Unexpected keyword 'enable'", which
		// crashes PHP-FPM and 500s every site on the pool (GH #718). "Off" is an
		// EMPTY ruleset — the extension loads but enforces nothing. The
		// `# mode=off` header written above is the marker the agent status
		// handler reads to report the mode.
		return buf.Bytes(), nil
	}

	// Concat bundle files in numeric prefix order.
	bundleDir := snufBundleDir
	if _, err := os.Stat(bundleDir); err != nil {
		bundleDir = snufFallbackBundle
	}
	files, err := filepath.Glob(filepath.Join(bundleDir, "*.rules"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	// GH #676: set of operator-disabled directive lines. RuleName is
	// "<base>#<line> <sp...>" (see listBundleRules); the "<base>#<line>" prefix
	// pins the exact directive to drop. The renderer used to emit every line and
	// only APPEND a note, so a "disabled" rule stayed fully active.
	disabledLines := map[string]bool{}
	for _, ov := range overrides {
		if !ov.Enabled {
			key := ov.RuleName
			if sp := strings.IndexByte(key, ' '); sp > 0 {
				key = key[:sp]
			}
			disabledLines[key] = true
		}
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f, err)
		}
		base := filepath.Base(f)
		buf.WriteString(fmt.Sprintf("\n# --- %s ---\n", base))
		for i, line := range strings.Split(string(data), "\n") {
			if disabledLines[base+"#"+strconv.Itoa(i+1)] {
				buf.WriteString("# DISABLED by operator: " + line + "\n")
				continue
			}
			if mode == models.SnuffleupagusModeSimulation {
				line = string(simulationRe.ReplaceAll([]byte(line), []byte(".drop().simulation();")))
			}
			buf.WriteString(line + "\n")
		}
	}

	// Operator overrides: emit kill directives. Snuffleupagus has no native
	// "disable rule by name" — we wrap with a trailing comment so operators
	// can audit via the file. The active disable happens by NOT including
	// the rule's directive line; the upstream rule files use `name=` so we
	//'d grep+strip per name. For now the override list is recorded and the
	// admin UI surfaces it; the next bundle revision will adopt named-rule
	// directives consistently.
	if len(overrides) > 0 {
		buf.WriteString("\n# --- operator overrides ---\n")
		for _, ov := range overrides {
			if !ov.Enabled {
				reason := ""
				if ov.Reason != nil {
					reason = strings.ReplaceAll(*ov.Reason, "\n", " ")
				}
				buf.WriteString(fmt.Sprintf("# DISABLED rule=%q reason=%q at=%s\n",
					ov.RuleName, reason, ov.SetAt.UTC().Format(time.RFC3339)))
			}
		}
	}

	return buf.Bytes(), nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".active.rules.*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
