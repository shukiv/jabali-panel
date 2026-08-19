package commands

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
)

// files.archive — build a tar.gz of the given scoped paths and write it
// to the shared staging dir so panel-api (same host, jabali user) can
// stream it out as a download. The archive is chowned to jabali:jabali +
// mode 0600 so only the panel process can read it; panel-api unlinks
// after streaming.
//
// GH #756: the archive was written to /tmp, but both jabali-agent and
// panel-api run PrivateTmp=yes (install.sh) — /tmp is a per-service mount
// namespace, so a tarball the agent writes to /tmp is INVISIBLE to
// panel-api, which then fails os.Open with "no such file or directory"
// (not a perms error — chownToPanelUser sets the owner fine, the path
// just doesn't exist in panel-api's private /tmp). Stage it under
// /var/lib/jabali-uploads instead: it sits OUTSIDE the tmp sandbox and is
// in panel-api's ReadWritePaths + AppArmor profile, so both units see the
// same on-disk path (same fix as the upload-staging dir — install.sh
// "Upload staging dir" note).
//
// Paths are resolved through the filesafe scope — a client cannot ask
// for /etc/passwd via the archive endpoint any more than via read().

const (
	// Panel-api's service user + a conservative cap matching the
	// upload cap — consistency with the rest of the files.* surface
	// matters more than eking out extra GB here.
	archiveOwnerUser  = "jabali"
	archiveOwnerGroup = "jabali"
	maxArchiveBytes   = int64(500 * 1024 * 1024)
	// GH #675: cap UNCOMPRESSED bytes read from the tenant tree too — a highly
	// compressible tree can blow far past the 500 MiB compressed cap on input.
	maxArchiveInputBytes = int64(4 * 1024 * 1024 * 1024)
)

// archiveStagingRoot is where the scratch tarball is written. It MUST live
// OUTSIDE the PrivateTmp sandbox so panel-api can read it back (GH #756 — see
// the handler doc above). /var/lib/jabali-uploads is created by install.sh
// (0750 jabali) and is in panel-api's ReadWritePaths + AppArmor profile. A var,
// not a const, so tests can redirect it to a t.TempDir().
var archiveStagingRoot = "/var/lib/jabali-uploads"

// ensureArchiveStagingRoot makes sure the staging dir exists and is owned by
// the panel user so panel-api can traverse it. Best-effort: install.sh already
// provisions it for the upload feature, so on a real host this is a no-op.
func ensureArchiveStagingRoot() error {
	if err := os.MkdirAll(archiveStagingRoot, 0o750); err != nil {
		return fmt.Errorf("ensure archive staging root %s: %w", archiveStagingRoot, err)
	}
	// If we just created it as root, hand it to the panel user; if it already
	// exists with the right owner this is a harmless re-chown. Ignore errors on
	// hosts without the jabali user (tests) — the temp-dir override skips this.
	_ = chownToPanelUser(archiveStagingRoot)
	return nil
}

type filesArchiveParams struct {
	UserID    string   `json:"user_id"`
	Username  string   `json:"username"`
	AdminRoot bool     `json:"admin_root"` // GH #1184 admin FM
	Paths     []string `json:"paths"`
}

type filesArchiveResponse struct {
	ArchivePath string `json:"archive_path"`
	Size        int64  `json:"size"`
}

// Per-user archive concurrency cap (GH #652). Archive generation writes a
// server-side scratch file, so cap in-flight requests per user to prevent a
// tenant from exhausting /tmp/agent fds.
const maxArchivesPerUser = 2

// maxArchiveStagingBytes (GH #652) bounds the TOTAL size of all in-flight
// archive scratch tarballs across ALL users, so concurrent archives from many
// tenants can't fill /tmp. Per-user is already bounded by maxArchivesPerUser ×
// maxArchiveBytes; this is the shared-staging ceiling on top of that.
const maxArchiveStagingBytes = int64(6 * 1024 * 1024 * 1024)

// reapStaleArchives (GH #652) removes orphaned scratch tarballs left by a
// crashed / disconnected / timed-out request. The archive op is bounded to 5m
// (GH #675), so anything older than 15m is definitely abandoned — deleting it
// frees the shared budget so live requests aren't starved by dead files.
func reapStaleArchives() {
	matches, _ := filepath.Glob(filepath.Join(archiveStagingRoot, "jabali-archive-*.tar.gz"))
	cutoff := 15 * time.Minute
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil {
			if time.Since(fi.ModTime()) > cutoff {
				_ = os.Remove(m)
			}
		}
	}
}

// archiveStagingBytesInUse sums the current jabali-archive-* scratch files.
func archiveStagingBytesInUse() int64 {
	matches, _ := filepath.Glob(filepath.Join(archiveStagingRoot, "jabali-archive-*.tar.gz"))
	var total int64
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil {
			total += fi.Size()
		}
	}
	return total
}

var (
	archiveInFlightMu sync.Mutex
	archiveInFlight   = map[string]int{}
	// archiveReservedBytes (GH #652) tracks bytes RESERVED by in-flight archives
	// (each reserves the per-archive max up-front). Guarded by archiveInFlightMu.
	// Reservation is race-safe where a bare pre-check is not: two concurrent
	// requests can't both pass, because each atomically adds its reservation to
	// the on-disk total under the lock before any bytes are written.
	archiveReservedBytes int64
)

// reserveArchiveBudget atomically reserves the per-archive max against the shared
// staging budget (on-disk + already-reserved). Returns false if it would exceed.
func reserveArchiveBudget() bool {
	archiveInFlightMu.Lock()
	defer archiveInFlightMu.Unlock()
	if archiveStagingBytesInUse()+archiveReservedBytes+maxArchiveBytes > maxArchiveStagingBytes {
		return false
	}
	archiveReservedBytes += maxArchiveBytes
	return true
}

func releaseArchiveBudget() {
	archiveInFlightMu.Lock()
	if archiveReservedBytes >= maxArchiveBytes {
		archiveReservedBytes -= maxArchiveBytes
	} else {
		archiveReservedBytes = 0
	}
	archiveInFlightMu.Unlock()
}

func acquireArchiveSlot(username string) bool {
	archiveInFlightMu.Lock()
	defer archiveInFlightMu.Unlock()
	if archiveInFlight[username] >= maxArchivesPerUser {
		return false
	}
	archiveInFlight[username]++
	return true
}

func releaseArchiveSlot(username string) {
	archiveInFlightMu.Lock()
	defer archiveInFlightMu.Unlock()
	if archiveInFlight[username] > 0 {
		archiveInFlight[username]--
		if archiveInFlight[username] == 0 {
			delete(archiveInFlight, username)
		}
	}
}

func filesArchiveHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p filesArchiveParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	if p.Username == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "username required"}
	}
	// GH #652: bound concurrent archive generation per user — each request
	// writes a server-side scratch tarball, so unbounded concurrency lets one
	// tenant spawn many jabali-archive-* scratch files and exhaust the agent/disk.
	if !acquireArchiveSlot(p.Username) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: "too many concurrent archive requests for this user; retry shortly"}
	}
	defer releaseArchiveSlot(p.Username)
	// GH #675: bound total runtime so a pathological tree cannot pin the agent.
	ctx, cancelArchive := context.WithTimeout(ctx, 5*time.Minute)
	defer cancelArchive()
	if len(p.Paths) == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "at least one path required"}
	}

	scope, err := fileScopeFor(p.UserID, p.Username, p.AdminRoot)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to create scope: %v", err),
		}
	}

	// Resolve every path up-front so a bad input fails before we
	// create any scratch file.
	resolved := make([]string, 0, len(p.Paths))
	for _, raw := range p.Paths {
		r, err := scope.Resolve(raw)
		if err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: fmt.Sprintf("path validation failed for %q: %v", raw, err),
			}
		}
		resolved = append(resolved, r)
	}

	// Random suffix avoids collisions if two archive requests fire
	// in the same microsecond; 8 bytes hex is plenty.
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("rand: %v", err),
		}
	}
	// GH #652: reap abandoned scratch tarballs first, then reject if the shared
	// staging area is still at budget (prevents many tenants' concurrent
	// archives, or a pile of orphans, from filling /tmp).
	reapStaleArchives()
	// GH #652: reserve the shared budget atomically (race-safe: concurrent
	// requests can't all pass a bare pre-check then collectively overflow /tmp).
	if !reserveArchiveBudget() {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeUnavailable,
			Message: "archive staging area is at capacity; retry shortly",
		}
	}
	defer releaseArchiveBudget() // release the reservation once done (file is now on-disk-accounted)
	// GH #756: stage under the shared, non-PrivateTmp dir so panel-api can read
	// the result back (a /tmp path is invisible across the PrivateTmp boundary).
	if err := ensureArchiveStagingRoot(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	out := filepath.Join(archiveStagingRoot, fmt.Sprintf("jabali-archive-%s.tar.gz", hex.EncodeToString(rnd[:])))

	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("create archive: %v", err),
		}
	}
	// If anything below fails we don't want an orphan scratch file.
	cleanup := func() { _ = os.Remove(out) }

	// Cap the underlying file writer so a runaway archive can't fill the
	// staging disk — panel-api runs as a system service and we don't want
	// to be the reason the host goes read-only.
	cw := &capWriter{w: f, limit: maxArchiveBytes}
	gz := gzip.NewWriter(cw)
	// GH #675: the input cap sits ABOVE gzip so it measures the uncompressed
	// bytes tar writes (file bodies + headers), independent of compression.
	inputCap := &capWriter{w: gz, limit: maxArchiveInputBytes}
	tw := tar.NewWriter(inputCap)

	for _, path := range resolved {
		if err := addToTar(ctx, tw, scope, path, filepath.Dir(path)); err != nil {
			tw.Close()
			gz.Close()
			f.Close()
			cleanup()
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		gz.Close()
		f.Close()
		cleanup()
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("tar close: %v", err)}
	}
	if err := gz.Close(); err != nil {
		f.Close()
		cleanup()
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("gzip close: %v", err)}
	}
	if err := f.Close(); err != nil {
		cleanup()
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("close: %v", err)}
	}

	info, err := os.Stat(out)
	if err != nil {
		cleanup()
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("stat: %v", err)}
	}

	// Chown to the panel-api user so it can read+unlink. Best effort:
	// if the host isn't set up with the expected user (e.g. tests),
	// we leave the file root-owned and the caller will 500.
	if err := chownToPanelUser(out); err != nil {
		// Soft warning — the file exists and the API can still try to
		// read it under its own CAP_DAC_OVERRIDE (it has none), but
		// we surface the error so operators see the misconfig.
		cleanup()
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("chown archive: %v", err),
		}
	}

	return &filesArchiveResponse{
		ArchivePath: out,
		Size:        info.Size(),
	}, nil
}

// addToTar walks `src` recursively and writes every entry into `tw`,
// with paths rebased to be relative to `baseDir`. Symlinks are stored
// as-is (tar's Typeflag=TypeSymlink) — we don't chase them. Regular-file
// bodies are opened through the filesafe scope (openat2 RESOLVE_BENEATH),
// so a tenant cannot race a checked file into a symlink between the walk's
// Lstat and the body read to disclose host files (Gitea #471).
// addToTar walks `src` recursively and writes every entry into `tw` using ONLY
// fd-safe scope primitives (GH #650): StatInScope/ReadDirInScope/ReadlinkInScope
// (each opens the parent escape-proof and *at(2)s the leaf with no symlink
// follow) and OpenInScope for regular-file bodies. This replaces filepath.Walk,
// whose os.ReadDir follows a directory that a tenant swapped to a symlink after
// validation — leaking out-of-scope names/modes/sizes into the tar headers.
func addToTar(ctx context.Context, tw *tar.Writer, scope *filesafe.Scope, src, baseDir string) error {
	li, err := scope.StatInScope(src)
	if err != nil {
		return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("stat %q: %v", src, err)}
	}
	rel, err := filepath.Rel(baseDir, src)
	if err != nil {
		return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("rel %q: %v", src, err)}
	}
	return archiveEntry(ctx, tw, scope, src, rel, *li)
}

// archiveEntry writes one entry (and, for a directory, its descendants) fd-safely.
func archiveEntry(ctx context.Context, tw *tar.Writer, scope *filesafe.Scope, path, rel string, li filesafe.LeafInfo) error {
	// GH #664: stop promptly on cancellation.
	if err := ctx.Err(); err != nil {
		return err
	}
	hdr := &tar.Header{Name: rel, Mode: int64(li.Mode.Perm()), ModTime: li.ModTime}

	switch {
	case li.IsSymlink:
		// Record the symlink as a header only — NEVER stream its target's
		// bytes (a swapped/escaping symlink cannot disclose host files).
		target, terr := scope.ReadlinkInScope(path)
		if terr != nil {
			return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("readlink %q: %v", path, terr)}
		}
		hdr.Typeflag = tar.TypeSymlink
		hdr.Linkname = target
		return tw.WriteHeader(hdr)

	case li.IsDir:
		hdr.Typeflag = tar.TypeDir
		if !strings.HasSuffix(hdr.Name, "/") {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		// ReadDirInScope opens the dir escape-proof (openat2 RESOLVE_BENEATH),
		// so a dir swapped to a symlink is refused, not followed.
		entries, rerr := scope.ReadDirInScope(path)
		if rerr != nil {
			return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("readdir %q: %v", path, rerr)}
		}
		for _, e := range entries {
			if err := archiveEntry(ctx, tw, scope, filepath.Join(path, e.Name), filepath.Join(rel, e.Name), e); err != nil {
				return err
			}
		}
		return nil

	case li.Mode.IsRegular():
		hdr.Typeflag = tar.TypeReg
		hdr.Size = li.Size
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		rf, oerr := scope.OpenInScope(path, os.O_RDONLY, 0)
		if oerr != nil {
			return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("open %q: %v", path, oerr)}
		}
		// Re-confirm regular via the fd (a swap to a fifo/device after stat).
		if fi, fstatErr := rf.Stat(); fstatErr != nil || !fi.Mode().IsRegular() {
			_ = rf.Close()
			return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("not a regular file after open: %q", path)}
		}
		// Write EXACTLY hdr.Size bytes so the tar entry stays valid even if the
		// file grew/shrank after stat (GH #655: explicit per-entry close).
		n, cErr := io.Copy(tw, io.LimitReader(rf, li.Size))
		_ = rf.Close()
		if cErr != nil {
			return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("copy %q: %v", path, cErr)}
		}
		if n < li.Size {
			if _, err := tw.Write(make([]byte, li.Size-n)); err != nil {
				return err
			}
		}
		return nil

	default:
		// device / fifo / socket: metadata-only; skip (never archive special files).
		return nil
	}
}

// capWriter caps a writer at `limit` total bytes. Used defensively to
// refuse runaway archives — panel-api runs as a system service and a
// 50 GB tarball could fill /tmp.
type capWriter struct {
	w     io.Writer
	limit int64
	n     int64
}

func (c *capWriter) Write(p []byte) (int, error) {
	if c.n+int64(len(p)) > c.limit {
		return 0, fmt.Errorf("archive size exceeds %d bytes", c.limit)
	}
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// chownToHostingUser sets ownership to <username>:www-data, matching
// the M9.5 homedir ownership contract. Used by files.ingest after
// moving a chunked-upload scratch file into the user's scope so it's
// accessible both to the user (for their own FPM) and to nginx (via
// the www-data group).
func chownToHostingUser(path, username string) error {
	u, err := user.Lookup(username)
	if err != nil {
		return fmt.Errorf("lookup %s: %w", username, err)
	}
	g, err := user.LookupGroup("www-data")
	if err != nil {
		return fmt.Errorf("lookup group www-data: %w", err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return fmt.Errorf("parse uid: %w", err)
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return fmt.Errorf("parse gid: %w", err)
	}
	return os.Chown(path, uid, gid)
}

// chownToPanelUser looks up jabali:jabali via name and chowns the
// file. The archive must be readable by panel-api (which runs as the
// jabali service user); leaving it root-owned would force the API to
// either fall back to a CAP_DAC_* check it doesn't have or read via
// a bounce through the agent — both uglier.
func chownToPanelUser(path string) error {
	u, err := user.Lookup(archiveOwnerUser)
	if err != nil {
		return fmt.Errorf("lookup %s: %w", archiveOwnerUser, err)
	}
	g, err := user.LookupGroup(archiveOwnerGroup)
	if err != nil {
		return fmt.Errorf("lookup group %s: %w", archiveOwnerGroup, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return fmt.Errorf("parse uid: %w", err)
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return fmt.Errorf("parse gid: %w", err)
	}
	return os.Chown(path, uid, gid)
}

func init() {
	Default.Register("files.archive", filesArchiveHandler)
}
