package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// ftpaccount.sshd_sync — render the per-subaccount SFTP Match blocks into
// /etc/ssh/sshd_config.d/jabali-xfer.conf (GH #1053 step 3).
//
// Subaccounts cannot ride M12's `Match Group jabali-sftp` block: its
// `ChrootDirectory /home/%u` expands to a home named after the SUBACCOUNT
// username, which doesn't exist — the failed chroot resets every
// connection. Each subaccount instead gets its own `Match User` block that
// chroots to the OWNING TENANT's home (root-owned 0751 per the M12 layout,
// which is what sshd's chroot ownership check demands) and starts the
// session in the account's home_path via `internal-sftp -d`.
//
// The panel sends the full desired set every time; the file is rendered
// from scratch (never patched), validated with `sshd -t`, rolled back on
// failure, and sshd reloaded only when content actually changed — same
// contract as system.set_ssh_config in this package.

const sshXferDropinDefault = "/etc/ssh/sshd_config.d/jabali-xfer.conf"

func getSSHXferDropinPath() string {
	if p := os.Getenv("JABALI_SSHD_XFER_DROPIN_PATH"); p != "" {
		return p
	}
	return sshXferDropinDefault
}

type ftpSSHDSyncAccount struct {
	Username string `json:"username"`
	// ChrootDir is the tenant home (/home/<tenant>) — root-owned per M12.
	ChrootDir string `json:"chroot_dir"`
	// StartDir is the session start directory RELATIVE TO the chroot,
	// always beginning with "/" ("/" = the chroot root itself).
	StartDir string `json:"start_dir"`
}

type ftpSSHDSyncParams struct {
	Accounts []ftpSSHDSyncAccount `json:"accounts"`
	// Generation is a monotonic sequence the panel assigns when it reads the
	// desired snapshot (JAB-267). The agent applies syncs in generation order
	// and DROPS any whose generation is older than the last one it applied, so
	// a delayed stale snapshot can never overwrite a newer one and resurrect a
	// revoked Match block (which — because disabling sftp_access does not lock
	// the password — would silently restore working password SFTP access).
	// 0 = unversioned (older panel): apply ungated, preserving prior behavior.
	Generation int64 `json:"generation"`
}

type ftpSSHDSyncResponse struct {
	Accounts int  `json:"accounts"`
	Changed  bool `json:"changed"`
	// Stale is true when the sync was dropped because its generation was older
	// than the last applied — a correct no-op, not an error.
	Stale bool `json:"stale,omitempty"`
}

// lastXferSyncGen is the highest generation applied to the xfer drop-in.
// Guarded by sshOpMu — read and written only inside the locked critical
// section below. In-memory on purpose: an agent restart drops every in-flight
// UDS call, so no stale sync outlives the reset, and the reconciler re-syncs
// current DB truth on its next pass regardless.
var lastXferSyncGen int64

// ftpXferUsernameRegex is deliberately stricter than usernameRegex: a
// rendered name lands inside sshd config, so beyond POSIX validity it must
// contain the "_" namespace separator every subaccount carries.
var ftpXferUsernameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

func validateFtpSSHDAccount(a ftpSSHDSyncAccount) *agentwire.AgentError {
	if !ftpXferUsernameRegex.MatchString(a.Username) || !strings.Contains(a.Username, "_") {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid subaccount username %q for sshd render", a.Username),
		}
	}
	// sshdConfigUnsafe rejects anything that could split or terminate a
	// rendered directive: whitespace breaks `internal-sftp -d <path>` into
	// extra argv, newlines inject new directives, quotes/backslashes
	// change sshd's tokenization. Docroot paths never need any of them.
	sshdConfigUnsafe := " \t\r\n\"'\\"
	// Legacy same-uid accounts chroot to the tenant home (/home/<tenant>);
	// GH #1145 isolated accounts chroot to their root-owned jail under
	// ftpJailRoot(). Both are root-owned chroot roots that satisfy sshd's
	// ownership-chain rule; anything else is rejected.
	chrootOK := strings.HasPrefix(a.ChrootDir, "/home/") ||
		strings.HasPrefix(a.ChrootDir, ftpJailRoot()+"/")
	if !chrootOK ||
		filepath.Clean(a.ChrootDir) != a.ChrootDir ||
		strings.ContainsAny(a.ChrootDir, sshdConfigUnsafe) {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid chroot_dir %q: must be a clean absolute path under /home or the FTP jail root, without whitespace or quotes", a.ChrootDir),
		}
	}
	if !strings.HasPrefix(a.StartDir, "/") ||
		filepath.Clean(a.StartDir) != a.StartDir ||
		strings.ContainsAny(a.StartDir, sshdConfigUnsafe) {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid start_dir %q: must be a clean chroot-relative path starting with / without whitespace or quotes", a.StartDir),
		}
	}
	return nil
}

// renderXferDropin produces the whole drop-in. Deterministic (accounts
// sorted by username) so content comparison is a meaningful change gate.
func renderXferDropin(accounts []ftpSSHDSyncAccount) string {
	var b strings.Builder
	b.WriteString(`# Jabali Panel — SFTP access for tenant FTP/SFTP subaccounts (GH #1053).
# RENDERED by the panel reconciler from the ftp_accounts table — do not
# edit in place; changes are overwritten on the next reconcile tick.
#
# Subaccounts are same-uid aliases of their tenant. They are NOT in the
# jabali-sftp group (its /home/%u chroot cannot fit an alias username);
# each gets a Match User block chrooting to the TENANT home instead.
`)
	sorted := append([]ftpSSHDSyncAccount(nil), accounts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Username < sorted[j].Username })
	for _, a := range sorted {
		b.WriteString("\nMatch User " + a.Username + "\n")
		// -u 0007 (JAB-264): strip OTHER read/traverse bits from uploaded
		// files/dirs, matching vsftpd's local_umask and the ADR-0030 privacy
		// model (files 0660, dirs 0770; group kept for nginx). -d: start the
		// session in the account's directory (not a boundary — that is the
		// chroot).
		b.WriteString("    ForceCommand internal-sftp -u 0007 -d " + a.StartDir + "\n")
		b.WriteString("    ChrootDirectory " + a.ChrootDir + "\n")
		b.WriteString("    AllowTcpForwarding no\n")
		b.WriteString("    X11Forwarding no\n")
		b.WriteString("    PermitTunnel no\n")
		b.WriteString("    AllowAgentForwarding no\n")
		// Subaccounts are password credentials by design (revocable,
		// per-protocol). PubkeyAuthentication MUST be explicitly off, not
		// left to the global default: sshd reads ~/.ssh/authorized_keys
		// from the alias's REAL home — home_path, typically a docroot —
		// BEFORE chroot. A tenant (or a compromised WordPress) writing
		// docroot/.ssh/authorized_keys would otherwise mint key-based
		// alias access that survives every password rotation.
		b.WriteString("    PasswordAuthentication yes\n")
		b.WriteString("    PubkeyAuthentication no\n")
		b.WriteString("    KbdInteractiveAuthentication no\n")
		b.WriteString("    PermitEmptyPasswords no\n")
	}
	return b.String()
}

func ftpSSHDSyncHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p ftpSSHDSyncParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
	}
	seen := map[string]struct{}{}
	for _, a := range p.Accounts {
		if aerr := validateFtpSSHDAccount(a); aerr != nil {
			return nil, aerr
		}
		if _, dup := seen[a.Username]; dup {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("duplicate account %q", a.Username)}
		}
		seen[a.Username] = struct{}{}
	}

	// The whole write→sshd -t→reload sequence, plus the generation high-water
	// read/advance, runs under sshOpMu. The lock is shared with
	// system.set_ssh_config because `sshd -t` validates the COMBINED config —
	// a concurrent writer to another drop-in would otherwise invalidate a test
	// this handler already passed. Serialization alone cannot reject an
	// already-stale snapshot, so the generation gate below does that (JAB-267).
	sshOpMu.Lock()
	defer sshOpMu.Unlock()

	if p.Generation > 0 && p.Generation < lastXferSyncGen {
		slog.WarnContext(ctx, "ftp sshd_sync: dropping stale snapshot",
			"generation", p.Generation, "last_applied", lastXferSyncGen, "accounts", len(p.Accounts))
		return ftpSSHDSyncResponse{Accounts: len(p.Accounts), Changed: false, Stale: true}, nil
	}

	path := getSSHXferDropinPath()
	desired := renderXferDropin(p.Accounts)
	prev, readErr := os.ReadFile(path)
	if readErr == nil && string(prev) == desired {
		// Already at the desired content: this generation is applied (no-op).
		// Advance the high-water so a later stale snapshot can't win.
		if p.Generation > lastXferSyncGen {
			lastXferSyncGen = p.Generation
		}
		return ftpSSHDSyncResponse{Accounts: len(p.Accounts), Changed: false}, nil
	}

	if err := atomicWrite(path, desired); err != nil {
		return nil, err
	}
	// sshd -t validates main config + every drop-in. On failure restore
	// the previous content — a broken sshd config is a host lockout.
	if os.Getenv("JABALI_SSHD_TEST_SKIP_VALIDATE") == "" {
		if out, err := execCommandContext(ctx, "sshd", "-t").CombinedOutput(); err != nil {
			restoreFile(path, prev)
			return nil, fmt.Errorf("sshd -t validation failed: %s: %w", strings.TrimSpace(string(out)), err)
		}
	}
	// The desired content is now validated and durably on disk. Advance the
	// high-water HERE, before the reload — a reload failure must not leave newer
	// on-disk content unprotected against an older in-flight sync overwriting
	// it. The reconciler re-issues the reload on its next pass with a fresh,
	// higher generation.
	if p.Generation > lastXferSyncGen {
		lastXferSyncGen = p.Generation
	}
	if os.Getenv("JABALI_SSHD_TEST_SKIP_RELOAD") == "" {
		unit := pickSSHUnit(ctx)
		if unit == "" {
			return nil, fmt.Errorf("no ssh/sshd systemd unit found")
		}
		if out, err := execCommandContext(ctx, "systemctl", "reload", unit).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("systemctl reload %s: %s: %w", unit, strings.TrimSpace(string(out)), err)
		}
	}
	return ftpSSHDSyncResponse{Accounts: len(p.Accounts), Changed: true}, nil
}

func init() {
	Default.Register("ftpaccount.sshd_sync", ftpSSHDSyncHandler)
}
