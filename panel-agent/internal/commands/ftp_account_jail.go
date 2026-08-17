package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
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

// provisionIsolatedJail builds the full isolated account: jail skeleton, bind
// mount, own-uid user, ACL grant, and per-uid quota. It returns a rollback that
// undoes every step performed so far; the caller runs it on any later failure.
// The mountpoint-relative start dir is always "/<ftpJailMountpoint>".
func provisionIsolatedJail(ctx context.Context, tenant *ftpTenant, p ftpAccountCreateParams) (rollback func(), aerr *agentwire.AgentError) {
	if aerr := validateIsolatedCreate(p, tenant); aerr != nil {
		return nil, aerr
	}

	jail := p.JailPath
	mountpoint := filepath.Join(jail, ftpJailMountpoint)

	var undo []func()
	rollback = func() {
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
	// Single undo for the whole jail+mount: teardownIsolatedJail unmounts
	// safely (blind unmount-until-EINVAL) BEFORE removing the tree, so
	// rollback can never RemoveAll through a live bind mount into tenant data.
	undo = append(undo, func() { _ = teardownIsolatedJail(ctx, jail) })
	if err := os.Chown(jail, 0, 0); err != nil {
		return fail(&agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chown jail %q root: %v", jail, err)})
	}
	if err := os.Chmod(jail, 0o755); err != nil {
		return fail(&agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chmod jail %q: %v", jail, err)})
	}
	// Mountpoint must be root-owned too: a failed/absent mount then exposes an
	// EMPTY root-owned dir, never tenant data by accident (fail-closed).
	if err := os.Chown(mountpoint, 0, 0); err != nil {
		return fail(&agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chown mountpoint %q root: %v", mountpoint, err)})
	}

	// 2. Bind-mount the selected sub-tree into the jail (host namespace).
	// CRITICAL (JAB-251-class): the tenant home is tenant-writable, so the
	// selected pathname must NOT be re-resolved at mount time — a swapped
	// symlink component would bind an arbitrary directory (e.g. /etc) into the
	// jail. Open the dir escape-proof via openat2(RESOLVE_BENEATH) and pin it
	// by fd, then bind the fd's inode through /proc/self/fd. The kernel
	// resolves the magic symlink to the pinned inode with no pathname walk.
	scope, serr := filesafe.NewScope(tenant.Username, tenant.Username, []string{tenant.HomeDir})
	if serr != nil {
		return fail(&agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("home scope for %q: %v", tenant.HomeDir, serr)})
	}
	srcFd, oerr := scope.OpenDirInScope(p.HomePath)
	if oerr != nil {
		return fail(&agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("confined open of selected dir %q: %v", p.HomePath, oerr)})
	}
	srcMagic := fmt.Sprintf("/proc/self/fd/%d", srcFd.Fd())
	if err := syscall.Mount(srcMagic, mountpoint, "", syscall.MS_BIND, ""); err != nil {
		srcFd.Close()
		return fail(&agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("bind-mount selected dir -> %q: %v", mountpoint, err)})
	}
	// The mount holds its own ref to the inode; the fd is no longer needed.
	srcFd.Close()

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
		"--comment", ftpAliasGecosFor(tenant.Username),
		p.Username,
	}
	if out, err := exec.CommandContext(ctx, "useradd", useraddArgs...).CombinedOutput(); err != nil {
		return fail(&agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("useradd (isolated) %q: %v: %s", p.Username, err, strings.TrimSpace(string(out)))})
	}
	undo = append(undo, func() { _ = exec.CommandContext(ctx, "userdel", "-f", p.Username).Run() })

	// 4. ACLs on the JAIL-SIDE mountpoint (never the tenant pathname). The jail
	// chain is root-owned so the argument cannot be swapped, and the mount has
	// pinned the inode, so `setfacl -R <jail>/data` recurses over exactly the
	// intended tree. Grant the sub-uid rwX and keep the tenant-uid rwX (the sub
	// writes as its own uid; the tenant must still manage those files). Default
	// ACLs (-d) make new files inherit both. Uppercase X = execute only where
	// already applicable (dirs / already-exec files). GNU setfacl -R skips
	// symlinks encountered during recursion by default.
	spec := fmt.Sprintf("u:%d:rwX,u:%d:rwX", p.UID, tenant.UID)
	if out, err := exec.CommandContext(ctx, "setfacl", "-R", "-m", spec, mountpoint).CombinedOutput(); err != nil {
		return fail(&agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("setfacl -R %q: %v: %s", mountpoint, err, strings.TrimSpace(string(out)))})
	}
	if out, err := exec.CommandContext(ctx, "setfacl", "-dR", "-m", spec, mountpoint).CombinedOutput(); err != nil {
		return fail(&agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("setfacl -dR %q: %v: %s", mountpoint, err, strings.TrimSpace(string(out)))})
	}

	// 5. Per-uid quota (the split cap). setquota block counts are 1KB units.
	blocks := strconv.FormatUint(uint64(p.QuotaMB)*1024, 10)
	if out, err := exec.CommandContext(ctx, "setquota", "-u", p.Username, blocks, blocks, "0", "0", p.QuotaMount).CombinedOutput(); err != nil {
		return fail(&agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("setquota (isolated) %q: %v: %s", p.Username, err, strings.TrimSpace(string(out)))})
	}

	return rollback, nil
}

// teardownIsolatedJail unmounts and removes a subaccount jail. Used by the
// create rollback, the delete verb, and the reconciler's fail-closed path.
// Idempotent. It NEVER touches the bind-mount SOURCE (tenant data) — only the
// jail overlay.
//
// CRITICAL data-safety invariant: on the fleet, /home is not a separate mount,
// so a bind mount is SAME-DEVICE — st_dev does not change at the mountpoint and
// there is no reliable stat-based "is it mounted" probe. We therefore unmount
// BLIND, looping until the kernel returns EINVAL ("not mounted"), and only
// RemoveAll once the mount is provably gone. If unmount fails for any other
// reason we abort WITHOUT removing — a stranded empty jail is recoverable by
// the reconciler; a RemoveAll that recurses through a live bind mount would
// delete the tenant's real files.
func teardownIsolatedJail(ctx context.Context, jailPath string) *agentwire.AgentError {
	if jailPath == "" {
		return nil
	}
	mountpoint := filepath.Join(jailPath, ftpJailMountpoint)
	// MNT_DETACH: lazy unmount removes the mount from the tree immediately
	// (new lookups see the underlying empty dir) even if a session holds it,
	// so the subsequent RemoveAll cannot descend into the source.
	for {
		err := syscall.Unmount(mountpoint, syscall.MNT_DETACH)
		if err == nil {
			continue // detached one layer; loop for any stacked binds
		}
		if errors.Is(err, syscall.EINVAL) {
			break // not a mountpoint (anymore) — safe to remove
		}
		return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("unmount jail %q: %v", mountpoint, err)}
	}
	if err := os.RemoveAll(jailPath); err != nil {
		return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("remove jail %q: %v", jailPath, err)}
	}
	return nil
}
