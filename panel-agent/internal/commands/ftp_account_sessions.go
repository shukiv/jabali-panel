package commands

import (
	"context"
	"strconv"
)

// ftpSshdSessionPattern is the pgrep/pkill -f argv pattern that matches a
// specific subaccount's live sshd session WITHOUT keying on uid. OpenSSH sets
// the per-session process title to "sshd: <login>@<tty>" (e.g. "@notty" for
// SFTP) and the privsep monitor to "sshd: <login> [priv]"; the [@ ] class
// matches both while the char immediately after the login name prevents a
// prefix collision ("shop_deploy" never matches "shop_deploy2@" or
// "shop_deploy_x@"). The login name is validated to [a-z0-9_] upstream, so it
// carries no regex metacharacters.
func ftpSshdSessionPattern(alias string) string {
	return "sshd: " + alias + "[@ ]"
}

// terminateFtpSessions best-effort kills a subaccount's live FTP/SFTP sessions
// so a disable, delete, suspend, or password-reset takes effect immediately,
// instead of leaving an already-open connection alive until it closes on its
// own (JAB-256). It is DEFENSE-IN-DEPTH on top of the shadow lock + degroup
// (which already block RE-authentication), so every step is best-effort and
// never aborts the caller.
//
// The kill strategy MUST branch on the isolation model, because a legacy
// same-uid alias shares the tenant's uid:
//   - Isolated (own uid >= ftpSubaccountUIDMin): the uid is unique to this
//     alias, so `loginctl terminate-user` + `pkill -KILL -u <uid>` terminate
//     exactly its sessions (sshd, internal-sftp, and vsftpd) and nothing else.
//   - Legacy same-uid: killing by uid would ALSO kill the tenant's shell,
//     cron leader, and sibling aliases (getpwuid can't even distinguish the
//     sessions). Fall back to the name-scoped sshd argv match above. vsftpd
//     legacy sessions carry no per-login handle at a shared uid and CANNOT be
//     precisely targeted — a documented residual (re-auth is already blocked;
//     only an already-open pipe lingers, on the shrinking deprecated set).
//
// pkill exits 1 when nothing matched (the common "no live session" case),
// which is not an error; the caller ignores all exit codes by design.
func terminateFtpSessions(ctx context.Context, aliasName string, aliasUID int) {
	if aliasUID >= ftpSubaccountUIDMin {
		uid := strconv.Itoa(aliasUID)
		_ = execCommandContext(ctx, "loginctl", "terminate-user", uid).Run()
		_ = execCommandContext(ctx, "pkill", "-KILL", "-u", uid).Run()
		return
	}
	_ = execCommandContext(ctx, "pkill", "-KILL", "-f", ftpSshdSessionPattern(aliasName)).Run()
}
