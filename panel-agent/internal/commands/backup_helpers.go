package commands

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

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

// bkInternal wraps an internal-error envelope.
func bkInternal(msg string, err error) error {
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
	repoProbeOther      repoProbeClass = iota // unknown failure — surface raw
	repoProbeMissing                          // no repo at the location → init
	repoProbeUnopenable                       // repo exists but can't be decrypted/read
)

// classifyRepoProbe maps a lowercased restic stderr to a repoProbeClass. Strings
// are the verbatim messages restic 0.16 emits (captured against 0.16.4):
//   - missing:    "unable to open config file: … no such file …" / "repository does not exist"
//   - unopenable: "wrong password or no key found" (foreign/rotated password),
//                 "config or key <id> is damaged: ciphertext verification failed" (corrupt/foreign)
//
// Order matters: the missing case also contains "config file", so check the
// unopenable signals (which never co-occur with a missing repo) first is not
// required — the missing markers are specific — but we check missing explicitly.
func classifyRepoProbe(lowerStderr string) repoProbeClass {
	if strings.Contains(lowerStderr, "wrong password or no key found") ||
		strings.Contains(lowerStderr, "ciphertext verification failed") ||
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

// bkEnsureRepoReady probes the remote and runs mkdir -p (SFTP only) +
// `restic init` if the repo doesn't exist yet. Idempotent — succeeds
// on already-initialized repos. Local destinations get the parent dir
// created if missing; failures bubble up.
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
	_, snapStderr, snapErr := backup.SnapshotsRemote(ctx, nil, repoURL, pwFile, extraEnv, bkResticOptions(sftp))
	if snapErr == nil {
		return nil
	}
	lower := strings.ToLower(strings.TrimSpace(string(snapStderr)))
	switch classifyRepoProbe(lower) {
	case repoProbeUnopenable:
		// A repository that EXISTS but cannot be opened (GH #454). Actionable
		// message instead of the raw restic dump — see classifyRepoProbe.
		return fmt.Errorf("backup repository at %q exists but this server cannot open it (%s).\n"+
			"This usually means the server (or /etc/jabali-panel/restic-repo.password) was reinstalled or "+
			"regenerated while the repository directory was preserved, so the snapshots are sealed with a "+
			"password this server no longer has (or the repo's config/key files are corrupted). To recover: "+
			"(a) restore the ORIGINAL /etc/jabali-panel/restic-repo.password from before the reinstall, or "+
			"(b) point this backup destination at a FRESH empty directory to start a new repository — the old "+
			"snapshots stay on disk but are unreadable without their original password.",
			repoURL, lower)
	case repoProbeOther:
		// Not a missing-repo signal, not a known unopenable signal — surface raw.
		return fmt.Errorf("snapshots probe: %w (stderr: %s)", snapErr, lower)
	}
	// repoProbeMissing → fall through to init below.
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
}
