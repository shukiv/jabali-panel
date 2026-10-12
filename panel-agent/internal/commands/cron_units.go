package commands

import (
	"os/user"
	"path/filepath"
)

// lookupCronUser resolves a tenant's Linux account for the cron handlers;
// tests swap in an account whose home is a temp dir.
var lookupCronUser = user.Lookup

// legacyCronUnitsRoot is where tenant cron units were written before GH #273
// moved them into the user's systemd search path. Nothing loads units from
// it; cron.remove and cron.list still clean it up.
var legacyCronUnitsRoot = "/etc/jabali-panel/cron-units"

// cronUnitsDir is where cron.apply writes a tenant's units, and so where
// cron.remove and cron.list must look: the user's own systemd search path.
func cronUnitsDir(u *user.User) string {
	return filepath.Join(u.HomeDir, ".config", "systemd", "user")
}
