package commands

import (
	"context"
	"os"
	"path/filepath"
)

// nspawnConfigRoot is where per-instance .nspawn config files live. A package
// var (env-overridable) so tests can point it at a temp dir; production uses
// the systemd default.
func nspawnConfigRoot() string {
	if r := os.Getenv("JABALI_NSPAWN_CONFIG_ROOT"); r != "" {
		return r
	}
	return "/etc/systemd/nspawn"
}

// reapUserNspawnPHPUnit tears down the legacy per-user PHP nspawn unit
// (systemd-nspawn@<user>-php.service) for a user being deleted (JAB-225).
//
// Current code never creates these units — per-user PHP containers were
// replaced by bubblewrap — but a user carried over from the M13-nspawn era can
// still own an *enabled* unit. Once the account (and its machine image) is
// gone, an enabled unit fails on every boot with "No image for machine
// '<user>-php'", padding `systemctl --failed` with a permanent dead entry.
// user.delete removes the account, so this mirrors reapUserFPMPools and closes
// the create-enabled / never-disabled-at-delete gap (same family as GH #754).
//
// Best-effort: every step tolerates an already-absent unit/config and never
// returns an error. `reset-failed` is deliberate — `disable` alone leaves the
// failed entry lingering in `systemctl --failed` until the next reboot.
func reapUserNspawnPHPUnit(ctx context.Context, username string) {
	if !usernameRegex.MatchString(username) {
		return
	}
	instance := username + "-php"
	unit := "systemd-nspawn@" + instance + ".service"

	_ = execCommandContext(ctx, "systemctl", "disable", "--now", unit).Run()
	_ = execCommandContext(ctx, "systemctl", "reset-failed", unit).Run()

	// Remove any generated per-instance nspawn config (AC: "removes any
	// generated unit/config").
	_ = os.Remove(filepath.Join(nspawnConfigRoot(), instance+".nspawn"))
}
