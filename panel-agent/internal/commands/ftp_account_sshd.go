package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
}

type ftpSSHDSyncResponse struct {
	Accounts int  `json:"accounts"`
	Changed  bool `json:"changed"`
}

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
	if !strings.HasPrefix(a.ChrootDir, "/home/") ||
		filepath.Clean(a.ChrootDir) != a.ChrootDir ||
		strings.ContainsAny(a.ChrootDir, sshdConfigUnsafe) {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid chroot_dir %q: must be a clean absolute path under /home without whitespace or quotes", a.ChrootDir),
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
		// -d: start the session in the account's directory. Not a
		// security boundary (the alias shares the tenant uid); the
		// boundary is the tenant-home chroot.
		b.WriteString("    ForceCommand internal-sftp -d " + a.StartDir + "\n")
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

	path := getSSHXferDropinPath()
	desired := renderXferDropin(p.Accounts)
	prev, readErr := os.ReadFile(path)
	if readErr == nil && string(prev) == desired {
		return ftpSSHDSyncResponse{Accounts: len(p.Accounts), Changed: false}, nil
	}

	if err := atomicWrite(path, desired); err != nil {
		return nil, err
	}
	// sshd -t validates main config + every drop-in. On failure restore
	// the previous content — a broken sshd config is a host lockout.
	if os.Getenv("JABALI_SSHD_TEST_SKIP_VALIDATE") == "" {
		if out, err := exec.CommandContext(ctx, "sshd", "-t").CombinedOutput(); err != nil {
			restoreFile(path, prev)
			return nil, fmt.Errorf("sshd -t validation failed: %s: %w", strings.TrimSpace(string(out)), err)
		}
	}
	if os.Getenv("JABALI_SSHD_TEST_SKIP_RELOAD") == "" {
		unit := pickSSHUnit(ctx)
		if unit == "" {
			return nil, fmt.Errorf("no ssh/sshd systemd unit found")
		}
		if out, err := exec.CommandContext(ctx, "systemctl", "reload", unit).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("systemctl reload %s: %s: %w", unit, strings.TrimSpace(string(out)), err)
		}
	}
	return ftpSSHDSyncResponse{Accounts: len(p.Accounts), Changed: true}, nil
}

func init() {
	Default.Register("ftpaccount.sshd_sync", ftpSSHDSyncHandler)
}
