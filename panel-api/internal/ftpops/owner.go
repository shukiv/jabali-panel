package ftpops

import "context"

// ReapOwner tears down every FTP/SFTP subaccount an owner has, as part of
// deleting that owner (JAB-265). It shares the host teardown with Delete
// (deleteHostAlias) but deliberately keeps a different failure policy: the row
// is deleted REGARDLESS of the host result. The owner is going away, so the row
// cannot serve as a retry handle; a failed teardown degrades to a rowless alias
// the stray-alias reaper sweeps on its next pass — never a silent
// live-credential orphan hidden from the reaper by a surviving row.
//
// Best-effort throughout: failures are logged, never returned, so they cannot
// fail the owner delete. The sshd drop-in is re-rendered once at the end so it
// no longer carries rules for the removed aliases (the reconciler converges if
// that fails). No-op when the account store or agent is unwired or the owner
// has no Linux username.
func ReapOwner(ctx context.Context, d Deps, userID, tenantUsername string) {
	if d.Accounts == nil || d.Agent == nil || tenantUsername == "" {
		return
	}
	accts, err := d.Accounts.ListByUserID(ctx, userID)
	if err != nil {
		warn(d, "cascade delete: list user ftp accounts failed", "user_id", userID, "err", err)
		return
	}
	for i := range accts {
		a := &accts[i]
		if err := deleteHostAlias(ctx, d, tenantUsername, a.Username); err != nil {
			warn(d, "cascade delete: ftp account agent teardown failed (row still removed; reaper backstops the alias)",
				"user_id", userID, "ftp_account", a.Username, "err", err)
		}
		if err := d.Accounts.Delete(ctx, a.ID); err != nil {
			warn(d, "cascade delete: ftp account DB delete failed",
				"user_id", userID, "ftp_account", a.Username, "err", err)
		}
	}
	syncHostAccess(ctx, d, tenantUsername)
}

func warn(d Deps, msg string, args ...any) {
	if d.Log != nil {
		d.Log.Warn(msg, args...)
	}
}
