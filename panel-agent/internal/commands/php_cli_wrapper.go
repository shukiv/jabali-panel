// php_cli_wrapper.go — per-user CLI PHP wrapper (GH #184).
//
// The panel pins each user's PHP version for their FPM pool (web), but the
// interactive shell, Composer, wp-cli, and cron resolve a bare `php` to the
// host default /usr/bin/php — so a user pinned to a non-default version got
// the wrong version at the CLI, and extensions enabled for their version
// looked "missing". This writes a per-user `php` that points at their pinned
// version; the SSH shell + cron prepend its dir to PATH so `php`, and
// anything with a `#!/usr/bin/env php` shebang (Composer, wp-cli), follow it.
//
// Layout (option B): /home/<user>/.jabali/bin/php -> /usr/bin/php<version>.
//
// SECURITY (ADR-0126): the agent runs as root and writes under /home/<user>.
// On a jabali host that home is root-owned 0751, but we do NOT rely on that
// implicitly — every path component we touch is Lstat'd and refused if it is
// a symlink or not root-owned, so a tenant who *did* control their home could
// not use a planted symlink to redirect root's mkdir/chown/symlink/rename
// into privileged space (symlink TOCTOU). Chowns use Lchown (never follow a
// link) and the wrapper is replaced only when the existing entry is absent or
// already a symlink — never over a regular file/dir. The symlink target is
// always a validated /usr/bin/php<version> that exists; never tenant-input.
package commands

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// userCLIPHPBinDir returns the per-user wrapper bin dir for a username.
func userCLIPHPBinDir(username string) string {
	return filepath.Join("/home", username, ".jabali", "bin")
}

// isRootOwned reports whether the stat info belongs to uid 0.
func isRootOwned(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0
}

// ensureRootDir makes `dir` a root-owned, symlink-free directory. If it
// already exists it must be a non-symlink dir owned by root, else we refuse
// (a tenant-planted symlink or tenant-owned dir is never followed/written
// into). Creates with Mkdir (not MkdirAll — the parent is verified by the
// caller) + Lchown to root.
func ensureRootDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refuse: %s is a symlink", dir)
		}
		if !fi.IsDir() {
			return fmt.Errorf("refuse: %s is not a directory", dir)
		}
		if isRootOwned(fi) {
			// Already ours. Enforce a safe mode in case a legacy/tenant state
			// left group/world-write bits — otherwise a tenant could keep
			// planting entries even though the dir is root-owned.
			if fi.Mode().Perm() != 0o755 {
				if cErr := os.Chmod(dir, 0o755); cErr != nil {
					return fmt.Errorf("harden mode %s: %w", dir, cErr)
				}
			}
			return nil
		}
		// Tenant-owned (legacy state from an older agent). Do NOT reclaim it in
		// place: both its MODE and its CONTENTS are tenant-controlled, so a bare
		// chown would leave a group/world-writable dir or tenant-planted entries
		// in a now-"root-owned" path. Instead rename the suspect dir ASIDE
		// within its (root-owned, tenant-unwritable) parent and discard it, then
		// recreate it empty + root-owned below. The caller rebuilds the wrapper
		// symlinks from scratch, so nothing of value is lost. The parent being
		// root-owned (verified by the caller for /home/<user>, and by the prior
		// ensureRootDir pass for .jabali) means a tenant cannot rename/swap the
		// suspect dir out from under us between the Lstat and the Rename.
		var rnd [8]byte
		if _, rErr := rand.Read(rnd[:]); rErr != nil {
			return fmt.Errorf("reclaim %s: rand: %w", dir, rErr)
		}
		aside := dir + ".reclaim-" + hex.EncodeToString(rnd[:])
		if rErr := os.Rename(dir, aside); rErr != nil {
			return fmt.Errorf("reclaim %s: rename aside: %w", dir, rErr)
		}
		// Best-effort discard. The suspect tree is already out of the live path;
		// leftover litter (root-owned) is harmless if removal hiccups.
		_ = os.RemoveAll(aside)
		// fall through to a fresh root-owned create
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if err := os.Lchown(dir, 0, 0); err != nil {
		return err
	}
	// Mkdir's mode is umask-masked; force the intended perms so the dir is never
	// group/world-writable regardless of the agent's umask.
	return os.Chmod(dir, 0o755)
}

// ensureUserCLIPHP writes/refreshes /home/<user>/.jabali/bin/php as a symlink
// to /usr/bin/php<version>. Idempotent and symlink-TOCTOU-safe (see file
// header). A user without a root-owned /home/<user> directory (no home, or a
// tenant-writable/symlinked home) is refused rather than written into.
func ensureUserCLIPHP(username, version string) error {
	if !phpPoolUsernameRegex.MatchString(username) {
		return fmt.Errorf("ensureUserCLIPHP: invalid username %q", username)
	}
	// GH #256: an explicit user CLI choice (set via the panel) overrides the
	// pool-derived version for the bare `php`. Resolution: choice file >
	// passed pool version > the user-phpver pin file. The versioned phpX.Y
	// wrappers below are created regardless.
	if ch := readUserCLIChoice(username); ch != "" {
		version = ch
	} else if version == "" {
		version = readUserPhpverPin(username)
	}
	// An empty version (e.g. choice cleared on a user with no pool pin) is
	// allowed: we still refresh the versioned phpX.Y wrappers below and leave
	// `php` untouched. But a NON-empty version must be valid + installed —
	// garbage input is a caller error.
	bareTarget := ""
	if version != "" {
		if !phpVersionRegex.MatchString(version) {
			return fmt.Errorf("ensureUserCLIPHP: invalid version %q", version)
		}
		bareTarget = "/usr/bin/php" + version
		if fi, err := os.Stat(bareTarget); err != nil || fi.IsDir() {
			return fmt.Errorf("ensureUserCLIPHP: target %s missing", bareTarget)
		}
	}

	home := filepath.Join("/home", username)
	hfi, err := os.Lstat(home)
	if err != nil {
		return nil // no home (system/service account) → nothing to wire
	}
	if hfi.Mode()&os.ModeSymlink != 0 || !hfi.IsDir() {
		// A symlinked or non-dir home is never safe to write under.
		return fmt.Errorf("ensureUserCLIPHP: %s is not a usable home directory", home)
	}

	// GH #256: PHP version is per-DOMAIN, but a user has a single `php` CLI.
	// A user with domains on different versions (8.3/8.4/8.5) can't get them
	// all from a bare `php`. Expose EVERY installed version as `php<X.Y>` in
	// the same on-PATH dir so they can select per project — `php8.3 composer
	// install`, `php8.5 -v` — while `php` stays their pinned default. Mirrors
	// cPanel's ea-phpNN wrappers. Shared by both write paths.
	versioned := installedPHPCLIVersions("/usr/bin")

	if !isRootOwned(hfi) {
		// GH #1332: a tenant-owned home (common on imported/migrated accounts, or
		// any host that provisions homes owned by the tenant) is the tenant's own
		// space. Writing under it AS ROOT is the symlink-TOCTOU risk the header
		// warns about, so the old code refused — and that refusal surfaced to the
		// user as a 502 when they changed their CLI version. Instead write the
		// wrappers AS THE TENANT: the kernel then enforces the tenant's own
		// permissions, so a planted symlink is followed as the tenant and can
		// only redirect writes to paths they may already write (i.e. only affects
		// themselves — no escalation).
		return ensureUserCLIPHPAsTenant(username, home, bareTarget, versioned)
	}

	// Root-owned home (the standard jabali provisioning): write as root with the
	// symlink-TOCTOU-safe helpers.
	jabaliDir := filepath.Join(home, ".jabali")
	if err := ensureRootDir(jabaliDir); err != nil {
		return err
	}
	binDir := filepath.Join(jabaliDir, "bin")
	if err := ensureRootDir(binDir); err != nil {
		return err
	}
	if bareTarget != "" {
		if err := replaceCLISymlink(filepath.Join(binDir, "php"), bareTarget); err != nil {
			return err
		}
	}
	// Best-effort per version (a bad one is skipped, never fails the pin above).
	for ver, src := range versioned {
		_ = replaceCLISymlink(filepath.Join(binDir, "php"+ver), src)
	}
	return nil
}

// ensureUserCLIPHPAsTenant writes the ~/.jabali/bin CLI wrappers for a
// tenant-OWNED home by dropping to the tenant's own uid/gid, so every mkdir and
// symlink runs with the tenant's privileges (GH #1332). A tenant already
// controls their home, so acting as them cannot escalate. The home MUST be
// owned by THIS user (a foreign owner is refused). bareTarget "" leaves `php`
// untouched; the versioned map mirrors the root path.
func ensureUserCLIPHPAsTenant(username, home, bareTarget string, versioned map[string]string) error {
	u, err := user.Lookup(username)
	if err != nil {
		return fmt.Errorf("ensureUserCLIPHP: lookup %s: %w", username, err)
	}
	uid64, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return fmt.Errorf("ensureUserCLIPHP: uid %q: %w", u.Uid, err)
	}
	gid64, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return fmt.Errorf("ensureUserCLIPHP: gid %q: %w", u.Gid, err)
	}
	// The home must actually be owned by THIS user before we write into it as
	// them — defends against a username/home-uid mismatch.
	hfi, err := os.Lstat(home)
	if err != nil {
		return err
	}
	if st, ok := hfi.Sys().(*syscall.Stat_t); !ok || uint64(st.Uid) != uid64 {
		return fmt.Errorf("ensureUserCLIPHP: %s is not owned by %s", home, username)
	}
	uid, gid := uint32(uid64), uint32(gid64)
	binDir := filepath.Join(home, ".jabali", "bin")
	if out, err := runFSAsUser(uid, gid, "mkdir", "-p", binDir); err != nil {
		return fmt.Errorf("ensureUserCLIPHP: mkdir %s as %s: %w (%s)", binDir, username, err, strings.TrimSpace(string(out)))
	}
	if bareTarget != "" {
		if out, err := runFSAsUser(uid, gid, "ln", "-sfn", bareTarget, filepath.Join(binDir, "php")); err != nil {
			return fmt.Errorf("ensureUserCLIPHP: link php as %s: %w (%s)", username, err, strings.TrimSpace(string(out)))
		}
	}
	// Best-effort per version (mirrors the root path): a bad one is skipped.
	for ver, src := range versioned {
		_, _ = runFSAsUser(uid, gid, "ln", "-sfn", src, filepath.Join(binDir, "php"+ver))
	}
	return nil
}

// runFSAsUser runs a coreutils FS command (mkdir/ln) as the given uid/gid via a
// setuid/setgid child, so the kernel enforces that user's permissions.
func runFSAsUser(uid, gid uint32, name string, args ...string) ([]byte, error) {
	bin, err := exec.LookPath(name)
	if err != nil {
		// The agent's PATH can be minimal (systemd) — fall back to /usr/bin.
		bin = filepath.Join("/usr/bin", name)
		if _, statErr := os.Stat(bin); statErr != nil {
			return nil, fmt.Errorf("%s not found: %w", name, err)
		}
	}
	cmd := execCommand(bin, args...) // GH #994 exec seam, not raw exec.Command
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: uid, Gid: gid, NoSetGroups: true},
	}
	return cmd.CombinedOutput()
}

// installedPHPCLIVersions scans srcDir for php<major>.<minor> CLI binaries
// (e.g. /usr/bin/php8.3) and returns version -> absolute path. Excludes the
// suffixed variants (php8.3-fpm) via the strict glob. Pure on srcDir so it is
// unit-testable without /usr/bin.
// userCLIChoiceRoot holds per-user explicit CLI default PHP version choices
// (GH #256). Separate from user-phpver (the pool-derived auto pin) so a user's
// explicit choice survives a domain version change. Overridable for tests.
func userCLIChoiceRoot() string {
	if r := os.Getenv("JABALI_PHP_CLI_CHOICE_ROOT"); r != "" {
		return r
	}
	return "/etc/jabali-panel/user-phpcli"
}

// readUserCLIChoice returns the user's explicit CLI PHP version ("8.3") or ""
// when unset/invalid.
func readUserCLIChoice(username string) string {
	b, err := os.ReadFile(filepath.Join(userCLIChoiceRoot(), username))
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(b))
	if !phpVersionRegex.MatchString(v) {
		return ""
	}
	return v
}

// readUserPhpverPin returns the pool-derived auto pin ("8.4") or "".
func readUserPhpverPin(username string) string {
	root := os.Getenv("JABALI_PHP_VER_PIN_ROOT")
	if root == "" {
		root = "/etc/jabali-panel/user-phpver"
	}
	b, err := os.ReadFile(filepath.Join(root, username))
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(b))
	if !phpVersionRegex.MatchString(v) {
		return ""
	}
	return v
}

func installedPHPCLIVersions(srcDir string) map[string]string {
	out := map[string]string{}
	matches, _ := filepath.Glob(filepath.Join(srcDir, "php[0-9].[0-9]"))
	for _, m := range matches {
		ver := strings.TrimPrefix(filepath.Base(m), "php")
		if !phpVersionRegex.MatchString(ver) {
			continue
		}
		if fi, err := os.Stat(m); err == nil && !fi.IsDir() {
			out[ver] = m
		}
	}
	return out
}

// replaceCLISymlink points `link` at `target` idempotently and safely: an
// existing entry that is NOT a symlink is refused (never overwrite a regular
// file/dir a tenant may have planted); a symlink already pointing at target
// is a no-op; otherwise it is replaced atomically (symlink to a temp name
// then rename over the live one).
func replaceCLISymlink(link, target string) error {
	if fi, err := os.Lstat(link); err == nil {
		if fi.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("refuse: %s exists and is not a symlink", link)
		}
		if cur, rerr := os.Readlink(link); rerr == nil && cur == target {
			return nil // already correct → no-op (no-change gate)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	tmp := link + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return fmt.Errorf("replaceCLISymlink: symlink: %w", err)
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replaceCLISymlink: rename: %w", err)
	}
	return nil
}

// removeUserCLIPHP removes the per-user CLI php wrapper (pool teardown).
// Removes only the `php` symlink (and only if it IS a symlink), leaving an
// empty .jabali/bin behind harmlessly.
func removeUserCLIPHP(username string) error {
	if !phpPoolUsernameRegex.MatchString(username) {
		return fmt.Errorf("removeUserCLIPHP: invalid username %q", username)
	}
	link := filepath.Join(userCLIPHPBinDir(username), "php")
	fi, err := os.Lstat(link)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("removeUserCLIPHP: %w", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("refuse: %s is not a symlink", link)
	}
	if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removeUserCLIPHP: %w", err)
	}
	return nil
}

// BackfillUserCLIPHP ensures a per-user CLI php wrapper for every user that
// already has a version pin, run once at agent startup. The reconciler only
// fires php.pool.apply for pending/error pools, so active pools (existing
// users) would otherwise never get a wrapper; a restart (e.g. `jabali
// update`) backfills them all. Best-effort: each user logged + skipped on
// error, never blocks boot.
func BackfillUserCLIPHP(log *slog.Logger) {
	root := os.Getenv("JABALI_PHP_VER_PIN_ROOT")
	if root == "" {
		root = "/etc/jabali-panel/user-phpver"
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return // no pins yet (fresh host) → nothing to backfill
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		username := e.Name()
		b, rerr := os.ReadFile(filepath.Join(root, username))
		if rerr != nil {
			continue
		}
		version := strings.TrimSpace(string(b))
		if err := ensureUserCLIPHP(username, version); err != nil && log != nil {
			log.Warn("backfill per-user CLI php wrapper", "user", username, "version", version, "err", err)
		}
	}
}
