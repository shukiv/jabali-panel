package commands

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// GH #1146 — WebDAV access for an FTP/SFTP subaccount, realized as a per-uid
// systemd worker (jabali-webdav@<sub>) that mirrors the per-user PHP-FPM model.
//
// The worker (panel-agent/cmd/jabali-webdav) runs as the subaccount's OWN uid
// and — for isolated (#1145) accounts — inside the same root-owned chroot jail
// that sshd/vsftpd use, so every file it writes is uid-owned, quota-counted, and
// kernel-confined to the home. It does NOT authenticate: nginx terminates TLS
// and reverse-proxies to the worker only after a privileged auth_request has
// verified the subaccount's credentials + membership in jabali-webdav (step 4).
//
// Lifecycle is socket-activated: systemd (as root) owns the socket, so the agent
// only writes a per-instance drop-in (numeric User/Group, RootDirectory+bind for
// isolated accounts, WEBDAV_ROOT/WEBDAV_PREFIX) and enables the .socket. The
// worker starts on the first proxied connection.
//
// webdavGroupName is the authorization gate the Phase-4 nginx authenticator
// checks per request (mirrors jabali-ftp for vsftpd). Held only while
// webdav_access is on.
const webdavGroupName = "jabali-webdav"

// webdavWorkerParams are the per-account facts the drop-in needs, derived from
// the subaccount's validated passwd identity.
type webdavWorkerParams struct {
	Username   string
	UID        int
	GID        int
	ServedRoot string // WEBDAV_ROOT: "/data" (isolated, inside the chroot) or the passwd home (legacy)
	Jail       string // RootDirectory for isolated accounts; "" = no chroot (legacy same-uid alias)
}

// deriveWebdavWorker computes the worker params from a validated subaccount.
// Isolated accounts (own uid in the reserved range) chroot to their #1145 jail
// and serve the bind-mounted "/data"; legacy same-uid aliases serve their passwd
// home directly with no chroot, matching the credential-scoped legacy model.
func deriveWebdavWorker(tenant *ftpTenant, u *user.User) (webdavWorkerParams, *agentwire.AgentError) {
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return webdavWorkerParams{}, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("parse uid for %q: %v", u.Username, err)}
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return webdavWorkerParams{}, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("parse gid for %q: %v", u.Username, err)}
	}
	if uid >= ftpSubaccountUIDMin {
		return webdavWorkerParams{
			Username:   u.Username,
			UID:        uid,
			GID:        gid,
			ServedRoot: "/" + ftpJailMountpoint, // /data inside the chroot
			Jail:       ftpJailPathFor(tenant, u.Username),
		}, nil
	}
	return webdavWorkerParams{
		Username:   u.Username,
		UID:        uid,
		GID:        gid,
		ServedRoot: u.HomeDir,
		Jail:       "",
	}, nil
}

// buildWebdavDropin renders the per-instance systemd override. Numeric User/Group
// because a name may not resolve inside RootDirectory (the jail has no passwd).
// For isolated accounts it sets the chroot and binds the STATIC worker binary in
// (so /usr/local/bin/jabali-webdav exists to exec inside the jail). WEBDAV_PREFIX
// is empty until Phase 4 wires the nginx path prefix.
func buildWebdavDropin(wp webdavWorkerParams) string {
	var b strings.Builder
	b.WriteString("# Managed by jabali panel-agent (GH #1146). Do NOT hand-edit.\n")
	b.WriteString("[Service]\n")
	fmt.Fprintf(&b, "User=%d\n", wp.UID)
	fmt.Fprintf(&b, "Group=%d\n", wp.GID)
	fmt.Fprintf(&b, "Environment=WEBDAV_ROOT=%s\n", wp.ServedRoot)
	// Prefix=/dav: nginx serves every subaccount under https://<panel>:8443/dav/
	// and proxies the path through UNSTRIPPED, so the worker strips /dav itself
	// (x/net/webdav also rewrites the Destination header for MOVE/COPY when Prefix
	// is set — nginx cannot). Shared across all subs; the socket, not the URL,
	// distinguishes them (GH #1146 step 4).
	b.WriteString("Environment=WEBDAV_PREFIX=/dav\n")
	if wp.Jail != "" {
		fmt.Fprintf(&b, "RootDirectory=%s\n", wp.Jail)
		b.WriteString("BindReadOnlyPaths=/usr/local/bin/jabali-webdav\n")
	}
	return b.String()
}

func webdavDropinDir(username string) string {
	return filepath.Join(systemdRoot(), fmt.Sprintf("jabali-webdav@%s.service.d", username))
}

func webdavSocketUnit(username string) string  { return fmt.Sprintf("jabali-webdav@%s.socket", username) }
func webdavServiceUnit(username string) string { return fmt.Sprintf("jabali-webdav@%s.service", username) }

// webdavWorkerEnabled reports whether the WebDAV worker is provisioned for
// username, by the presence of its per-instance drop-in dir (written by
// enableWebdavWorker, removed by disableWebdavWorker). A cheap os.Stat — no
// systemctl exec — so the reconciler can poll it per-account each tick without
// churn. The reconciler diffs this against the row's desired webdav_access to
// heal a failed enable/disable.
func webdavWorkerEnabled(username string) bool {
	fi, err := os.Stat(webdavDropinDir(username))
	return err == nil && fi.IsDir()
}

// ensureWebdavGroup makes the jabali-webdav authorization group exist.
// Idempotent; install.sh's webdav provisioning also creates it, but an older
// host that enables webdav_access before that lands must not fail here.
func ensureWebdavGroup(ctx context.Context) *agentwire.AgentError {
	if _, err := user.LookupGroup(webdavGroupName); err == nil {
		return nil
	}
	if out, err := execCommandContext(ctx, "groupadd", "--system", webdavGroupName).CombinedOutput(); err != nil {
		return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("groupadd %s: %v: %s", webdavGroupName, err, strings.TrimSpace(string(out)))}
	}
	return nil
}

// enableWebdavForCreate realizes the WebDAV worker for a freshly-created
// subaccount: it resolves the just-created passwd entry (for its uid/gid + home)
// and enables the worker + jabali-webdav group. Called by the create handler
// after the account exists, mirroring the FTPAccess group step.
func enableWebdavForCreate(ctx context.Context, tenant *ftpTenant, username string) *agentwire.AgentError {
	u, err := user.Lookup(username)
	if err != nil {
		return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("lookup %q for webdav enable: %v", username, err)}
	}
	return enableWebdavWorker(ctx, tenant, u)
}

// enableWebdavWorker provisions and starts the per-subaccount WebDAV worker.
// Idempotent for the reconciler's per-tick calls: the drop-in is rewritten (and
// systemd reloaded) ONLY when its content changed, and group-add is skipped when
// already a member — so a steady state does no work.
func enableWebdavWorker(ctx context.Context, tenant *ftpTenant, u *user.User) *agentwire.AgentError {
	wp, aerr := deriveWebdavWorker(tenant, u)
	if aerr != nil {
		return aerr
	}
	if aerr := ensureWebdavGroup(ctx); aerr != nil {
		return aerr
	}
	// Group membership — the Phase-4 auth gate. Guard the usermod to avoid
	// per-tick churn (usermod -aG on an existing member is a no-op but noisy).
	if member, _ := isUserInGroup(ctx, wp.Username, webdavGroupName); !member {
		if out, err := execCommandContext(ctx, "usermod", "-aG", webdavGroupName, wp.Username).CombinedOutput(); err != nil {
			return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("usermod -aG %s %s: %v: %s", webdavGroupName, wp.Username, err, strings.TrimSpace(string(out)))}
		}
	}
	// Drop-in — rewrite + reload only on change (per-tick idempotency).
	dir := webdavDropinDir(wp.Username)
	path := filepath.Join(dir, "override.conf")
	content := []byte(buildWebdavDropin(wp))
	if !filesMatch(path, content) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("create webdav drop-in dir %q: %v", dir, err)}
		}
		if err := writeFileAtomically(path, content, 0o644); err != nil {
			return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write webdav drop-in %q: %v", path, err)}
		}
		if out, err := execCommandContext(ctx, "systemctl", "daemon-reload").CombinedOutput(); err != nil {
			return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("systemctl daemon-reload: %v: %s", err, strings.TrimSpace(string(out)))}
		}
	}
	// Enable + activate the SOCKET: the worker starts on the first connection,
	// and socket enablement persists across reboots. Idempotent.
	unit := webdavSocketUnit(wp.Username)
	if out, err := execCommandContext(ctx, "systemctl", "enable", "--now", unit).CombinedOutput(); err != nil {
		return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("systemctl enable --now %s: %v: %s", unit, err, strings.TrimSpace(string(out)))}
	}
	return nil
}

// disableWebdavWorker tears the worker down: stop+disable BOTH units (disabling
// only the socket leaves the running service; stopping only the service lets the
// socket re-activate it), remove the drop-in, leave the group. Idempotent and
// safe for an account that never had webdav — every step no-ops.
func disableWebdavWorker(ctx context.Context, username string) *agentwire.AgentError {
	// disable --now stops + disables the socket; the activated service is then
	// stopped explicitly (it has no [Install], so it is never "enabled").
	_ = execCommandContext(ctx, "systemctl", "disable", "--now", webdavSocketUnit(username)).Run()
	_ = execCommandContext(ctx, "systemctl", "stop", webdavServiceUnit(username)).Run()
	_ = os.RemoveAll(webdavDropinDir(username))
	_ = execCommandContext(ctx, "systemctl", "daemon-reload").Run()
	// Drop the authorization group last — the gate is closed once the units and
	// membership are both gone.
	if aerr := setWebdavGroupMembership(ctx, username, false); aerr != nil {
		return aerr
	}
	return nil
}

// setWebdavGroupMembership adds/removes username from jabali-webdav. Mirrors
// setFtpGroupMembership: removal is a no-op for an absent group or non-member.
func setWebdavGroupMembership(ctx context.Context, username string, member bool) *agentwire.AgentError {
	if member {
		if aerr := ensureWebdavGroup(ctx); aerr != nil {
			return aerr
		}
		if out, err := execCommandContext(ctx, "usermod", "-aG", webdavGroupName, username).CombinedOutput(); err != nil {
			return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("usermod -aG %s %s: %v: %s", webdavGroupName, username, err, strings.TrimSpace(string(out)))}
		}
		return nil
	}
	isMember, err := isUserInGroup(ctx, username, webdavGroupName)
	if err != nil || !isMember {
		return nil // absent group or non-member: removal is a no-op
	}
	if out, gerr := execCommandContext(ctx, "gpasswd", "-d", username, webdavGroupName).CombinedOutput(); gerr != nil {
		return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("gpasswd -d %s %s: %v: %s", username, webdavGroupName, gerr, strings.TrimSpace(string(out)))}
	}
	return nil
}
