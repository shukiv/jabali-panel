package commands

// migration.rsync_remote_home — rsync source:<src_path>/ → <dest_path>/
// via sshpass / ssh-key. Run by the restore stage once per domain in
// the BackupUser-emitted domains-paths.txt manifest. Replaces the
// "bundle home into cpmove tar then ImportHomeSplit" path so transfer
// resumes on transient failures + skips already-synced files.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
)

type migrationRsyncRemoteHomeParams struct {
	JobID      string `json:"job_id"`
	Host       string `json:"host"`
	SSHUser    string `json:"ssh_user"`
	SecretPath string `json:"secret_path"` // /etc/jabali-panel/migration-secrets/<job>.env
	SrcPath    string `json:"src_path"`    // absolute source path, e.g. /home/<acct>/domains/<dom>/public_html
	SrcAccount string `json:"src_account"` // source cPanel/DA account; src_path must live under /home/<src_account>/ (JAB-45)
	SrcRoot    string `json:"src_root"`    // GH #746: caller-provided allowed source root (Plesk: /var/www/vhosts, not under /home). Empty → legacy /home/<src_account> guard.
	DestPath   string `json:"dest_path"`   // absolute dest path on this host
	DestUser   string `json:"dest_user"`   // chown target after rsync
	Port       int    `json:"port"`        // source SSH port; 0 → 22 (GH #429)
	// Excludes are additional rsync --exclude patterns, RELATIVE to the
	// source path (GH #429 follow-up: the Plesk vhost-extra pass excludes
	// every already-rsynced docroot + Plesk system dirs). Validated:
	// relative, no traversal. Excludes can only SHRINK what copies.
	Excludes []string `json:"excludes,omitempty"`
}

type migrationRsyncRemoteHomeResult struct {
	BytesCopied int64    `json:"bytes_copied"`
	Files       int64    `json:"files"`
	DestPath    string   `json:"dest_path"`
	Skipped     []string `json:"skipped,omitempty"`

	// Partial is set when rsync ended with exit 23 (some files could not be
	// read or transferred) or 24 (source files vanished mid-copy, e.g. a
	// live Maildir). Everything else was copied and chowned; Errors holds
	// rsync's own lines naming what was left behind.
	Partial  bool     `json:"partial,omitempty"`
	ExitCode int      `json:"exit_code,omitempty"`
	Errors   []string `json:"errors,omitempty"`
}

func init() {
	Default.Register("migration.rsync_remote_home", migrationRsyncRemoteHomeHandler)
}

// resolveRsyncSrcRoot returns the absolute source root the rsync src_path must
// live under, or a non-empty error message. A caller-provided srcRoot (GH #746:
// Plesk passes /var/www/vhosts, whose docroots are NOT under /home) is honored,
// failing closed on traversal or a root of "/"; otherwise the JAB-45 legacy
// /home/<srcAccount> scope applies (cPanel/DA/Hestia/CyberPanel). The panel
// already scoped SrcPath to the subscription's real docroots — this is
// defence-in-depth against a tampered manifest pulling arbitrary paths.
func resolveRsyncSrcRoot(srcRoot, srcAccount string) (string, string) {
	if srcRoot != "" {
		if strings.Contains(srcRoot, "..") {
			return "", "src_root must not contain .."
		}
		clean := filepath.Clean(srcRoot)
		if clean == "/" || clean == "." {
			return "", "src_root resolves to filesystem root"
		}
		return clean, ""
	}
	if srcAccount == "" || strings.Contains(srcAccount, "/") || strings.Contains(srcAccount, "..") {
		return "", "src_account required and must be a bare account name (no / or ..)"
	}
	clean := filepath.Clean("/home/" + srcAccount)
	if clean == "/home" || !strings.HasPrefix(clean, "/home/") {
		return "", "src_account resolves outside /home"
	}
	return clean, ""
}

func migrationRsyncRemoteHomeHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p migrationRsyncRemoteHomeParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument,
			Message: "malformed JSON: " + err.Error()}
	}
	if p.JobID == "" || p.Host == "" || p.SecretPath == "" || p.SrcPath == "" || p.DestPath == "" || p.DestUser == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument,
			Message: "job_id, host, secret_path, src_path, dest_path, dest_user required"}
	}
	if !looksLikeUnixUsername(p.DestUser) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument,
			Message: "invalid dest_user"}
	}
	// JAB-45: validate the UNTRUSTED remote source path first — scope it to the
	// source account home (/home/<src_account>), not just /home/, so a tampered
	// manifest can't pull another source tenant's files (the SSH login is usually
	// root/admin). filepath.Clean collapses any ../ traversal.
	// Reject path separators + ".." traversal, but ALLOW dots: CyberPanel's
	// account home is /home/<domain> (e.g. /home/smoke.example.com), so a bare
	// no-dots rule wrongly refused every CyberPanel home rsync. filepath.Clean
	// on srcRoot below + the srcRoot-prefix check keep this traversal-safe.
	srcRoot, rootErr := resolveRsyncSrcRoot(p.SrcRoot, p.SrcAccount)
	if rootErr != "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: rootErr}
	}
	cleanSrc := filepath.Clean(p.SrcPath)
	if cleanSrc != srcRoot && !strings.HasPrefix(cleanSrc, srcRoot+"/") {
		// Keep the legacy /home/<src_account> wording for the JAB-45 path
		// (existing callers + tests key on it); name the actual root for the
		// caller-provided src_root case (GH #746).
		msg := "src_path must be under /home/<src_account> on source"
		if p.SrcRoot != "" {
			msg = "src_path must be under the allowed source root (" + srcRoot + ")"
		}
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: msg}
	}
	p.SrcPath = cleanSrc
	// A root session must not read the source as root. Root follows a symlink
	// the account planted in its own tree (a symlinked mail/ or domains/), so
	// the copy would pull files only root can read on the source into the new
	// account. The source-side rsync runs as the owner of the account's top
	// directory instead, which the account cannot replace; it then reads
	// only what the account itself can read. A non-root login already is
	// that boundary.
	rsyncPathOpt, rpErr := sourceRsyncPathOpt(p.SSHUser, srcRoot, p.SrcRoot != "", p.SrcPath)
	if rpErr != "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: rpErr}
	}
	// Bind the destination to the destination user's own home and resolve it
	// symlink-safe (Gitea #465). A bare strings.HasPrefix(dest, "/home/") let a
	// call pair dest_user=alice with dest_path=/home/bob/... — root would then
	// rsync into and chown -R bob's tree to alice. filesafe.Scope.Resolve
	// rejects any path outside /home/<dest_user> AND fails closed on a
	// symlinked component (a swapped parent can't redirect the root write).
	homeRoot := "/home/" + p.DestUser
	destScope, scErr := filesafe.NewScope(p.DestUser, p.DestUser, []string{homeRoot})
	if scErr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal,
			Message: "scope init: " + scErr.Error()}
	}
	resolvedDest, rsErr := destScope.Resolve(p.DestPath)
	if rsErr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("dest_path %q must resolve under %s: %v", p.DestPath, homeRoot, rsErr)}
	}
	p.DestPath = resolvedDest
	// Secret path must resolve to exactly <secrets-dir>/<job-ulid>.env.
	//
	// See validateMigrationSecretPath: a root-side read of a caller-named file.
	cleanSecret, secErr := validateMigrationSecretPath(p.SecretPath)
	if secErr != nil {
		return nil, secErr
	}
	p.SecretPath = cleanSecret

	subctx, cancel := context.WithTimeout(ctx, 4*time.Hour)
	defer cancel()

	if _, mkErr := destScope.MkdirInScope(p.DestPath, true, 0o755); mkErr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("mkdir %s: %v", p.DestPath, mkErr)}
	}

	sshUser := p.SSHUser
	if sshUser == "" {
		sshUser = "root"
	}

	// SSH auth from the secret env-file + host-key-pinned ssh options
	// (loadMigrationSSHTransport).
	transport, trErr := loadMigrationSSHTransport(p.SecretPath, p.Port)
	if trErr != nil {
		return nil, trErr
	}
	defer transport.Close()
	sshOpt, sshPass := transport.SSHOpt, transport.SSHPass

	// Build rsync argv. Trailing slash on source = copy contents.
	rsyncArgs := []string{
		"-aH", "--no-h", "--info=stats2",
		"--exclude=.lock", "--exclude=.cache",
		"--exclude=public_html/.well-known/acme-challenge/",
	}
	for _, e := range p.Excludes {
		if msg := validateRsyncExclude(e); msg != "" {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument,
				Message: "excludes: " + msg}
		}
		rsyncArgs = append(rsyncArgs, "--exclude="+e)
	}
	rsyncArgs = remoteRsyncTail(rsyncArgs, rsyncPathOpt, sshOpt, sshUser+"@"+p.Host, p.SrcPath, p.DestPath)
	var cmd *exec.Cmd
	if sshPass != "" {
		// sshpass-prefixed invocation. -e reads SSHPASS from env so the
		// password never lands in ps argv.
		cmd = execCommandContext(subctx, "sshpass", append([]string{"-e", "rsync"}, rsyncArgs...)...)
		cmd.Env = append(os.Environ(), "SSHPASS="+sshPass)
	} else {
		cmd = execCommandContext(subctx, "rsync", rsyncArgs...)
	}
	out, runErr := cmd.CombinedOutput()
	partialCode := 0
	if runErr != nil {
		// 23/24 leave everything else copied: still chown it (otherwise the
		// tree keeps the source's numeric owners) and report it as partial.
		partialCode = rsyncPartialExit(runErr)
		if partialCode == 0 {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal,
				Message: fmt.Sprintf("rsync failed: %v: %s", runErr, truncate(string(out), 4096))}
		}
	}
	bytesCopied, files := parseRsyncStats(string(out))

	// Chown to dest user + group=www-data so nginx (in www-data) can
	// traverse. Same convention migration.import_home applies.
	wwwData, lkErr := lookupGroup("www-data")
	gid := dontknowGID
	if lkErr == nil {
		gid = wwwData
	}
	uid, ulkErr := lookupUserUID(p.DestUser)
	if ulkErr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("dest_user %q not found: %v", p.DestUser, ulkErr)}
	}
	chown := execCommandContext(subctx, "chown", "-R",
		fmt.Sprintf("%d:%d", uid, gid), p.DestPath)
	if cout, cerr := chown.CombinedOutput(); cerr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("chown: %v: %s", cerr, truncate(string(cout), 1024))}
	}
	_ = execCommandContext(subctx, "find", p.DestPath, "-type", "d", "-exec", "chmod", "g+rx", "{}", "+").Run()
	_ = execCommandContext(subctx, "find", p.DestPath, "-type", "f", "-exec", "chmod", "g+r", "{}", "+").Run()

	res := migrationRsyncRemoteHomeResult{
		BytesCopied: bytesCopied,
		Files:       files,
		DestPath:    p.DestPath,
	}
	if partialCode != 0 {
		res.Partial = true
		res.ExitCode = partialCode
		res.Errors = rsyncErrorLines(string(out))
	}
	return res, nil
}

// lookupGroup returns the GID of a unix group, sentinel on miss.
const dontknowGID = -1

func lookupGroup(name string) (int, error) {
	out, err := execCommand("getent", "group", name).Output()
	if err != nil {
		return 0, fmt.Errorf("getent group %s: %w", name, err)
	}
	fields := bytes.Split(bytes.TrimSpace(out), []byte(":"))
	if len(fields) < 3 {
		return 0, fmt.Errorf("getent group %s: malformed", name)
	}
	var gid int
	if _, err := fmt.Sscanf(string(fields[2]), "%d", &gid); err != nil {
		return 0, err
	}
	return gid, nil
}

func lookupUserUID(name string) (int, error) {
	out, err := execCommand("getent", "passwd", name).Output()
	if err != nil {
		return 0, fmt.Errorf("getent passwd %s: %w", name, err)
	}
	fields := bytes.Split(bytes.TrimSpace(out), []byte(":"))
	if len(fields) < 3 {
		return 0, fmt.Errorf("getent passwd %s: malformed", name)
	}
	var uid int
	if _, err := fmt.Sscanf(string(fields[2]), "%d", &uid); err != nil {
		return 0, err
	}
	return uid, nil
}

// unused-warning sink — keeps filepath dep when other paths get
// inlined later.
var _ = filepath.Join

// stripBase64Whitespace drops spaces/tabs/newlines so a PEM-wrapped or
// line-broken base64 value decodes the same way the old shell pipe did.
func stripBase64Whitespace(r rune) rune {
	switch r {
	case ' ', '\t', '\n', '\r':
		return -1
	}
	return r
}

// validateRsyncExclude sanity-checks one caller-provided --exclude pattern.
// rsync is exec'd argv-direct (no shell), so this is not an injection sink —
// it rejects shapes that could smuggle traversal into a later
// interpretation: .. segments, empty, oversized. A leading "/" is ALLOWED
// and encouraged: in rsync exclude semantics it anchors the pattern to the
// TRANSFER root (not the filesystem root), which is how the Plesk
// vhost-extra pass pins its excludes so a user's own private/bin/ deeper
// in the tree still copies. Excludes can only SHRINK the copy either way.
func validateRsyncExclude(e string) string {
	switch {
	case e == "":
		return "empty pattern"
	case strings.Contains(e, ".."):
		return "pattern must not contain ..: " + e
	case len(e) > 300:
		return "pattern too long"
	}
	return ""
}
