package commands

// migration.rsync_remote_home — rsync source:<src_path>/ → <dest_path>/
// via sshpass / ssh-key. Run by the restore stage once per domain in
// the BackupUser-emitted domains-paths.txt manifest. Replaces the
// "bundle home into cpmove tar then ImportHomeSplit" path so transfer
// resumes on transient failures + skips already-synced files.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	// A bare strings.HasPrefix is not enough: it happily accepts
	// "/etc/jabali-panel/migration-secrets/../../shadow", and the file is read
	// below with os.ReadFile as root. Clean the path first, then require the
	// directory to match exactly and the basename to be a validated job id --
	// the same shape migration_admin_run.go already enforces, which takes a
	// job_id and builds the path itself rather than trusting one.
	//
	// Not reachable today: every caller builds this from a server-generated
	// genULID() job id. This is the boundary check CONVENTIONS.md asks for
	// regardless of who the caller is, on a root-side arbitrary-file read.
	cleanSecret := filepath.Clean(p.SecretPath)
	secretBase := strings.TrimSuffix(filepath.Base(cleanSecret), ".env")
	if filepath.Dir(cleanSecret) != migrationSecretsBaseDir ||
		!strings.HasSuffix(cleanSecret, ".env") ||
		!migrationJobIDRe.MatchString(secretBase) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument,
			Message: "secret_path must be <job-id>.env directly under " + migrationSecretsBaseDir}
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

	// Resolve SSH auth from the secret env-file. Two recognised keys
	// (same shape cpanel/da/hestia Discoverer loadSecret reads):
	//   SSH_PASSWORD=<plain>
	//   SSH_PRIVATE_KEY_B64=<base64-PEM>
	secretBytes, sErr := os.ReadFile(p.SecretPath)
	if sErr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("read secret %s: %v", p.SecretPath, sErr)}
	}
	var sshPass string
	var keyTmp string
	for _, line := range strings.Split(string(secretBytes), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "SSH_PASSWORD":
			sshPass = v
		case "SSH_PRIVATE_KEY_B64":
			// Stash the decoded key in a 0600 tempfile rsync can ssh -i.
			b64 := strings.TrimSpace(v)
			tmp, tmpErr := os.CreateTemp("", "jabali-migrate-key-*")
			if tmpErr != nil {
				continue
			}
			_ = os.Chmod(tmp.Name(), 0o600)
			// Decode in-process. Previously this shelled out to
			// `bash -c "echo $b64 | base64 -d > tmp"`, interpolating the
			// secret-file value into a shell string — a shell-injection
			// sink if the value isn't pure base64. Go's decoder removes the
			// shell entirely.
			decoded, dErr := base64.StdEncoding.DecodeString(strings.Map(stripBase64Whitespace, b64))
			if dErr != nil {
				_ = tmp.Close()
				_ = os.Remove(tmp.Name())
				continue
			}
			if _, wErr := tmp.Write(decoded); wErr != nil {
				_ = tmp.Close()
				_ = os.Remove(tmp.Name())
				continue
			}
			_ = tmp.Close()
			keyTmp = tmp.Name()
		}
	}
	if sshPass == "" && keyTmp == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition,
			Message: "no SSH_PASSWORD or SSH_PRIVATE_KEY_B64 in secret file"}
	}
	if keyTmp != "" {
		defer os.Remove(keyTmp)
	}

	// Build rsync argv. Trailing slash on source = copy contents.
	// Host-key pinning (Gitea #461): consume the per-job known_hosts the
	// panel-api discover stage pinned (sibling of the secret file) and use
	// accept-new, so a key already pinned at discover is verified (a
	// mid-migration key flip is rejected) and an unpinned host is captured on
	// first use — never the old blind StrictHostKeyChecking=no + /dev/null.
	knownHosts := strings.TrimSuffix(p.SecretPath, ".env") + ".known_hosts"
	sshOpt := "-o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=" + knownHosts
	port := p.Port
	if port < 1 || port > 65535 {
		port = 22
	}
	sshOpt += " -p " + strconv.Itoa(port)
	if keyTmp != "" {
		sshOpt += " -i " + keyTmp + " -o IdentitiesOnly=yes"
	}
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
	rsyncArgs = append(rsyncArgs,
		"-e", "ssh "+sshOpt,
		sshUser+"@"+p.Host+":"+strings.TrimRight(p.SrcPath, "/")+"/",
		strings.TrimRight(p.DestPath, "/")+"/",
	)
	var cmd *exec.Cmd
	if sshPass != "" {
		// sshpass-prefixed invocation. -e reads SSHPASS from env so the
		// password never lands in ps argv.
		cmd = exec.CommandContext(subctx, "sshpass", append([]string{"-e", "rsync"}, rsyncArgs...)...)
		cmd.Env = append(os.Environ(), "SSHPASS="+sshPass)
	} else {
		cmd = exec.CommandContext(subctx, "rsync", rsyncArgs...)
	}
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("rsync failed: %v: %s", runErr, truncate(string(out), 4096))}
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
	chown := exec.CommandContext(subctx, "chown", "-R",
		fmt.Sprintf("%d:%d", uid, gid), p.DestPath)
	if cout, cerr := chown.CombinedOutput(); cerr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("chown: %v: %s", cerr, truncate(string(cout), 1024))}
	}
	_ = exec.CommandContext(subctx, "find", p.DestPath, "-type", "d", "-exec", "chmod", "g+rx", "{}", "+").Run()
	_ = exec.CommandContext(subctx, "find", p.DestPath, "-type", "f", "-exec", "chmod", "g+r", "{}", "+").Run()

	return migrationRsyncRemoteHomeResult{
		BytesCopied: bytesCopied,
		Files:       files,
		DestPath:    p.DestPath,
	}, nil
}

// lookupGroup returns the GID of a unix group, sentinel on miss.
const dontknowGID = -1

func lookupGroup(name string) (int, error) {
	out, err := exec.Command("getent", "group", name).Output()
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
	out, err := exec.Command("getent", "passwd", name).Output()
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
// it rejects shapes that could WIDEN the copy or smuggle traversal into a
// later interpretation: absolute patterns, .. segments, empty, oversized.
func validateRsyncExclude(e string) string {
	switch {
	case e == "":
		return "empty pattern"
	case strings.HasPrefix(e, "/"):
		return "pattern must be relative: " + e
	case strings.Contains(e, ".."):
		return "pattern must not contain ..: " + e
	case len(e) > 300:
		return "pattern too long"
	}
	return ""
}
