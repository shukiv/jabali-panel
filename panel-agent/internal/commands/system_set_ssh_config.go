package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// systemSetSSHConfigParams is the input shape for system.set_ssh_config.
//
// PasswordAuth governs the GLOBAL PasswordAuthentication directive in
// jabali-sshd.conf (affects root and any user not in the jabali-sftp
// group). UserPasswordAuth governs PasswordAuthentication / KbdInteractive-
// Authentication INSIDE the Match Group jabali-sftp block in jabali-sftp.conf
// (affects hosting users). The two are independent because of how sshd
// resolves Match blocks: first-match-wins inside the block scope.
type systemSetSSHConfigParams struct {
	Port             uint16 `json:"port"`
	PasswordAuth     bool   `json:"password_auth"`
	UserPasswordAuth bool   `json:"user_password_auth"`
}

// systemSetSSHConfigResponse is the output shape. Returns the values that
// were actually applied so the caller can confirm.
type systemSetSSHConfigResponse struct {
	Port             uint16 `json:"port"`
	PasswordAuth     bool   `json:"password_auth"`
	UserPasswordAuth bool   `json:"user_password_auth"`
}

// getSSHConfigPath returns the global drop-in (Port + global PasswordAuth).
func getSSHConfigPath() string {
	if p := os.Getenv("JABALI_SSHD_DROPIN_PATH"); p != "" {
		return p
	}
	return "/etc/ssh/sshd_config.d/jabali-sshd.conf"
}

// getSSHSftpDropinPath returns the M12 SFTP drop-in (Match Group jabali-sftp).
func getSSHSftpDropinPath() string {
	if p := os.Getenv("JABALI_SSHD_SFTP_DROPIN_PATH"); p != "" {
		return p
	}
	return "/etc/ssh/sshd_config.d/jabali-sftp.conf"
}

// renderSftpDropin produces the full M12 Match block, toggling password
// auth lines based on userPasswordAuth. The non-auth bits (ForceCommand,
// AllowTcpForwarding, etc.) are preserved verbatim from the install-time
// template — see install/ssh/jabali-sftp.conf.
func renderSftpDropin(userPasswordAuth bool) string {
	pwLine := "no"
	if userPasswordAuth {
		pwLine = "yes"
	}
	return fmt.Sprintf(`# Jabali Panel — SFTP access for users in the jabali-sftp group.
# MANAGED FILE — rewritten by panel-agent system.set_ssh_config.
# Initial template: install/ssh/jabali-sftp.conf

Match Group jabali-sftp
    # Enforce SFTP-only. No shell sessions for members of this group.
    ForceCommand internal-sftp

    # Disallow every non-file-transfer feature.
    AllowTcpForwarding no
    X11Forwarding no
    PermitTunnel no
    AllowAgentForwarding no

    # Lock user inside own home. Panel flips /home/<u> to root:<u> 0751
    # via ssh.user.home_chown(mode=sftp) before joining jabali-sftp.
    ChrootDirectory /home/%%u

    # Auth toggled by Server Settings → SSH Access → "User Password
    # Authentication". Default OFF (key-only) per project posture.
    PasswordAuthentication %s
    KbdInteractiveAuthentication %s
    PermitEmptyPasswords no

    # Log session activity with INFO — tells us who connected without per-file spam.
    ClientAliveInterval 300
    ClientAliveCountMax 2
`, pwLine, pwLine)
}

// renderGlobalDropin produces the global Port + PasswordAuthentication
// drop-in.
func renderGlobalDropin(port uint16, passwordAuth bool) string {
	pwLine := "no"
	if passwordAuth {
		pwLine = "yes"
	}
	return fmt.Sprintf("Port %d\nPasswordAuthentication %s\n", port, pwLine)
}

// atomicWrite writes content to path via a sibling .new file + rename.
// Best-effort cleanup of the .new file on rename failure.
func atomicWrite(path, content string) error {
	newPath := path + ".new"
	if err := os.WriteFile(newPath, []byte(content), 0600); err != nil {
		return fmt.Errorf("write %s: %w", newPath, err)
	}
	if err := os.Rename(newPath, path); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("rename %s -> %s: %w", newPath, path, err)
	}
	return nil
}

// restoreFile writes prevContent back to path; if prevContent is empty
// (file didn't exist before), removes the file. Errors are best-effort —
// rollback is a recovery path, not a primary one.
func restoreFile(path string, prevContent []byte) {
	if len(prevContent) > 0 {
		_ = os.WriteFile(path, prevContent, 0600)
		return
	}
	_ = os.Remove(path)
}

// systemSetSSHConfigHandler applies SSH port + global/user password-auth
// settings via two atomic drop-in writes. After both files are in place,
// runs sshd -t — if validation fails, BOTH files are restored from their
// previous contents before returning. On success, reloads sshd.
func systemSetSSHConfigHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p systemSetSSHConfigParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	if p.Port < 1 || p.Port > 65535 {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "port must be between 1 and 65535",
		}
	}

	globalPath := getSSHConfigPath()
	sftpPath := getSSHSftpDropinPath()

	// Snapshot prev contents BEFORE any write so rollback is exact.
	prevGlobal, _ := os.ReadFile(globalPath)
	prevSftp, _ := os.ReadFile(sftpPath)

	// Build + write the global drop-in first.
	if err := atomicWrite(globalPath, renderGlobalDropin(p.Port, p.PasswordAuth)); err != nil {
		return nil, err
	}

	// Build + write the SFTP drop-in. If this fails, roll back the
	// global write so we don't leave a partial state.
	if err := atomicWrite(sftpPath, renderSftpDropin(p.UserPasswordAuth)); err != nil {
		restoreFile(globalPath, prevGlobal)
		return nil, err
	}

	// Validate the combined config (sshd -t reads main config + all drop-ins).
	if os.Getenv("JABALI_SSHD_TEST_SKIP_VALIDATE") == "" {
		cmd := execCommandContext(ctx, "sshd", "-t")
		if out, err := cmd.CombinedOutput(); err != nil {
			restoreFile(globalPath, prevGlobal)
			restoreFile(sftpPath, prevSftp)
			return nil, fmt.Errorf("sshd -t validation failed: %s: %w", strings.TrimSpace(string(out)), err)
		}
	}

	// Reload the SSH daemon. Debian/Ubuntu ship the unit as ssh.service;
	// RHEL/Rocky ship it as sshd.service. Pick whichever exists rather
	// than hard-coding sshd, which was breaking jabali update on
	// Debian 13. If reload fails the validated config stays on disk —
	// a re-attempt or manual reload by the operator will pick it up.
	if os.Getenv("JABALI_SSHD_TEST_SKIP_RELOAD") == "" {
		unit := pickSSHUnit(ctx)
		if unit == "" {
			return nil, fmt.Errorf("no ssh/sshd systemd unit found")
		}
		cmd := execCommandContext(ctx, "systemctl", "reload", unit)
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("systemctl reload %s: %s: %w", unit, strings.TrimSpace(string(out)), err)
		}
	}

	return systemSetSSHConfigResponse{
		Port:             p.Port,
		PasswordAuth:     p.PasswordAuth,
		UserPasswordAuth: p.UserPasswordAuth,
	}, nil
}

// pickSSHUnit returns "ssh" on Debian-family hosts and "sshd" on
// RHEL-family hosts, by asking systemd which unit file exists. Returns
// "" if neither is present (caller decides whether to fail or skip).
func pickSSHUnit(ctx context.Context) string {
	for _, name := range []string{"ssh", "sshd"} {
		cmd := execCommandContext(ctx, "systemctl", "list-unit-files", name+".service")
		out, err := cmd.CombinedOutput()
		if err != nil {
			continue
		}
		// list-unit-files prints a header even when the unit is missing;
		// look for the unit name itself in the output.
		if strings.Contains(string(out), name+".service") {
			return name
		}
	}
	return ""
}

func init() {
	Default.Register("system.set_ssh_config", systemSetSSHConfigHandler)
}
