package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// systemReservedFPMSlugs are non-tenant FPM pools that live under
// userPhpverDir but must NEVER be reaped as orphans — e.g. phpMyAdmin's "pma"
// pool, seeded by install.sh. They have no panel-user row, so the orphan
// reaper would otherwise mistake them for a deleted user's leftovers.
var systemReservedFPMSlugs = map[string]bool{
	"pma": true,
}

// versionedSlugRe matches a versioned pool slug ("alice-php8.2") and captures
// the owning username. Usernames themselves cannot contain "-" or "." (see
// phpPoolUsernameRegex), so the split is unambiguous.
var versionedSlugRe = regexp.MustCompile(`^(.+)-php[0-9]+\.[0-9]+$`)

// usernameForSlug returns the owning username for a pool slug. A versioned slug
// ("alice-php8.2") maps to its base username ("alice"); a default slug is the
// username itself.
func usernameForSlug(slug string) string {
	if m := versionedSlugRe.FindStringSubmatch(slug); m != nil {
		return m[1]
	}
	return slug
}

// reapFPMPoolArtifacts stops+disables the slug's FPM master and removes its
// on-disk artifacts (pool.d confs, per-user fpm conf, version-pin file, and —
// for a versioned slug — its systemd drop-in dir). Best-effort: every step
// tolerates already-missing files and never returns an error, so it is safe to
// call from user.delete teardown and the orphan reaper. Mirrors the teardown in
// php.pool.remove.
func reapFPMPoolArtifacts(ctx context.Context, slug string) {
	if !phpPoolSlugRegex.MatchString(slug) || strings.Contains(slug, "..") {
		return
	}
	isVersioned := versionedSlugRe.MatchString(slug)

	// Stop + disable the FPM master.
	if os.Getenv("JABALI_PHP_POOL_SKIP_RELOAD") == "" {
		svc := fmt.Sprintf("jabali-fpm@%s.service", slug)
		_ = exec.CommandContext(ctx, "systemctl", "stop", svc).Run()
		_ = exec.CommandContext(ctx, "systemctl", "disable", "--quiet", svc).Run()
	}

	// pool.d confs across all installed PHP versions.
	if matches, err := filepath.Glob(fmt.Sprintf("/etc/php/*/fpm/pool.d/jabali-%s.conf", slug)); err == nil {
		for _, path := range matches {
			_ = os.Remove(path)
		}
	}

	// Per-user fpm conf.
	fpmConfRoot := os.Getenv("JABALI_FPM_CONFIG_ROOT")
	if fpmConfRoot == "" {
		fpmConfRoot = "/etc/jabali-panel/fpm"
	}
	_ = os.Remove(filepath.Join(fpmConfRoot, slug+".conf"))

	// Version-pin file — the one fpmWorkerStatus counts (GH #686).
	_ = os.Remove(filepath.Join(userPhpverDir, slug))

	// A versioned slug owns its own drop-in dir; the default pool's is owned by
	// user.slice.ensure and must be left intact.
	if isVersioned {
		_ = os.RemoveAll(filepath.Join(systemdRoot(), fmt.Sprintf("jabali-fpm@%s.service.d", slug)))
		if os.Getenv("JABALI_PHP_POOL_SKIP_RELOAD") == "" {
			_ = exec.CommandContext(ctx, "systemctl", "daemon-reload").Run()
		}
	}
}

// reapUserFPMPools tears down every FPM pool owned by username — the default
// pool (slug == username) plus any versioned masters ("<username>-phpX.Y").
// Called from user.delete so a deleted user leaves no orphaned pool behind
// (GH #686: the stale version-pin file otherwise inflated the PHP Manager
// "FPM workers" count).
func reapUserFPMPools(ctx context.Context, username string) {
	if !phpPoolUsernameRegex.MatchString(username) {
		return
	}
	// Default pool.
	reapFPMPoolArtifacts(ctx, username)

	// Versioned pools: discover them by their per-user fpm confs.
	fpmConfRoot := os.Getenv("JABALI_FPM_CONFIG_ROOT")
	if fpmConfRoot == "" {
		fpmConfRoot = "/etc/jabali-panel/fpm"
	}
	if matches, err := filepath.Glob(filepath.Join(fpmConfRoot, username+"-php*.conf")); err == nil {
		for _, path := range matches {
			slug := strings.TrimSuffix(filepath.Base(path), ".conf")
			if usernameForSlug(slug) == username {
				reapFPMPoolArtifacts(ctx, slug)
			}
		}
	}
}

// --- Orphan reaper (php.pool.reap-orphans) ---------------------------------

// phpPoolReapOrphansParams is the input shape for php.pool.reap-orphans.
type phpPoolReapOrphansParams struct {
	// KeepUsernames is the authoritative set of usernames that still own a
	// panel account. A pool whose owner is NOT in this set is a candidate
	// orphan. REQUIRED: a nil pointer (field absent) is refused so a broken
	// caller can never trigger a mass reap.
	KeepUsernames *[]string `json:"keep_usernames"`
}

// phpPoolReapOrphansResponse is the output shape for php.pool.reap-orphans.
type phpPoolReapOrphansResponse struct {
	Reaped []string `json:"reaped"`
}

// osUserExists reports whether an OS account named username exists. A package
// var so tests can stub it. Used as a hard safety belt: even with a wrong
// keep-list, a pool whose OS user still exists is never reaped.
var osUserExists = func(username string) bool {
	_, err := user.Lookup(username)
	return err == nil
}

// maxReapPerCall bounds agent work per reap pass, mirroring the reconciler's
// 50-item policy elsewhere.
const maxReapPerCall = 50

// phpPoolReapOrphansHandler garbage-collects FPM pools left behind by a deleted
// user (GH #686). It enumerates userPhpverDir and reaps any pool whose owning
// username is (a) not in KeepUsernames, AND (b) has no surviving OS account,
// AND (c) is not a system-reserved slug (e.g. "pma"). The triple guard means a
// live tenant's pool survives even if the caller's keep-list is stale/empty.
func phpPoolReapOrphansHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p phpPoolReapOrphansParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	if p.KeepUsernames == nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "keep_usernames is required (a nil list would risk a mass reap)",
		}
	}
	keep := make(map[string]bool, len(*p.KeepUsernames))
	for _, u := range *p.KeepUsernames {
		keep[u] = true
	}

	entries, err := os.ReadDir(userPhpverDir)
	if err != nil {
		if os.IsNotExist(err) {
			return phpPoolReapOrphansResponse{Reaped: []string{}}, nil
		}
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to read %s: %v", userPhpverDir, err),
		}
	}

	reaped := make([]string, 0)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		slug := e.Name()
		if systemReservedFPMSlugs[slug] {
			continue
		}
		owner := usernameForSlug(slug)
		if keep[owner] {
			continue
		}
		if osUserExists(owner) { // safety belt vs a stale/empty keep-list
			continue
		}
		reapFPMPoolArtifacts(ctx, slug)
		reaped = append(reaped, slug)
		if len(reaped) >= maxReapPerCall {
			break
		}
	}
	return phpPoolReapOrphansResponse{Reaped: reaped}, nil
}

func init() {
	Default.Register("php.pool.reap-orphans", phpPoolReapOrphansHandler)
}
