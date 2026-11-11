package commands

// migration_pull_mail_shadow.go — copy a cPanel account's mail password files
// from the migration source into the job's staging dir.
//
// cPanel keeps each mailbox's password hash in <home>/etc/<domain>/shadow
// (`local:hash:…`). The panel's import reads them to keep a mailbox's source
// password when the operator chose to preserve credentials. An SSH pull runs
// pkgacct --skiphomedir, so these files are not in the tarball; this verb
// fetches only them.
//
// Everything about the copy is fixed here, not by the caller:
//   - source: <src_home>/etc/, where src_home is /home[N]/<src_account>;
//   - destination: <staging>/<job_id>/mail-shadow/, created 0700, files 0600,
//     handed to the job dir's owner (the panel) once the copy is swept;
//   - filters: one level of directories, and in them only files named
//     `shadow`, at most 1 MiB. No -l/-L/-D: a symlink or device on the source
//     is skipped, never followed or recreated.
//   - a root login runs the source side as the account (sourceRsyncAsOwner),
//     so the copy reads only what the account itself can read. If <home>/etc
//     were a symlink to /, a root-side read would copy the source's
//     /etc/shadow; as the account it cannot.
//
// The reply carries counts and directory names only, never file content.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type migrationPullMailShadowParams struct {
	JobID      string `json:"job_id"`
	Host       string `json:"host"`
	SSHUser    string `json:"ssh_user"`
	SecretPath string `json:"secret_path"` // /etc/jabali-panel/migration-secrets/<job_id>.env
	SrcAccount string `json:"src_account"`
	SrcHome    string `json:"src_home"` // optional; /home[N]/<src_account>, default /home/<src_account>
	Port       int    `json:"port"`
}

type migrationPullMailShadowResult struct {
	Domains int   `json:"domains"` // domain dirs that hold a copied shadow file
	Files   int   `json:"files"`
	Bytes   int64 `json:"bytes"`
	// Removed counts entries the post-copy sweep deleted (anything that is
	// not <domain>/shadow as a regular file within the size cap).
	Removed int `json:"removed,omitempty"`
	// Unreadable lists the domain dirs the account could not read on the
	// source; their mailboxes get no source password.
	Unreadable []string `json:"unreadable,omitempty"`
	Partial    bool     `json:"partial,omitempty"`
	ExitCode   int      `json:"exit_code,omitempty"`
	Errors     []string `json:"errors,omitempty"`
}

const (
	mailShadowDirName  = "mail-shadow"
	mailShadowMaxBytes = 1 << 20
	mailShadowTimeout  = 10 * time.Minute
)

// mailShadowStagingRoot is where the panel stages a job (<root>/<job_id>).
// A var so tests can point it at a temp dir.
var mailShadowStagingRoot = migrationStagingRoot

// mailShadowSrcHomeRe is the shape of a cPanel account home: /home, /home2,
// /home3 … followed by the account name.
var mailShadowSrcHomeRe = regexp.MustCompile(`^/home[0-9]*/[^/]+$`)

func init() {
	Default.Register("migration.pull_mail_shadow", migrationPullMailShadowHandler)
}

func migrationPullMailShadowHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p migrationPullMailShadowParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument,
			Message: "malformed JSON: " + err.Error()}
	}
	srcHome, sshUser, secretPath, aerr := validateMailShadowParams(&p)
	if aerr != nil {
		return nil, aerr
	}

	destDir, aerr := prepareMailShadowDir(p.JobID)
	if aerr != nil {
		return nil, aerr
	}
	// A failed copy leaves nothing behind.
	done := false
	defer func() {
		if !done {
			_ = os.RemoveAll(destDir)
		}
	}()

	transport, aerr := loadMigrationSSHTransport(secretPath, p.Port)
	if aerr != nil {
		return nil, aerr
	}
	defer transport.Close()

	rsyncPathOpt, rpErr := sourceRsyncPathOpt(sshUser, srcHome, false, srcHome)
	if rpErr != "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: rpErr}
	}
	srcEtc := srcHome + "/etc"
	args := mailShadowRsyncArgs(rsyncPathOpt, transport.SSHOpt, sshUser+"@"+p.Host, srcEtc, destDir)

	subctx, cancel := context.WithTimeout(ctx, mailShadowTimeout)
	defer cancel()
	var cmd *exec.Cmd
	if transport.SSHPass != "" {
		cmd = execCommandContext(subctx, "sshpass", append([]string{"-e", "rsync"}, args...)...)
		cmd.Env = append(os.Environ(), "SSHPASS="+transport.SSHPass)
	} else {
		cmd = execCommandContext(subctx, "rsync", args...)
	}
	out, runErr := cmd.CombinedOutput()
	partial := 0
	if runErr != nil {
		if partial = rsyncPartialExit(runErr); partial == 0 {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal,
				Message: fmt.Sprintf("rsync failed: %v: %s", runErr, strings.Join(rsyncErrorLines(string(out)), "; "))}
		}
	}

	res, sErr := sweepMailShadowDir(destDir)
	if sErr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "sweep " + mailShadowDirName + ": " + sErr.Error()}
	}
	if cErr := handMailShadowToJobOwner(p.JobID); cErr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal,
			Message: "hand " + mailShadowDirName + " to the job dir's owner: " + cErr.Error()}
	}
	res.Unreadable = mailShadowUnreadableDomains(string(out), srcEtc)
	if partial != 0 {
		res.Partial = true
		res.ExitCode = partial
		res.Errors = rsyncErrorLines(string(out))
	}
	done = true
	return res, nil
}

// validateMailShadowParams checks every caller-supplied field and returns the
// account home, the SSH login and the cleaned secret path.
func validateMailShadowParams(p *migrationPullMailShadowParams) (srcHome, sshUser, secretPath string, aerr *agentwire.AgentError) {
	bad := func(msg string) (string, string, string, *agentwire.AgentError) {
		return "", "", "", &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: msg}
	}
	if p.JobID == "" || p.Host == "" || p.SecretPath == "" || p.SrcAccount == "" {
		return bad("job_id, host, secret_path, src_account required")
	}
	if !migrationJobIDRe.MatchString(p.JobID) {
		return bad("invalid job_id")
	}
	if !validMailShadowHost(p.Host) {
		return bad("invalid host")
	}
	sshUser = p.SSHUser
	if sshUser == "" {
		sshUser = "root"
	}
	if sshUser != "root" && (!looksLikeUnixUsername(sshUser) || strings.HasPrefix(sshUser, "-")) {
		return bad("invalid ssh_user")
	}
	acctRoot, msg := resolveRsyncSrcRoot("", p.SrcAccount)
	if msg != "" {
		return bad(msg)
	}
	srcHome = acctRoot
	if p.SrcHome != "" {
		clean := filepath.Clean(p.SrcHome)
		if clean != p.SrcHome || !mailShadowSrcHomeRe.MatchString(clean) || filepath.Base(clean) != p.SrcAccount {
			return bad("src_home must be /home[N]/<src_account>")
		}
		srcHome = clean
	}
	if !shellSafeSourcePath(srcHome) {
		return bad(fmt.Sprintf("source account directory %q may only use letters, digits, '.', '_', '-' and '/'", srcHome))
	}
	secretPath, aerr = validateMigrationSecretPath(p.SecretPath)
	if aerr != nil {
		return "", "", "", aerr
	}
	if strings.TrimSuffix(filepath.Base(secretPath), ".env") != p.JobID {
		return bad("secret_path must be the job's own <job_id>.env")
	}
	return srcHome, sshUser, secretPath, nil
}

// validMailShadowHost accepts a host name or an IP literal. A leading '-' is
// refused: the value becomes part of an rsync argument.
func validMailShadowHost(h string) bool {
	if h == "" || len(h) > 253 || strings.HasPrefix(h, "-") {
		return false
	}
	for _, r := range h {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '-', r == ':':
		default:
			return false
		}
	}
	return true
}

// prepareMailShadowDir creates <staging>/<job_id>/mail-shadow fresh, 0700.
// The job dir must already exist as a real directory. An earlier run's
// mail-shadow dir is removed first so no stale hash survives; a mail-shadow
// that is a symlink or anything but a directory is refused.
func prepareMailShadowDir(jobID string) (string, *agentwire.AgentError) {
	root := canonicalizeRootPrefix(mailShadowStagingRoot, migrationStagingRoot)
	const dirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	rootFd, err := unix.Open(root, dirFlags, 0)
	if err != nil {
		return "", &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("staging root %s: %v", root, err)}
	}
	defer unix.Close(rootFd)
	jobFd, err := unix.Openat(rootFd, jobID, dirFlags, 0)
	if err != nil {
		return "", &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("job staging dir %s/%s missing or not a directory: %v", root, jobID, err)}
	}
	defer unix.Close(jobFd)
	// Refuse before anything is copied: the files go to this dir's owner.
	if _, _, err := mailShadowJobOwner(rootFd, jobFd); err != nil {
		return "", &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: err.Error()}
	}

	dest := filepath.Join(root, jobID, mailShadowDirName)
	var st unix.Stat_t
	switch err := unix.Fstatat(jobFd, mailShadowDirName, &st, unix.AT_SYMLINK_NOFOLLOW); {
	case err == nil:
		if st.Mode&unix.S_IFMT != unix.S_IFDIR {
			return "", &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition,
				Message: fmt.Sprintf("refusing: %s exists and is not a directory", dest)}
		}
		if rmErr := os.RemoveAll(dest); rmErr != nil {
			return "", &agentwire.AgentError{Code: agentwire.CodeInternal,
				Message: fmt.Sprintf("remove earlier %s: %v", dest, rmErr)}
		}
	case !errors.Is(err, unix.ENOENT):
		return "", &agentwire.AgentError{Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("stat %s: %v", dest, err)}
	}
	if err := unix.Mkdirat(jobFd, mailShadowDirName, 0o700); err != nil {
		return "", &agentwire.AgentError{Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("mkdir %s: %v", dest, err)}
	}
	// umask-proof: the dir holds password hashes.
	dfd, err := unix.Openat(jobFd, mailShadowDirName, dirFlags, 0)
	if err != nil {
		return "", &agentwire.AgentError{Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("open %s: %v", dest, err)}
	}
	defer unix.Close(dfd)
	if err := unix.Fchmod(dfd, 0o700); err != nil {
		return "", &agentwire.AgentError{Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("chmod %s: %v", dest, err)}
	}
	return dest, nil
}

// mailShadowFchownat is unix.Fchownat; a var so tests can see the calls.
var mailShadowFchownat = unix.Fchownat

// mailShadowFstat is unix.Fstat; a var so tests can report another owner.
var mailShadowFstat = unix.Fstat

// mailShadowJobOwner returns the uid/gid the copied files are handed to: the
// owner of the job dir, accepted only when it is root or the owner of the
// staging root itself (the panel's service user, set by the installer). Any
// other owner — a mis-set install, a future writer under the staging root —
// is refused, so the source's password hashes never go to a third account.
func mailShadowJobOwner(rootFd, jobFd int) (int, int, error) {
	var rst, jst unix.Stat_t
	if err := mailShadowFstat(rootFd, &rst); err != nil {
		return 0, 0, fmt.Errorf("stat staging root: %w", err)
	}
	if err := mailShadowFstat(jobFd, &jst); err != nil {
		return 0, 0, fmt.Errorf("stat job dir: %w", err)
	}
	if jst.Uid != 0 && jst.Uid != rst.Uid {
		return 0, 0, fmt.Errorf("refusing: the job staging dir is owned by uid %d, which is neither root nor the staging root's owner (uid %d)", jst.Uid, rst.Uid)
	}
	return int(jst.Uid), int(jst.Gid), nil
}

// handMailShadowToJobOwner gives the swept mail-shadow tree to the owner of
// <staging>/<job_id> — the panel, which reads the files during the mailbox
// import and removes the dir after it. Modes stay 0700/0600. Until the last
// call the tree is root's 0700, so nothing else can have changed it; every
// chown is AT_SYMLINK_NOFOLLOW relative to a directory fd opened O_NOFOLLOW,
// and the mail-shadow dir itself goes last.
func handMailShadowToJobOwner(jobID string) error {
	root := canonicalizeRootPrefix(mailShadowStagingRoot, migrationStagingRoot)
	const dirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	rootFd, err := unix.Open(root, dirFlags, 0)
	if err != nil {
		return fmt.Errorf("staging root: %w", err)
	}
	defer unix.Close(rootFd)
	jobFd, err := unix.Openat(rootFd, jobID, dirFlags, 0)
	if err != nil {
		return fmt.Errorf("job dir: %w", err)
	}
	defer unix.Close(jobFd)
	uid, gid, err := mailShadowJobOwner(rootFd, jobFd)
	if err != nil {
		return err
	}

	dfd, err := unix.Openat(jobFd, mailShadowDirName, dirFlags, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", mailShadowDirName, err)
	}
	d := os.NewFile(uintptr(dfd), mailShadowDirName)
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return fmt.Errorf("list %s: %w", mailShadowDirName, err)
	}
	for _, name := range names {
		domFd, err := unix.Openat(dfd, name, dirFlags, 0)
		if err != nil {
			return fmt.Errorf("open %s: %w", name, err)
		}
		cerr := mailShadowFchownat(domFd, "shadow", uid, gid, unix.AT_SYMLINK_NOFOLLOW)
		if cerr == nil {
			cerr = mailShadowFchownat(dfd, name, uid, gid, unix.AT_SYMLINK_NOFOLLOW)
		}
		unix.Close(domFd)
		if cerr != nil {
			return fmt.Errorf("chown %s: %w", name, cerr)
		}
	}
	if err := mailShadowFchownat(jobFd, mailShadowDirName, uid, gid, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("chown %s: %w", mailShadowDirName, err)
	}
	return nil
}

// mailShadowFilterArgs are the rsync options that decide what is copied.
// -r without -l/-L/-D: symlinks and devices are skipped, never followed.
// Only directories one level down and, inside them, files named `shadow`
// pass the filters; -m drops directories left empty.
func mailShadowFilterArgs() []string {
	return []string{
		"-rt", "-m",
		"--max-size=1m",
		"--chmod=D0700,F0600",
		"--include=/*/", "--include=/*/shadow", "--exclude=*",
	}
}

// mailShadowRsyncArgs is the full rsync argv for the copy.
func mailShadowRsyncArgs(rsyncPathOpt, sshOpt, userHost, srcEtc, dest string) []string {
	return remoteRsyncTail(mailShadowFilterArgs(), rsyncPathOpt, sshOpt, userHost, srcEtc, dest)
}

// sweepMailShadowDir keeps only <domain>/shadow regular files within the size
// cap and removes everything else, then counts what is left.
func sweepMailShadowDir(dest string) (migrationPullMailShadowResult, error) {
	var res migrationPullMailShadowResult
	entries, err := os.ReadDir(dest)
	if err != nil {
		return res, err
	}
	for _, e := range entries {
		p := filepath.Join(dest, e.Name())
		fi, err := os.Lstat(p)
		if err != nil {
			return res, err
		}
		if !fi.IsDir() {
			if err := os.RemoveAll(p); err != nil {
				return res, err
			}
			res.Removed++
			continue
		}
		sub, err := os.ReadDir(p)
		if err != nil {
			return res, err
		}
		kept := 0
		for _, s := range sub {
			sp := filepath.Join(p, s.Name())
			si, err := os.Lstat(sp)
			if err != nil {
				return res, err
			}
			if s.Name() == "shadow" && si.Mode().IsRegular() && si.Size() <= mailShadowMaxBytes {
				kept++
				res.Files++
				res.Bytes += si.Size()
				continue
			}
			if err := os.RemoveAll(sp); err != nil {
				return res, err
			}
			res.Removed++
		}
		if kept == 0 {
			if err := os.Remove(p); err != nil {
				return res, err
			}
			continue
		}
		res.Domains++
	}
	return res, nil
}

// mailShadowUnreadableDomains names the <srcEtc>/<domain> dirs (or shadow
// files) rsync reported it could not read, from its error lines. Names only.
func mailShadowUnreadableDomains(out, srcEtc string) []string {
	seen := map[string]bool{}
	prefix := `"` + strings.TrimRight(srcEtc, "/") + "/"
	for _, l := range strings.Split(out, "\n") {
		if !strings.Contains(l, "Permission denied") {
			continue
		}
		i := strings.Index(l, prefix)
		if i < 0 {
			continue
		}
		rest := l[i+len(prefix):]
		end := strings.IndexAny(rest, `"/`)
		if end <= 0 {
			continue
		}
		name := rest[:end]
		if len(name) <= 253 && !strings.ContainsAny(name, " \t") {
			seen[name] = true
		}
	}
	out2 := make([]string, 0, len(seen))
	for n := range seen {
		out2 = append(out2, n)
	}
	sort.Strings(out2)
	if len(out2) == 0 {
		return nil
	}
	return out2
}
