package commands

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// M30 backup-side helpers. Shared across backup_home / backup_databases /
// backup_mailboxes / backup_create / backup_restore.

// ulidRE is the agent-side ULID validator; mirror of scanIDRE in
// security_malware.go (Crockford base32, 26 chars).
var ulidRE = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// backupUsernameRE mirrors usernameRE in security_malware.go. Linux
// username constraint, used to build /home/<u> paths.
var backupUsernameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// backupDomainNameRE constrains a domain name that flows into a restic --include
// path + rsync destination (GH #1359 per-domain docroot restore): lowercase DNS
// labels only, so no '/', '..', or leading '-' can escape ~/domains/<domain>.
var backupDomainNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// dbNameRE matches MariaDB database names: alpha + digits + underscore,
// up to 64 chars.
var dbNameRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,63}$`)

// emailLocalRE matches the local-part of an email address; we don't
// allow shell-special chars in mailbox tokens that build CLI args.
var emailLocalRE = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// emailDomainRE matches a domain label list.
var emailDomainRE = regexp.MustCompile(`^[a-zA-Z0-9.-]+$`)

// bkInvalidArg is the backup-side wrapper for InvalidArgument errors.
func bkInvalidArg(msg string) error {
	return &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: msg}
}

// bkInternal wraps an internal-error envelope. Restic lock contention gets
// its own typed, human-sized error: even with --retry-lock (GH #1045), a
// long-running backup can outlast the wait, and the raw restic stderr JSON
// is unreadable in a UI toast. FailedPrecondition tells the caller (and the
// operator) this is "busy, retry later", not a broken repository.
func bkInternal(msg string, err error) error {
	if err != nil && strings.Contains(err.Error(), "repository is already locked") {
		return &agentwire.AgentError{
			Code:    agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("%s: the backup repository is busy (another backup, restore, or cleanup is running) — try again in a few minutes", msg),
		}
	}
	return &agentwire.AgentError{
		Code:    agentwire.CodeInternal,
		Message: fmt.Sprintf("%s: %v", msg, err),
	}
}

// bkResticBin returns the restic binary path. Hard-fail in handlers
// that need it; the foundation step (install_backup_foundation) is
// supposed to land it on every host.
func bkResticBin() (string, error) {
	bin, err := exec.LookPath("restic")
	if err != nil {
		return "", fmt.Errorf("restic binary missing: %w", err)
	}
	return bin, nil
}

// bkResticConfig builds a ResticConfig pointing at the destination
// (post-M30.2 / ADR-0080). repoURL empty falls back to the legacy
// local repo so unit tests + callers that don't supply a destination
// keep working unchanged.
func bkResticConfig(repoURL, credentialsRef string, sftp *backupSFTPInputs) (backup.ResticConfig, error) {
	return bkResticConfigWithPassword(repoURL, credentialsRef, "", sftp)
}

// bkResticOptions builds restic's `-o key=value` flags for this request.
//
// SECURITY (JAB-194): the ONLY option the agent will ever pass to restic is
// `sftp.command`, and the agent BUILDS it here from typed fields — it is never
// accepted from the wire.
//
// `sftp.command` is the command restic executes to reach an SFTP backend, so a
// caller who can set it gets arbitrary command execution as root. The agent
// previously took a pre-built `extra_options []string` off the wire and passed
// it into restic's argv unvalidated, across ten commands.
//
// An allowlist of permitted `-o` keys does not fix that, which is why the
// contract changed instead: the dangerous key IS the one we legitimately use,
// so any filter loose enough to admit our real value —
//
//	sftp.command=sshpass -e ssh -o StrictHostKeyChecking=accept-new … user@host -s sftp
//
// is loose enough to admit a hostile one. Scanning it for shell metacharacters
// is equally hopeless when the legitimate value is full of spaces, quotes and
// flags. Taking typed inputs and constructing the string here removes the
// primitive rather than trying to recognise abuse of it — the same reasoning as
// migration_admin_run.go taking a job_id and deriving the secret path itself.
//
// TestNoAgentBackupStructAcceptsExtraOptions keeps the wire field from coming
// back.
func bkResticOptions(sftp *backupSFTPInputs) []string {
	if sftp == nil {
		return nil
	}
	flag := backup.SFTPCommandFlag(backup.SFTPInputs{
		Host:    sftp.Host,
		User:    sftp.User,
		Port:    sftp.Port,
		Path:    sftp.Path,
		Auth:    sftp.Auth,
		KeyPath: sftp.KeyPath,
	})
	if flag == "" {
		return nil
	}
	return []string{flag}
}

// bkResticConfigWithPassword is the variant used by interactive
// disaster-recovery. passwordFile empty falls back to the canonical
// /etc/jabali-panel/restic-repo.password; non-empty lets the CLI hand
// a temp-file path so a live host's runtime password isn't clobbered
// during a drill / test recovery.
func bkResticConfigWithPassword(repoURL, credentialsRef, passwordFile string, sftp *backupSFTPInputs) (backup.ResticConfig, error) {
	cfg := backup.DefaultConfig()
	if repoURL != "" {
		cfg.Repo = repoURL
	}
	if passwordFile != "" {
		cfg.PasswordFile = passwordFile
	}
	// Built from typed inputs, never taken from the request (JAB-194).
	cfg.ExtraOptions = bkResticOptions(sftp)
	if credentialsRef != "" {
		env, err := backup.LoadEnvFile(credentialsRef)
		if err != nil {
			return cfg, fmt.Errorf("load creds %s: %w", credentialsRef, err)
		}
		cfg.ExtraEnv = env
	}
	return cfg, nil
}

// repoProbeClass classifies the stderr of a failed `restic snapshots` probe so
// bkEnsureRepoReady can tell "repo isn't there yet, go init it" apart from
// "repo is there but we can't open it" (GH #454) apart from anything else.
type repoProbeClass int

const (
	repoProbeOther             repoProbeClass = iota // unknown failure — surface raw
	repoProbeMissing                                 // no repo at the location → init
	repoProbeUnopenable                              // repo exists but no stored key matches the password (foreign/rotated) → GH #454
	repoProbeKeyConfigMismatch                       // a key opened with the password but its master can't decrypt config → concurrent-init race (JAB-405) or corrupt config
)

// classifyRepoProbe maps a lowercased restic stderr to a repoProbeClass. Strings
// are the verbatim messages restic emits (captured against 0.16.4 and re-checked
// on 0.18.0):
//   - missing:    "unable to open config file: … no such file …" / "repository does not exist"
//   - unopenable: "wrong password or no key found" — no stored key decrypts with
//     the password (foreign/rotated password, e.g. a host reinstall).
//   - key/config mismatch: "config or key <id> is damaged: ciphertext verification
//     failed" — a key file DID open with the password (so the password is correct),
//     but its master key can't decrypt `config`. That is the signature of two
//     concurrent `restic init` on one empty repo (JAB-405), or a corrupt config —
//     NOT a wrong password. The two never co-occur (verified: wrong-password vs an
//     initialized repo yields the first string, the dual-key race yields the second),
//     so they map to distinct classes and distinct recovery advice.
//
// Match the mismatch case on the specific "ciphertext verification failed" phrase,
// not a bare "is damaged": restic also says "<thing> is damaged" for index/pack
// corruption, which must keep the generic unopenable hint rather than the
// "your password is fine, count your key files" guidance.
func classifyRepoProbe(lowerStderr string) repoProbeClass {
	if strings.Contains(lowerStderr, "ciphertext verification failed") {
		return repoProbeKeyConfigMismatch
	}
	if strings.Contains(lowerStderr, "wrong password or no key found") ||
		strings.Contains(lowerStderr, "is damaged") {
		return repoProbeUnopenable
	}
	if strings.Contains(lowerStderr, "repository does not exist") ||
		strings.Contains(lowerStderr, "unable to open config file") ||
		strings.Contains(lowerStderr, "is there a repository at") {
		return repoProbeMissing
	}
	return repoProbeOther
}

// resticKeyIDRE matches a restic key-file name: a 32-byte key id, hex-encoded to
// 64 lowercase hex chars. Filtering keys/ entries on it counts real key files
// only, dropping any stray (a moved-aside .jabali-* key, a lost+found).
var resticKeyIDRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// mismatchKeyIDRE pulls the key id restic names in
// "config or key <id> is damaged: ciphertext verification failed". The id is the
// full 64-hex key-file name (verified against restic 0.18.0), so it matches a
// keys/ entry exactly — the key whose master does not decrypt config.
var mismatchKeyIDRE = regexp.MustCompile(`config or key ([0-9a-f]{64}) is damaged`)

// repoKeyListing is the result of counting a repository's key files (JAB-405
// Part 2a). It sharpens the key/config-mismatch message from "count the files
// yourself" to a concrete count plus which key restic named. A nil *repoKeyListing
// means the count was not available — an unsupported backend, or a listing that
// failed (carried separately as listErr) — and the message falls back to the
// generic count-it-yourself guidance.
type repoKeyListing struct {
	ids   []string // key-file names in keys/, each a 64-hex restic key id
	named string   // the key id restic named in the ciphertext error, "" if unparsed
}

// namedPresent reports whether the key restic named in the error is actually one
// of the listed key files. The move-aside recovery is only ever suggested when
// this holds: if the named key is absent, the listing does not match the failure
// and moving a file would be blind (the 2b safety invariant, surfaced as advice).
func (l *repoKeyListing) namedPresent() bool {
	if l == nil || l.named == "" {
		return false
	}
	for _, id := range l.ids {
		if id == l.named {
			return true
		}
	}
	return false
}

// parseKeyIDs extracts restic key-file names from `ls -1` output (one name per
// line), keeping only 64-hex entries so a stray file never inflates the count.
func parseKeyIDs(raw []byte) []string {
	var ids []string
	for _, line := range strings.Split(string(raw), "\n") {
		name := strings.TrimSpace(line)
		if resticKeyIDRE.MatchString(name) {
			ids = append(ids, name)
		}
	}
	return ids
}

// mismatchKeyID returns the key id restic named in a lowered ciphertext-mismatch
// stderr, or "" if the phrase is not present or the id can't be parsed.
func mismatchKeyID(lowerStderr string) string {
	if m := mismatchKeyIDRE.FindStringSubmatch(lowerStderr); len(m) == 2 {
		return m[1]
	}
	return ""
}

// listRepoKeysTimeout bounds the read-only keys/ listing that a mismatch failure
// folds in for diagnostics. The probe already reached the repo (a ciphertext
// error means ssh auth and reachability were fine), so a hang is unlikely; the
// bound just guarantees the diagnostic never makes the failure materially slower.
const listRepoKeysTimeout = 30 * time.Second

// listRepoKeys lists the restic key-file names in a repository's keys/ directory
// directly, BELOW restic — restic cannot open a config-mismatched repo to list
// keys itself (`restic key list` dies with the same ciphertext error, since it
// opens config first). SFTP → `ssh user@host -- ls -1 <path>/keys`; local →
// os.ReadDir. Any other backend is unsupported and returns an error the caller
// folds into a fail-soft message. Read-only — it never creates, moves, or
// deletes anything.
func listRepoKeys(ctx context.Context, destKind, repoURL string, sftp *backupSFTPInputs, extraEnv []string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, listRepoKeysTimeout)
	defer cancel()

	switch {
	case destKind == backup.KindSFTP && sftp != nil && sftp.Host != "":
		out, err := backup.ListRemoteSFTP(ctx, backup.SFTPInputs{
			Host:    sftp.Host,
			User:    sftp.User,
			Port:    sftp.Port,
			Path:    sftp.Path,
			Auth:    sftp.Auth,
			KeyPath: sftp.KeyPath,
		}, path.Join(sftp.Path, "keys"), extraEnv)
		if err != nil {
			return nil, err
		}
		return parseKeyIDs(out), nil
	case destKind == backup.KindLocal:
		entries, err := os.ReadDir(filepath.Join(repoURL, "keys"))
		if err != nil {
			return nil, fmt.Errorf("read keys dir: %w", err)
		}
		var ids []string
		for _, e := range entries {
			if resticKeyIDRE.MatchString(e.Name()) {
				ids = append(ids, e.Name())
			}
		}
		return ids, nil
	default:
		return nil, fmt.Errorf("cannot list keys/ for a %q backend", destKind)
	}
}

// repoUnopenableMessage builds the operator-facing error for a repository that
// EXISTS but cannot be opened. It is a single paragraph with no newlines: the
// same text surfaces in a run-failure log AND in a short UI toast on the manual
// "test connection" path, and a multi-line block is unreadable in the toast.
// passwordFile is the restic password file the probe used (per-destination sealed
// file or the shared default), named so the operator edits the right one.
//
// For the key/config-mismatch class (JAB-405), keys carries a key-file count when
// one is available (Part 2a): a known count turns the generic "count them
// yourself" guidance into a concrete instruction — the race (≥2 keys incl. the
// named one) vs a corrupt config (exactly 1) vs a listing that does not match the
// error (do NOT move anything). keys nil with a non-nil listErr keeps the generic
// guidance and appends why the count is missing (fail-soft: a listing failure
// never masks the underlying mismatch). listErr is flattened to one line so the
// toast stays a single paragraph.
func repoUnopenableMessage(class repoProbeClass, repoURL, passwordFile, lowerStderr string, keys *repoKeyListing, listErr error) string {
	switch class {
	case repoProbeKeyConfigMismatch:
		preamble := fmt.Sprintf("backup repository at %q exists and a key file opened with the password at %s, "+
			"so the password is CORRECT — do NOT restore or regenerate it. The failure (%s) is a key whose "+
			"master does not match the repository config, which happens when two backups ran `restic init` on "+
			"the same empty repository at once (leaving more than one key file and a single config), or the "+
			"config is corrupt.",
			repoURL, passwordFile, lowerStderr)

		var recovery string
		switch {
		case keys != nil && len(keys.ids) >= 2 && keys.namedPresent():
			// The race: more than one key file and the key restic named is one of
			// them. Name the exact file to move so there is no guessing.
			recovery = fmt.Sprintf(" To recover: the keys/ directory holds %d key files and one is the key restic "+
				"named (%s), so this is the concurrent-init race — move keys/%s out of keys/ and retry (restore it "+
				"if that does not help). The old snapshots stay on disk.",
				len(keys.ids), keys.named, keys.named)
		case keys != nil && len(keys.ids) == 1 && keys.namedPresent():
			// One key file that still can't decrypt config: the config itself is
			// corrupt, so there is nothing to move — start fresh.
			recovery = " To recover: the keys/ directory holds exactly ONE key file, so the config itself is " +
				"corrupt — point this destination at a FRESH empty directory. The old snapshots stay on disk."
		case keys != nil:
			// Listing succeeded but does not match the error (the named key is absent,
			// or no key files parsed). Do NOT tell the operator to move anything — a
			// blind move is exactly what the 2b safety invariant forbids.
			recovery = fmt.Sprintf(" To recover: the keys/ directory holds %d key files but the key restic named "+
				"in the error is not among them, so the listing does not match the failure — do NOT move any key "+
				"file; treat the repository as corrupt and point this destination at a FRESH empty directory. The "+
				"old snapshots stay on disk.",
				len(keys.ids))
		default:
			// Count unavailable — keep the original count-it-yourself guidance, byte
			// for byte, then say why the automatic count is missing if a list failed.
			recovery = " To recover: if the repository's keys/ directory holds MORE THAN ONE file, move " +
				"the key restic named in the error out of keys/ and retry (restore it if that does not help); if keys/ " +
				"holds exactly ONE file the config is corrupt — point this destination at a FRESH empty directory. " +
				"The old snapshots stay on disk."
			if listErr != nil {
				recovery += " (Could not list keys/ to count the key files automatically: " +
					strings.Join(strings.Fields(listErr.Error()), " ") + ".)"
			}
		}
		return preamble + recovery
	default: // repoProbeUnopenable
		return fmt.Sprintf("backup repository at %q exists but this server cannot open it (%s). No stored key "+
			"matches the password at %s — usually the server or that password file was reinstalled or regenerated "+
			"while the repository directory was preserved, so the snapshots are sealed with a password this server "+
			"no longer has. To recover: restore the ORIGINAL password file from before the reinstall, or point this "+
			"destination at a FRESH empty directory to start a new repository — the old snapshots stay on disk but "+
			"are unreadable without their original password.",
			repoURL, lowerStderr, passwordFile)
	}
}

// bkEnsureRepoReady probes the remote and runs mkdir -p (SFTP only) +
// `restic init` if the repo doesn't exist yet. Idempotent — succeeds
// on already-initialized repos. Local destinations get the parent dir
// created if missing; failures bubble up.
//
// The probe→init is serialized per destination via a file lock (JAB-405):
// a schedule's first run against a brand-new destination fans out several
// per-account jobs concurrently, and without serialization each one probes
// the empty repo, classifies it "missing", and runs `restic init`. Two
// simultaneous inits on the same empty repo leave two key files but a single
// `config`, whose master key matches only one of them; a later open can pick
// the mismatched key and fail fatally ("config or key <id> is damaged:
// ciphertext verification failed") instead of falling through — the repo is
// then permanently unopenable and every backup fails at "ensure repo". The
// lock makes the loser re-probe after the winner's init and short-circuit on
// the now-existing repo.
func bkEnsureRepoReady(ctx context.Context, repoURL, credentialsRef, destKind, passwordFile string, sftp *backupSFTPInputs) error {
	if repoURL == "" {
		return nil
	}
	// A destination with its own sealed password (M30.2.x) must be probed
	// AND initialised with THAT password. Probing with the legacy shared
	// file made a rotated destination look unopenable — and once the
	// reconciler purges the legacy file (all destinations migrated), this
	// probe hard-failed before any stage could run.
	pwFile := passwordFile
	if pwFile == "" {
		pwFile = backup.DefaultPasswordFile
	}
	var extraEnv []string
	if credentialsRef != "" {
		env, err := backup.LoadEnvFile(credentialsRef)
		if err != nil {
			return fmt.Errorf("load creds: %w", err)
		}
		extraEnv = env
	}
	return withRepoInitLock(ctx, repoURL, func() error {
		_, snapStderr, snapErr := backup.SnapshotsRemote(ctx, nil, repoURL, pwFile, extraEnv, bkResticOptions(sftp))
		if snapErr == nil {
			return nil
		}
		lower := strings.ToLower(strings.TrimSpace(string(snapStderr)))
		switch cls := classifyRepoProbe(lower); cls {
		case repoProbeMissing:
			// Only an explicit missing-repo signal reaches `restic init` below.
			// Fail-closed: any other class returns here, so a new class (or a
			// dropped case) can never fall through to init an existing-but-broken
			// repo — where "already initialized" is swallowed and the run proceeds
			// to die later with a worse error.
		case repoProbeOther:
			// Not a missing-repo signal, not a known unopenable signal — surface raw.
			return fmt.Errorf("snapshots probe: %w (stderr: %s)", snapErr, lower)
		default:
			// A repository that EXISTS but cannot be opened: a foreign/rotated
			// password (GH #454) or a key/config mismatch from a concurrent-init
			// race (JAB-405). Actionable message instead of the raw restic dump.
			//
			// For the mismatch class, count the key files (below restic, which
			// can't open the repo to list them) so the message can name the exact
			// key to move vs "the config is corrupt, start fresh" (JAB-405 Part
			// 2a). Read-only. Fail-soft: a listing error is folded into the message,
			// never returned in place of the mismatch — the operator must still see
			// why the backup failed.
			var keys *repoKeyListing
			var listErr error
			if cls == repoProbeKeyConfigMismatch {
				if ids, err := listRepoKeys(ctx, destKind, repoURL, sftp, extraEnv); err != nil {
					listErr = err
				} else {
					keys = &repoKeyListing{ids: ids, named: mismatchKeyID(lower)}
				}
			}
			return errors.New(repoUnopenableMessage(cls, repoURL, pwFile, lower, keys, listErr))
		}
		if destKind == "sftp" && sftp != nil && sftp.Host != "" {
			if _, err := backup.MkdirRemoteSFTP(ctx, backup.SFTPInputs{
				Host:    sftp.Host,
				User:    sftp.User,
				Port:    sftp.Port,
				Path:    sftp.Path,
				Auth:    sftp.Auth,
				KeyPath: sftp.KeyPath,
			}, extraEnv); err != nil {
				return fmt.Errorf("ssh mkdir: %w", err)
			}
		}
		// Init with the SAME password the probe used: a fresh repo for a
		// destination that carries its own sealed password must be created under
		// that password, or every later open fails.
		_, initStderr, initErr := backup.InitRemote(ctx, nil, repoURL, pwFile, extraEnv, bkResticOptions(sftp))
		if initErr != nil {
			ls := strings.ToLower(strings.TrimSpace(string(initStderr)))
			if strings.Contains(ls, "already initialized") ||
				strings.Contains(ls, "config file already exists") {
				return nil
			}
			return fmt.Errorf("restic init: %w (stderr: %s)", initErr, ls)
		}
		return nil
	})
}

// repoInitLockDir is the directory the per-destination repo-init locks live in.
// It defaults to the same directory as the restore flock (which the agent
// already mkdir's and writes to, so no new permission surface). A var, not a
// const, so tests can redirect it to a temp dir.
var repoInitLockDir = filepath.Dir(restoreLockPath)

const (
	// repoInitLockMaxWait bounds how long a job waits for another job's
	// probe→init on the SAME destination. init is seconds; this is generous.
	// Without a bound, a job could sit behind a hung SFTP init for the whole
	// orchestrator deadline (90m account / 6h system).
	repoInitLockMaxWait = 5 * time.Minute
	// repoInitLockPoll is the retry interval while the lock is contended.
	repoInitLockPoll = 200 * time.Millisecond
)

// withRepoInitLock runs fn while holding an exclusive advisory file lock scoped
// to one destination (keyed by the repo URL), so concurrent first-run jobs
// cannot both `restic init` the same empty repo (JAB-405). Different
// destinations hash to different lock files and never contend; the lock is held
// only across the short probe+init, then released.
//
// syscall.Flock has no context support, so a bare blocking LOCK_EX would ignore
// the caller's deadline and could park behind a hung init. Instead this polls
// LOCK_EX|LOCK_NB, honouring ctx and repoInitLockMaxWait, and fails loud rather
// than proceeding without the lock — running init unserialized is exactly the
// bug this prevents.
func withRepoInitLock(ctx context.Context, repoURL string, fn func() error) error {
	if err := os.MkdirAll(repoInitLockDir, 0o750); err != nil {
		return fmt.Errorf("mkdir repo-init lock dir: %w", err)
	}
	// Key on the exact repo URL string (not a normalized form): two jobs for
	// the same destination carry the byte-identical URL, and re-pointing a
	// destination to a fresh directory (the JAB-405 workaround) is a different
	// URL that correctly gets its own lock.
	sum := sha256.Sum256([]byte(repoURL))
	lockPath := filepath.Join(repoInitLockDir, fmt.Sprintf(".init-%x.lock", sum[:16]))
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return fmt.Errorf("open repo-init lock %q: %w", lockPath, err)
	}
	defer lf.Close()

	deadline := time.Now().Add(repoInitLockMaxWait)
	for {
		flockErr := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if flockErr == nil {
			break
		}
		if flockErr != syscall.EWOULDBLOCK {
			return fmt.Errorf("acquire repo-init lock %q: %w", lockPath, flockErr)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("repo-init lock %q still held by another job after %s — the holding job is probably stuck probing or initialising this destination; check its job log",
				lockPath, repoInitLockMaxWait)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("repo-init lock %q: %w", lockPath, ctx.Err())
		case <-time.After(repoInitLockPoll):
		}
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	return fn()
}
