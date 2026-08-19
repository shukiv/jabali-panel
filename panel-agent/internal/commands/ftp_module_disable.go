package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// ftp.disable — FAIL-CLOSED FTP module shutoff (JAB-259).
//
// The generic system.module.disable is deliberately fail-soft (a stubborn unit
// must not strand the others already stopped) and never masks the daemon or
// closes its firewall rules — correct for dns/mail/quota/security, wrong for a
// security shutoff. FTP gets this dedicated verb: stop -> mask -> remove the UFW
// rules -> VERIFY the daemon is inactive and masked, returning a typed
// failed_precondition when it is not, so the control plane can never report FTP
// "off" while vsftpd is still able to accept connections.
//
// Idempotent and safe to re-run every reconcile tick (the reconciler's
// convergeFtpDisabled path calls this until it succeeds).
const (
	ftpDisableUnit = "vsftpd"
	// ftpPasvRangeRule + ftpControlPortRule MUST match the rules install.sh
	// opens in install_ftp_firewall_rules and closes in converge_ftp_masking.
	ftpControlPortRule = "21/tcp"
	ftpPasvRangeRule   = "40000:40100/tcp"
	// ftpSessionLeafName is the per-tenant leaf cgroup the pam_exec hook
	// (JAB-263 phase D) moves FTPS workers into, under jabali-user-<t>.slice.
	// MUST match install/ftp/jabali-ftp-session-cgroup.
	ftpSessionLeafName   = "ftp-sessions"
	ftpCgroupRootDefault = "/sys/fs/cgroup/jabali.slice"
)

// ftpCgroupRoot is the cgroup subtree swept for ftp-sessions leaves. Overridable
// for tests (a real /sys/fs/cgroup can't be faked).
func ftpCgroupRoot() string {
	if r := os.Getenv("JABALI_FTP_CGROUP_ROOT"); r != "" {
		return r
	}
	return ftpCgroupRootDefault
}

type ftpDisableResponse struct {
	Active    bool `json:"active"`
	Masked    bool `json:"masked"`
	PortsOpen bool `json:"ports_open"`
}

func ftpModuleDisableHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	present := unitExists(ctx, ftpDisableUnit)
	if present {
		// Mask (not just disable): a masked unit cannot be started by a
		// dependency or a stray reconciler install — stronger than the generic
		// verb's `disable --now`.
		_, _ = execCommandContext(ctx, "systemctl", "stop", ftpDisableUnit).CombinedOutput()
		_, _ = execCommandContext(ctx, "systemctl", "mask", ftpDisableUnit).CombinedOutput()
	}
	// JAB-263 phase D: the pam_exec hook moves live FTPS workers OUT of
	// vsftpd.service into per-tenant ftp-sessions leaf cgroups, so `stop vsftpd`
	// above does NOT kill an in-flight transfer. Cut them too, or 259's "vsftpd
	// cannot accept connections" silently weakens to "cannot accept NEW ones".
	killFtpSessionLeaves()
	// Remove the firewall rules (best-effort; ufw may be absent/inactive). The
	// deletes are idempotent — `ufw delete` on a missing rule is a no-op.
	_, _ = execCommandContext(ctx, "ufw", "delete", "allow", ftpControlPortRule).CombinedOutput()
	_, _ = execCommandContext(ctx, "ufw", "delete", "allow", ftpPasvRangeRule).CombinedOutput()

	resp := ftpDisableResponse{
		Active:    ftpUnitActive(ctx, ftpDisableUnit),
		Masked:    ftpUnitMasked(ctx, ftpDisableUnit),
		PortsOpen: ftpControlPortOpen(ctx),
	}

	// The security invariant is "vsftpd cannot accept connections", guaranteed
	// by inactive AND masked (a masked+dead daemon behind a stray ufw rule is
	// not reachable). Fail closed on either; report ports_open for Phase C to
	// surface a lingering rule but don't hot-loop the reconciler on a cosmetic
	// ufw quirk when the daemon is already dead.
	if resp.Active || (present && !resp.Masked) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("ftp still exposed after disable: active=%v masked=%v ports_open=%v", resp.Active, resp.Masked, resp.PortsOpen),
		}
	}
	return resp, nil
}

// ftpUnitActive reports whether the unit is currently running.
func ftpUnitActive(ctx context.Context, unit string) bool {
	out, _ := execCommandContext(ctx, "systemctl", "is-active", unit).CombinedOutput()
	return strings.TrimSpace(string(out)) == "active"
}

// ftpUnitMasked reports whether the unit is masked. `systemctl is-enabled`
// prints "masked" (with a non-zero exit) for a masked unit.
func ftpUnitMasked(ctx context.Context, unit string) bool {
	out, _ := execCommandContext(ctx, "systemctl", "is-enabled", unit).CombinedOutput()
	return strings.TrimSpace(string(out)) == "masked"
}

// ftpControlPortOpen reports whether ufw still allows the FTP control port.
// A missing/inactive ufw (error) means nothing we manage is open.
func ftpControlPortOpen(ctx context.Context) bool {
	out, err := execCommandContext(ctx, "ufw", "status").CombinedOutput()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == ftpControlPortRule && strings.EqualFold(fields[1], "ALLOW") {
			return true
		}
	}
	return false
}

// killFtpSessionLeaves kills every process in every per-tenant ftp-sessions leaf
// cgroup (JAB-263 phase D) via the cgroup-v2 cgroup.kill file, then removes the
// emptied leaf (it is recreated by the pam_exec hook on the next login).
// Best-effort: a walk/write failure just leaves that tenant's workers, which
// degrades to pre-phase-D behavior, never worse.
func killFtpSessionLeaves() {
	_ = filepath.WalkDir(ftpCgroupRoot(), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable subtree — skip, don't abort the whole walk
		}
		if !d.IsDir() || d.Name() != ftpSessionLeafName {
			return nil
		}
		// cgroup.kill (Linux 5.14+) SIGKILLs every process in the cgroup at once.
		_ = os.WriteFile(filepath.Join(path, "cgroup.kill"), []byte("1"), 0o644)
		_ = os.Remove(path) // rmdir the now-empty leaf
		return filepath.SkipDir
	})
}

func init() {
	Default.Register("ftp.disable", ftpModuleDisableHandler)
}
