package commands

import (
	"context"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
)

// fileScopeFor builds the filesafe scope for a file command (GH #1184).
//
// Ordinary tenant ops confine to the caller's home directory, exactly as
// before. When adminRoot is set the scope may READ the whole filesystem minus
// the hard deny-list (filesafe.DefaultAdminDeniedPrefixes), but every WRITE is
// additionally confined to the JAB-358 allow-list of safe data roots
// (filesafe.DefaultAdminMutableRoots) — so the admin File Manager can never
// create a root-owned file under a path root later executes or trusts
// (/etc/cron.d, systemd units, /etc/passwd, the loader, package hooks …).
//
// JAB-357 AC4: adminRoot was previously honoured on trust of the request flag,
// on the reasoning that the agent's unix socket is panel-only. That trust is
// now defence-in-depth, not the only line: admin_root is refused unless the
// CONNECTING PEER is authorized for it (its SO_PEERCRED UID is on the agent's
// -admin-uids allow-list, stamped onto ctx by the server via WithPeerIdentity).
// So a co-located service that reached the socket after a connect-gate or
// unix-group regression cannot escalate to the root scope merely by sending
// admin_root=true — PeerAdminCapable fails closed for it. The panel's own
// admin-auth AND default-off server-setting gate still run upstream, and the
// deny/mutable lists are still enforced here regardless.
func fileScopeFor(ctx context.Context, userID, username string, adminRoot bool) (*filesafe.Scope, error) {
	if adminRoot {
		if !PeerAdminCapable(ctx) {
			return nil, &agentwire.AgentError{Code: agentwire.CodePermissionDenied, Message: "admin_root requires an authorized peer"}
		}
		return filesafe.NewAdminScope(userID, username, filesafe.DefaultAdminDeniedPrefixes, filesafe.DefaultAdminMutableRoots)
	}
	return filesafe.NewScope(userID, username, []string{"/home/" + username})
}

// fileOwnerIDs returns the uid/gid new files should be chowned to. The admin
// File Manager (GH #1184, root scope) leaves root-tree files root:root (0,0);
// tenant mode uses <user>:www-data via hostingIDs.
//
// JAB-357 AC4: root:root ownership is a privileged outcome, so it is granted
// only when the connecting peer is admin-capable. A spoofed admin_root=true
// from an unauthorized peer never reaches here (fileScopeFor rejects first),
// but this guards defensively — an unauthorized peer falls back to the tenant
// hostingIDs mapping rather than chowning a new file to root.
func fileOwnerIDs(ctx context.Context, username string, adminRoot bool) (int, int) {
	if adminRoot && PeerAdminCapable(ctx) {
		return 0, 0
	}
	return hostingIDs(username)
}
