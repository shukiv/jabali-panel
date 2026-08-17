// migration.import_home — copy an extracted cpmove home tree into a
// destination jabali user's /home/<user>/. M35 cPanel restore stage.
//
// Source path: /var/lib/jabali-migrations/<job-id>/extracted/cp/
//
//	<source-user>/homedir/
//
// Destination: /home/<target-user>/
//
// Mechanics:
//   - rsync -aH (preserve perms/links/hardlinks) with delete-after on
//     the destination so resume after partial failure converges
//   - chown -R <target>:<target> after rsync (cpmove tarballs preserve
//     source-side ownership which would point at numeric uids on the
//     source host)
//   - excludes .ssh/ (panel-side ssh_keys table is the truth; reconciler
//     materialises authorized_keys), .cpanel/ (cPanel control files
//     meaningless on jabali), .htpasswd files (operator re-creates),
//     and standard backup-noise (.lock, .DS_Store, etc.)
//   - 4-hour timeout per call; mid-call cancellation kills rsync
//
// SECURITY: only runs as root (agent is privileged). The dest_user
// argument is validated against /etc/passwd via getent before any
// rsync runs — refuses to write to a path that doesn't belong to a
// real user. The src argument MUST be inside /var/lib/jabali-
// migrations/ (path-prefix check). Both checks fail-closed with
// agentwire.CodeInvalidArgument.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
)

const (
	migrationStagingRoot = "/var/lib/jabali-migrations"
	migrationHomeTimeout = 4 * time.Hour
)

// migrationStagingRoots — every staging area the agent accepts src paths under.
// The panel builds staging under <working_folder>/migrations, default
// /var/lib/jabali/migrations (GH #327 — the config-driven working-folder scheme
// replaced the flat legacy path). The legacy /var/lib/jabali-migrations stays
// for pre-schema-bump installs (install.sh symlinks it). NOTE: a non-default
// working_folder (e.g. /mnt/storage) is not covered here yet — the panel would
// need to pass the resolved root.
var migrationStagingRoots = []string{
	"/var/lib/jabali-migrations",
	"/var/lib/jabali/migrations",
}

// canonicalizeStagingRootPrefix rewrites ONLY the /var/lib/jabali-migrations
// compat-symlink ROOT prefix of a staging path to its real target
// (/var/lib/jabali/migrations, GH #327), leaving the remainder untouched.
//
// filesafe opens files with openat2 RESOLVE_BENEATH, which refuses to traverse
// ANY symlink component — so a path whose root prefix is the compat symlink
// fails every open with ENOTDIR ("not a directory"), silently importing nothing
// (GH #530). We resolve ONLY the known system-owned root symlink; the tenant-
// extractable remainder is passed through verbatim so RESOLVE_BENEATH still
// governs every component of it atomically — a symlink planted in the staged
// tree is still rejected, and the anti-traversal protection stays intact.
// No-op when the path isn't under the legacy root, or the root isn't a symlink.
func canonicalizeStagingRootPrefix(path string) string {
	return canonicalizeRootPrefix(path, migrationStagingRoot)
}

// canonicalizeRootPrefix is the testable core: it EvalSymlinks-resolves ONLY
// legacyRoot (a system-owned path) and swaps it for the head of `path`,
// preserving the remainder byte-for-byte. It never calls EvalSymlinks on the
// remainder, so a symlink there survives into the returned path and is left for
// the caller's RESOLVE_BENEATH open to reject.
func canonicalizeRootPrefix(path, legacyRoot string) string {
	if path != legacyRoot && !strings.HasPrefix(path, legacyRoot+"/") {
		return path
	}
	real, err := filepath.EvalSymlinks(legacyRoot)
	if err != nil || real == "" || real == legacyRoot {
		return path
	}
	return real + strings.TrimPrefix(path, legacyRoot)
}

// migrationHomeExcludes is the rsync --exclude pattern set. .ssh/ is
// excluded because the panel's ssh_keys table is the truth on
// authorized_keys (reconciler writes); .cpanel/ is cPanel-only
// metadata; standard noise files round it out.
var migrationHomeExcludes = []string{
	".ssh/",
	".cpanel/",
	".htpasswd",
	".lock",
	".DS_Store",
	"public_html/.well-known/acme-challenge/",
	"tmp/",
}

type migrationImportHomeParams struct {
	JobID    string `json:"job_id"`
	SrcDir   string `json:"src_dir"`   // absolute, must live under migrationStagingRoot
	DestUser string `json:"dest_user"` // resolved via getent — must exist + have a home
	// DestSubpath — optional. When set, rsync lands at
	// <homedir>/<DestSubpath>/ instead of <homedir>/ directly. The
	// agent mkdir -p's the path + chown's it to the dest user
	// before rsync. Used by M35.8 per-domain docroot split so
	// /home/<user>/domains/<dom>/public_html/ gets the right files.
	DestSubpath string `json:"dest_subpath,omitempty"`
	// ExtraExcludes — optional rsync --exclude additions on top of
	// the built-in migrationHomeExcludes set. Used for nested-
	// domain exclusion ("don't copy the addon's public_html when
	// rsyncing the parent's").
	ExtraExcludes []string `json:"extra_excludes,omitempty"`
}

type migrationImportHomeResult struct {
	BytesCopied int64    `json:"bytes_copied"`
	Files       int64    `json:"files"`
	DestPath    string   `json:"dest_path"`
	Skipped     []string `json:"skipped,omitempty"`
}

func init() {
	Default.Register("migration.import_home", migrationImportHomeHandler)
}

func migrationImportHomeHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p migrationImportHomeParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInvalidArgument, Message: "malformed JSON: " + err.Error(),
		}
	}
	if p.JobID == "" || p.SrcDir == "" || p.DestUser == "" {
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInvalidArgument, Message: "job_id, src_dir, dest_user required",
		}
	}

	// Path validation — resolve src_dir symlink-safe under the staging root
	// (Gitea #468). A bare strings.HasPrefix check is not symlink-aware: a
	// symlink inside the staging tree could point rsync at arbitrary host
	// files to copy into the tenant home. filesafe.Scope.Resolve rejects any
	// path outside /var/lib/jabali-migrations and fails closed on a symlinked
	// component.
	srcScope, sErr := filesafe.NewScope(p.JobID, "migration", migrationStagingRoots)
	if sErr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "src scope init: " + sErr.Error()}
	}
	srcAbs, err := srcScope.Resolve(p.SrcDir)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("src_dir must resolve under %s: %v", strings.Join(migrationStagingRoots, " or "), err),
		}
	}

	// Resolve dest user via getent (system-truth). Refuse if the
	// user has no entry — restore-stage runner is responsible for
	// CreateUser before calling this.
	u, err := user.Lookup(p.DestUser)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("dest_user %q not found in /etc/passwd: %v", p.DestUser, err),
		}
	}
	if u.HomeDir == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("dest_user %q has no homedir", p.DestUser),
		}
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("getent returned non-numeric uid %q for %q", u.Uid, p.DestUser),
		}
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("getent returned non-numeric gid %q for %q", u.Gid, p.DestUser),
		}
	}

	subctx, cancel := context.WithTimeout(ctx, migrationHomeTimeout)
	defer cancel()

	// Build rsync argv. Trailing slash on source = copy contents
	// not the directory itself.
	// --no-h forces raw byte counts in the stats2 summary.
	// Without it rsync 3.x prints "Total transferred file size: 752.0M"
	// which parseRsyncStats can't parse as int → byte count
	// silently zeros (QA flagged: 752 MB restored, manifest
	// reported bytes=0).
	// --delete-after with a constrained scope would wipe content the
	// previous per-domain rsync wrote. Drop --delete-after when a
	// DestSubpath is supplied (per-domain mode) so multiple calls
	// accumulate cleanly without trashing each other's output.
	rsyncFlags := "-aH"
	// --partial keeps a partially-transferred file on interruption (instead
	// of discarding it); --append-verify resumes that file from where it
	// stopped and then checksums the whole result. So a transfer that dies
	// mid-large-file (flaky long-haul link, 50 GB account) resumes that one
	// file instead of restarting it. Safe for migration because the source
	// is a static cpmove snapshot during the window, and append-verify's
	// full-file checksum self-corrects a bad/truncated partial (mismatch ->
	// rsync re-sends the file).
	args := []string{rsyncFlags, "--partial", "--append-verify", "--no-h", "--info=stats2"}
	if p.DestSubpath == "" {
		args = append(args, "--delete-after")
	}
	for _, ex := range migrationHomeExcludes {
		args = append(args, "--exclude="+ex)
	}
	for _, ex := range p.ExtraExcludes {
		if strings.TrimSpace(ex) == "" {
			continue
		}
		args = append(args, "--exclude="+ex)
	}
	// Bind + symlink-safe resolve the destination to the dest user's own
	// home (Gitea #468). os.MkdirAll + chown -R against a string path would
	// follow a symlinked home component and redirect the root write/own
	// outside the account tree.
	destScope, dsErr := filesafe.NewScope(p.DestUser, p.DestUser, []string{u.HomeDir})
	if dsErr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "dest scope init: " + dsErr.Error()}
	}
	dest := u.HomeDir + "/"
	if p.DestSubpath != "" {
		// Defense: refuse absolute or escape paths.
		clean := strings.TrimLeft(filepath.Clean(p.DestSubpath), "/")
		if clean == "" || strings.Contains(clean, "..") {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: fmt.Sprintf("dest_subpath invalid: %q", p.DestSubpath),
			}
		}
		resolvedDest, rErr := destScope.Resolve(filepath.Join(u.HomeDir, clean))
		if rErr != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: fmt.Sprintf("dest_subpath %q must resolve under %s: %v", p.DestSubpath, u.HomeDir, rErr),
			}
		}
		// Create the subpath escape-proof (openat2), then chown it.
		if _, mkErr := destScope.MkdirInScope(resolvedDest, true, 0o755); mkErr != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("mkdir %s: %v", resolvedDest, mkErr),
			}
		}
		dest = resolvedDest + "/"
		// Symlink-safe chown (Gitea #497): Walk+Lchown stays inside the tree,
		// never following a symlink the source tarball may have planted.
		_ = chownTreeRecursive(dest, uid, gid)
	}
	srcWithSlash := strings.TrimRight(srcAbs, "/") + "/"
	args = append(args, srcWithSlash, dest)

	cmd := execCommandContext(subctx, "rsync", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("rsync failed: %v: %s", err, truncate(string(out), 4096)),
		}
	}

	bytesCopied, files := parseRsyncStats(string(out))

	// Chown the entire destination tree so files land owned by the
	// destination user EVEN though the source tarball preserved
	// source-side numeric uids. Group = www-data (not the user's
	// own primary group) so nginx (running as www-data) can
	// traverse + read docroot content. The cpanel rsync inherited
	// source-side group=<user>, which on jabali means nginx hits
	// 403 on every site request (`drwxr-x---` with group=user
	// excludes www-data). Same convention user.create already
	// applies to a fresh /home/<user>/.
	wwwData, lookupErr := user.LookupGroup("www-data")
	groupID := gid // fallback: keep the user's primary group
	if lookupErr == nil {
		if g, perr := strconv.Atoi(wwwData.Gid); perr == nil {
			groupID = g
		}
	}
	chownTarget := dest
	if p.DestSubpath == "" {
		chownTarget = u.HomeDir
	}
	// Symlink-safe recursive chown (Gitea #497): the rsync'd tree is
	// source-tarball-controlled, so use Walk+Lchown (never `chown -R`, which
	// follows symlinks out of the tree).
	if cerr := chownTreeRecursive(chownTarget, uid, groupID); cerr != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("chown failed: %v", cerr),
		}
	}
	// Apply group-rx on directories + group-r on files so nginx
	// (in the www-data group) can traverse + read. Same trick the
	// install.sh user-create script + reconciler use on fresh
	// /home/<user>/ trees.
	_ = execCommandContext(subctx, "find", chownTarget, "-type", "d", "-exec", "chmod", "g+rx", "{}", "+").Run()
	_ = execCommandContext(subctx, "find", chownTarget, "-type", "f", "-exec", "chmod", "g+r", "{}", "+").Run()

	return migrationImportHomeResult{
		BytesCopied: bytesCopied,
		Files:       files,
		DestPath:    u.HomeDir,
	}, nil
}

// parseRsyncStats pulls byte + file counts from `rsync --info=stats2`
// stderr. Returns zeros when stats lines aren't present (very small
// transfers may skip the stats block).
func parseRsyncStats(stats string) (bytes, files int64) {
	for _, line := range strings.Split(stats, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Number of regular files transferred:"):
			fields := strings.Fields(line)
			if len(fields) > 0 {
				cleaned := strings.ReplaceAll(fields[len(fields)-1], ",", "")
				if v, err := strconv.ParseInt(cleaned, 10, 64); err == nil {
					files = v
				}
			}
		case strings.HasPrefix(line, "Total transferred file size:"):
			fields := strings.Fields(line)
			if len(fields) >= 5 {
				cleaned := strings.ReplaceAll(fields[4], ",", "")
				if v, err := strconv.ParseInt(cleaned, 10, 64); err == nil {
					bytes = v
				}
			}
		}
	}
	return bytes, files
}

// truncate returns at most n bytes of s. Used to bound the size of
// rsync's stderr inside an AgentError so a stuck rsync producing
// MB of warnings doesn't blow up the JSON envelope.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...[truncated]"
}
