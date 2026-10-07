package backupmetadata

import (
	"context"
	"fmt"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailboxops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1993: with OverwriteRows, the rows the account already has take the
// backup's settings. An uploaded file was written by whoever made it, so its
// author must not get a login to data the file didn't supply.

// overwriteMailbox gives a mailbox the account already has the backup's quota
// and disabled flag. It keeps its password: the mail already in it didn't
// come from the file.
func overwriteMailbox(ctx context.Context, d Deps, r *ApplyResult, existing *models.Mailbox, mb internalbackup.MetadataMailbox, address string) {
	label := fmt.Sprintf("mailbox %s (%s)", mb.ID, address)
	if mb.PasswordHash != "" && mb.PasswordHash != existing.PasswordHash {
		r.Errors = append(r.Errors, label+": kept its password; a password from an uploaded backup is set only on a mailbox the restore creates")
	}
	quota := mb.QuotaBytes != existing.QuotaBytes
	disabled := mb.IsDisabled != existing.IsDisabled
	if !quota && !disabled {
		return
	}
	if d.MailboxSettings == nil {
		r.Errors = append(r.Errors, label+": settings not updated: the mailbox settings are not wired")
		return
	}
	if quota {
		if mb.QuotaBytes < mailboxops.MinQuotaBytes {
			r.Errors = append(r.Errors, fmt.Sprintf("%s: quota not taken from the backup: it is under the %d MiB minimum", label, mailboxops.MinQuotaBytes>>20))
		} else if err := d.MailboxSettings.SetQuota(ctx, existing, mb.QuotaBytes); err != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("%s: quota not updated: %v", label, err))
		}
	}
	if disabled {
		if err := d.MailboxSettings.SetDisabled(ctx, existing, mb.IsDisabled); err != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("%s: disabled not updated: %v", label, err))
		}
	}
}

// existingDBUser is a database user of the backup that the account already
// has here, as id.
type existingDBUser struct {
	id     string
	backup internalbackup.MetadataDatabaseUser
}

// overwriteDBUserPasswords gives each database user the account already has
// the backup's password, but only when every database it can open holds
// nothing but this file's data (ArchiveMariaDBs): the file's author knows
// that password. Otherwise the user keeps its password, with a line in the
// report.
func overwriteDBUserPasswords(ctx context.Context, d Deps, accountID string, users []existingDBUser, r *ApplyResult) {
	for _, u := range users {
		row, err := d.DatabaseUsers.FindByID(ctx, u.id)
		if err != nil || row == nil || row.UserID != accountID {
			r.Errors = append(r.Errors, fmt.Sprintf("db_user %s (%s): kept its password: lookup: %v", u.backup.ID, u.backup.Username, err))
			continue
		}
		// This server's account, whatever name the file gives it.
		label := fmt.Sprintf("db_user %s (%s)", u.backup.ID, row.Username)
		if why := dbUserPasswordRefusal(ctx, d, accountID, row, u.backup); why != "" {
			r.Errors = append(r.Errors, label+": kept its password: "+why)
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, restoredDBAccountTimeout)
		_, err = d.Agent.Call(callCtx, "db_user.create", map[string]any{
			"db_user_name": row.Username, "password_hash": u.backup.NativePasswordHash,
		})
		cancel()
		if err != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("%s: kept its password: setting it in MariaDB failed: %v", label, err))
			continue
		}
		if u.backup.PasswordHash != "" {
			if err := d.DatabaseUsers.UpdatePasswordHash(ctx, row.ID, u.backup.PasswordHash); err != nil {
				r.Errors = append(r.Errors, fmt.Sprintf("%s: has the backup's password in MariaDB, but the panel's record of it was not updated: %v", label, err))
			}
		}
	}
}

// dbUserPasswordRefusal says why row keeps its password, or "".
func dbUserPasswordRefusal(ctx context.Context, d Deps, accountID string, row *models.DatabaseUser, backup internalbackup.MetadataDatabaseUser) string {
	switch {
	case row.Engine == "postgres":
		return "a PostgreSQL user's password is not restored onto an existing user"
	case !nativePasswordHashRe.MatchString(backup.NativePasswordHash):
		return "the backup doesn't carry its MariaDB password"
	case d.Agent == nil:
		return "the server agent is not wired"
	case d.DatabaseGrants == nil || d.Databases == nil:
		return "the database checks are not wired"
	case d.ArchiveMariaDBs == nil:
		return "this server's agent is too old to tell whether its databases hold only the uploaded backup's data; run jabali update and restore again"
	}
	// The grants after this restore: the ones it had, and the ones added.
	grants, err := d.DatabaseGrants.ListByDatabaseUserID(ctx, row.ID)
	if err != nil {
		return fmt.Sprintf("list its grants: %v", err)
	}
	if len(grants) == 0 {
		return "it can open no database this backup restored"
	}
	for _, g := range grants {
		db, err := d.Databases.FindByID(ctx, g.DatabaseID)
		if err != nil || db == nil {
			return fmt.Sprintf("look up database %s: %v", g.DatabaseID, err)
		}
		switch {
		case db.UserID != accountID:
			return fmt.Sprintf("it can open %s, which isn't this account's", db.Name)
		case !d.RestoredDatabases[db.Name]:
			return fmt.Sprintf("it can open %s, which this backup didn't restore", db.Name)
		case !d.ArchiveMariaDBs[db.Name]:
			// Loaded over data this server had, or not loaded cleanly.
			return fmt.Sprintf("it can open %s, which holds data that isn't the uploaded backup's", db.Name)
		}
	}
	return ""
}
