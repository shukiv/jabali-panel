package backupmetadata

import (
	"context"
	"errors"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dbops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// restoreDBLimits is the restored account's hosting package limits on
// databases and database users; 0 means no limit. A restore keeps to them as
// a database or database user created in the panel does (dbops.Create, POST
// /database-users): the count and the insert share one transaction.
type restoreDBLimits struct {
	databases, databaseUsers uint32
}

// readRestoreDBLimits reads the limits of account userID. No package, or a
// package that no longer exists, is no limit (GH #282). An error means a
// package is set but couldn't be read: the restore then adds no database and
// no database user rather than lift the limits.
func readRestoreDBLimits(ctx context.Context, d Deps, userID string) (restoreDBLimits, error) {
	if d.Users == nil {
		return restoreDBLimits{}, errors.New("the package limits can't be checked")
	}
	u, err := d.Users.FindByID(ctx, userID)
	if errors.Is(err, repository.ErrNotFound) {
		// Apply found or created the account before this step. Gone since,
		// no row can be added for it: each references it (users.id).
		return restoreDBLimits{}, nil
	}
	if err != nil {
		return restoreDBLimits{}, fmt.Errorf("look up the account: %v", err)
	}
	if u == nil {
		return restoreDBLimits{}, errors.New("look up the account: not found")
	}
	dbs, err := dbops.PackageLimit(ctx, d.Packages, u, dbops.MaxDatabases)
	if err != nil {
		return restoreDBLimits{}, err
	}
	users, err := dbops.PackageLimit(ctx, d.Packages, u, dbops.MaxDatabaseUsers)
	if err != nil {
		return restoreDBLimits{}, err
	}
	return restoreDBLimits{databases: dbs, databaseUsers: users}, nil
}

// restoreLimitError means a row would take the account past a package limit.
type restoreLimitError struct {
	limit uint32
	noun  string // "database" or "database user"
}

func (e *restoreLimitError) Error() string {
	noun := e.noun
	if e.limit != 1 {
		noun += "s"
	}
	return fmt.Sprintf("the account's hosting package allows %d %s", e.limit, noun)
}

// createDatabase inserts row within the databases limit.
func (l restoreDBLimits) createDatabase(ctx context.Context, repo repository.DatabaseRepository, row *models.Database) error {
	if l.databases == 0 {
		return repo.Create(ctx, row)
	}
	err := repo.CreateWithinCap(ctx, row, l.databases)
	if errors.Is(err, repository.ErrDatabaseCapExceeded) {
		return &restoreLimitError{limit: l.databases, noun: "database"}
	}
	return err
}

// createDatabaseUser inserts row within the database users limit.
func (l restoreDBLimits) createDatabaseUser(ctx context.Context, repo repository.DatabaseUserRepository, row *models.DatabaseUser) error {
	if l.databaseUsers == 0 {
		return repo.Create(ctx, row)
	}
	err := repo.CreateWithinCap(ctx, row, l.databaseUsers)
	if errors.Is(err, repository.ErrDatabaseUserCapExceeded) {
		return &restoreLimitError{limit: l.databaseUsers, noun: "database user"}
	}
	return err
}
