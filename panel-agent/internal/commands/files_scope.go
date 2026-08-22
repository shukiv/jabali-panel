package commands

import (
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
// adminRoot is honoured only because the agent's unix socket is panel-only:
// the panel sets it after its own admin-auth AND default-off server-setting
// gate, and both lists are enforced here regardless as the last line of
// defence.
func fileScopeFor(userID, username string, adminRoot bool) (*filesafe.Scope, error) {
	if adminRoot {
		return filesafe.NewAdminScope(userID, username, filesafe.DefaultAdminDeniedPrefixes, filesafe.DefaultAdminMutableRoots)
	}
	return filesafe.NewScope(userID, username, []string{"/home/" + username})
}

// fileOwnerIDs returns the uid/gid new files should be chowned to. The admin
// File Manager (GH #1184, root scope) leaves root-tree files root:root (0,0);
// tenant mode uses <user>:www-data via hostingIDs.
func fileOwnerIDs(username string, adminRoot bool) (int, int) {
	if adminRoot {
		return 0, 0
	}
	return hostingIDs(username)
}
