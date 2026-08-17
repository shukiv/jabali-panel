package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// GH #1145 / JAB-252 + JAB-253 — TRUE (separate-uid) isolation for an FTP/SFTP
// subaccount.
//
// The legacy model (ftp_account.go) makes a subaccount a same-uid alias
// chrooted to the tenant home: an SFTP credential scoped to a sub-dir can `..`
// out to the whole account (JAB-252), and a mutable-home symlink escapes the
// vsftpd chroot (JAB-253). An ISOLATED subaccount instead gets:
//
//   - its OWN uid (allocated monotonically by the panel, never reused) and its
//     own primary group — kernel identity separation from the tenant AND from
//     sibling subaccounts;
//   - a ROOT-OWNED jail dir (/var/lib/jabali-ftp-jails/<tenant>/<label>,
//     root:root 0755) whose ONLY writable content is the selected sub-tree,
//     bind-mounted in at a root-owned mountpoint. The jail satisfies sshd's
//     root-owned-chroot-chain rule AND vsftpd's secure-chroot rule, so it is
//     the chroot for BOTH protocols; `..` from the mountpoint hits the empty
//     root-owned jail root, never a sibling or ancestor;
//   - POSIX ACLs granting the sub-uid AND the tenant-uid rwX on the exposed
//     sub-tree (with default ACLs so new files inherit), so the tenant keeps
//     managing files the sub writes under its own uid;
//   - a per-uid disk quota (the split allocation) so a separate uid cannot
//     escape the tenant's package quota and fill the disk.
//
// The mount is a plain bind mount created by the agent. The agent runs as root
// in the HOST mount namespace (install.sh keeps PrivateTmp / ProtectKernel* /
// PrivateMounts OFF for it — proven on jabalitests), so the bind mount is
// host-global and visible to a fresh sshd/vsftpd login with no namespace-escape
// machinery. It does NOT survive reboot; the panel reconciler re-establishes it
// each tick (DB-is-truth), and a boot oneshot converges before first login.

// ftpJailRootDefault is the parent of every per-subaccount jail. Root-owned,
// outside any tenant-writable tree.
const ftpJailRootDefault = "/var/lib/jabali-ftp-jails"

// ftpJailMountpoint is the fixed name of the bind-mount target inside each
// jail. The session start dir is always "/<ftpJailMountpoint>".
const ftpJailMountpoint = "data"

// ftpSubaccountUIDMin is the floor of the reserved, never-reused uid range for
// isolated subaccounts (must match migration 000267's allocator base). Well
// above tenant uids and the systemd DynamicUser range (61184-65519) and
// nobody (65534). The agent re-checks this floor — it never trusts the panel's
// allocated uid blindly.
const ftpSubaccountUIDMin = 500000

func ftpJailRoot() string {
	if p := os.Getenv("JABALI_FTP_JAIL_ROOT"); p != "" {
		return p
	}
	return ftpJailRootDefault
}

// ftpJailPathFor derives the canonical jail path for a subaccount. The panel
// stores and sends the same value (jail_path); the agent re-derives and
// requires an exact match so a caller can never point the chroot elsewhere.
func ftpJailPathFor(tenant *ftpTenant, username string) string {
	return filepath.Join(ftpJailRoot(), tenant.Username, username)
}

// validateIsolatedCreate re-validates every panel-supplied field for the
// isolated path. The agent never trusts the panel just because it is localhost.
func validateIsolatedCreate(p ftpAccountCreateParams, tenant *ftpTenant) *agentwire.AgentError {
	if p.UID < ftpSubaccountUIDMin {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("isolated uid %d is below the reserved floor %d", p.UID, ftpSubaccountUIDMin),
		}
	}
	if int(p.UID) == tenant.UID {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("isolated uid %d collides with the tenant uid", p.UID),
		}
	}
	if p.QuotaMB == 0 {
		// An unlimited separate uid escapes the tenant's package quota and can
		// fill the disk — the disk-fill hole the split allocation closes.
		return &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "isolated account requires a non-zero quota_mb (unlimited separate uid = disk-fill hole)",
		}
	}
	if p.QuotaMount == "" {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "isolated account requires quota_mount for per-uid setquota",
		}
	}
	// The panel-supplied jail_path must be EXACTLY the canonical path — no
	// caller-chosen chroot root.
	want := ftpJailPathFor(tenant, p.Username)
	if p.JailPath != want {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("jail_path %q is not the canonical jail %q", p.JailPath, want),
		}
	}
	return nil
}

// resolveIsolatedTarget returns the real, symlink-resolved path of the selected
// sub-tree beneath the tenant home. Unlike the legacy path (which passes the
// unresolved home_path to useradd), the jail bind-mounts the RESOLVED inode, so
// a later symlink retarget of home_path cannot move the live mount (JAB-253).
// The directory must already exist — the panel creates it via the confined
// scope before provisioning the jail.
func resolveIsolatedTarget(homePath string, tenant *ftpTenant) (string, *agentwire.AgentError) {
	resolved, err := filepath.EvalSymlinks(homePath)
	if err != nil {
		return "", &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("resolve selected dir %q: %v", homePath, err),
		}
	}
	rel, rerr := filepath.Rel(tenant.HomeDir, resolved)
	if rerr != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", &agentwire.AgentError{
			Code:    agentwire.CodePermissionDenied,
			Message: fmt.Sprintf("selected dir %q resolves outside the tenant home %q", homePath, tenant.HomeDir),
		}
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("selected dir %q is not an existing directory", homePath),
		}
	}
	return resolved, nil
}

// provisionIsolatedJail builds the full isolated account: jail skeleton, bind
// mount, own-uid user, ACL grant, and per-uid quota. It returns a rollback that
// undoes every step performed so far; the caller runs it on any later failure.
// The mountpoint-relative start dir is always "/<ftpJailMountpoint>".
func provisionIsolatedJail(ctx context.Context, tenant *ftpTenant, p ftpAccountCreateParams) (rollback func(), aerr *agentwire.AgentError) {
	if aerr := validateIsolatedCreate(p, tenant); aerr != nil {
		return nil, aerr
	}
	target, aerr := resolveIsolatedTarget(p.HomePath, tenant)
	if aerr != nil {
		return nil, aerr
	}

	jail := p.JailPath
	mountpoint := filepath.Join(jail, ftpJailMountpoint)

	var undo []func()
	rollback = func() {
		// Reverse order.
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
	}
	fail := func(e *agentwire.AgentError) (func(), *agentwire.AgentError) {
		rollback()
		return nil, e
	}

	// 1. Root-owned jail skeleton (root:root 0755). MkdirAll is safe here —
	// the jail root is outside any tenant-writable tree, so no symlink race.
	if err := os.MkdirAll(mountpoint, 0o755); err != nil {
		return fail(&agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("create jail %q: %v", mountpoint, err)})
	}
	// MkdirAll may have created several ancestors; remove the per-account jail
	// dir on rollback (the /<tenant> parent is shared, left in place).
	undo = append(undo, func() { _ = os.RemoveAll(jail) })
	if err := os.Chown(jail, 0, 0); err != nil {
		return fail(&agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chown jail %q root: %v", jail, err)})
	}
	if err := os.Chmod(jail, 0o755); err != nil {
		return fail(&agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chmod jail %q: %v", jail, err)})
	}
	// The mountpoint must be root-owned too: sshd/vsftpd chroot to the jail
	// root, and the mountpoint is a child of it, but keeping it root:root 0755
	// means a failed/absent mount exposes an EMPTY root-owned dir, never
	// tenant data by accident.
	if err := os.Chown(mountpoint, 0, 0); err != nil {
		return fail(&agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chown mountpoint %q root: %v", mountpoint, err)})
	}

	// 2. Bind-mount the resolved sub-tree into the jail (host namespace).
	if err := syscall.Mount(target, mountpoint, "", syscall.MS_BIND, ""); err != nil {
		return fail(&agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("bind-mount %q -> %q: %v", target, mountpoint, err)})
	}
	undo = append(undo, func() { _ = syscall.Unmount(mountpoint, 0) })

	// 3. Own-uid user (NO --non-unique), own primary group, nologin, GECOS
	// marker, passwd home = jail (so vsftpd chroots to the jail, not a
	// tenant-mutable path). --no-create-home: the jail already exists.
	uidStr := strconv.FormatUint(uint64(p.UID), 10)
	useraddArgs := []string{
		"--uid", uidStr,
		"--user-group", // own primary group — member of NO tenant group
		"--home-dir", jail,
		"--no-create-home",
		"--shell", "/usr/sbin/nologin",
		"--comment", ftpAliasGecos,
		p.Username,
	}
	if out, err := exec.CommandContext(ctx, "useradd", useraddArgs...).CombinedOutput(); err != nil {
		return fail(&agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("useradd (isolated) %q: %v: %s", p.Username, err, strings.TrimSpace(string(out)))})
	}
	undo = append(undo, func() { _ = exec.CommandContext(ctx, "userdel", "-f", p.Username).Run() })

	// 4. ACLs on the EXPOSED sub-tree: the sub-uid must read/write it, and the
	// tenant-uid must keep managing files the sub writes under its own uid.
	// Default ACLs (-d) make new files inherit both grants. Applied to the
	// resolved target (which the bind mount reflects). Uppercase X = execute
	// only where already applicable (dirs / already-exec files).
	spec := fmt.Sprintf("u:%d:rwX,u:%d:rwX", p.UID, tenant.UID)
	if out, err := exec.CommandContext(ctx, "setfacl", "-R", "-m", spec, target).CombinedOutput(); err != nil {
		return fail(&agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("setfacl -R %q: %v: %s", target, err, strings.TrimSpace(string(out)))})
	}
	if out, err := exec.CommandContext(ctx, "setfacl", "-dR", "-m", spec, target).CombinedOutput(); err != nil {
		return fail(&agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("setfacl -dR %q: %v: %s", target, err, strings.TrimSpace(string(out)))})
	}
	// No ACL rollback: the grants are additive and harmless if the account is
	// torn down (the uid is retired and never reused); the reaper's later
	// setquota-0/userdel path handles cleanup.

	// 5. Per-uid quota (the split cap). setquota block counts are 1KB units.
	blocks := strconv.FormatUint(uint64(p.QuotaMB)*1024, 10)
	if out, err := exec.CommandContext(ctx, "setquota", "-u", p.Username, blocks, blocks, "0", "0", p.QuotaMount).CombinedOutput(); err != nil {
		return fail(&agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("setquota (isolated) %q: %v: %s", p.Username, err, strings.TrimSpace(string(out)))})
	}

	return rollback, nil
}

// teardownIsolatedJail unmounts and removes a subaccount jail. Used by the
// delete verb and the reconciler's fail-closed path. Idempotent: a missing
// mount or dir is not an error. It NEVER touches the bind-mount SOURCE (tenant
// data) — only the jail overlay.
func teardownIsolatedJail(ctx context.Context, jailPath string) *agentwire.AgentError {
	if jailPath == "" {
		return nil
	}
	mountpoint := filepath.Join(jailPath, ftpJailMountpoint)
	// Unmount if mounted; MNT_DETACH so a busy mount (live session) still
	// releases and the kernel finalizes when the last ref drops.
	if isMountpoint(mountpoint) {
		if err := syscall.Unmount(mountpoint, syscall.MNT_DETACH); err != nil {
			return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("unmount jail %q: %v", mountpoint, err)}
		}
	}
	if err := os.RemoveAll(jailPath); err != nil {
		return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("remove jail %q: %v", jailPath, err)}
	}
	return nil
}

// isMountpoint reports whether path is a mount point by comparing its device
// number to its parent's (a bind mount changes st_dev at the mountpoint).
func isMountpoint(path string) bool {
	var st, parent syscall.Stat_t
	if err := syscall.Lstat(path, &st); err != nil {
		return false
	}
	if err := syscall.Lstat(filepath.Dir(path), &parent); err != nil {
		return false
	}
	return st.Dev != parent.Dev
}
