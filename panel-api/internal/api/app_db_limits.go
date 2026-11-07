package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dbops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// appDBLimits is an owner's package limits on databases and database users.
// An app install, a WordPress install and a WordPress clone each create one of
// both, so their rows go through these limits like a database created on its
// own (dbops.Create) or a database user (POST /database-users). 0 means no
// limit: no package, or a package that sets none.
type appDBLimits struct {
	databases     uint32
	databaseUsers uint32
}

// loadAppDBLimits reads owner's limits. An error wraps dbops.ErrQuotaUnavailable:
// the package could not be read, so the install is refused, not let through.
func loadAppDBLimits(ctx context.Context, pkgs repository.PackageRepository, owner *models.User) (appDBLimits, error) {
	if owner == nil {
		return appDBLimits{}, fmt.Errorf("%w: no owner", dbops.ErrQuotaUnavailable)
	}
	dbs, err := dbops.PackageLimit(ctx, pkgs, owner, dbops.MaxDatabases)
	if err != nil {
		return appDBLimits{}, err
	}
	users, err := dbops.PackageLimit(ctx, pkgs, owner, dbops.MaxDatabaseUsers)
	if err != nil {
		return appDBLimits{}, err
	}
	return appDBLimits{databases: dbs, databaseUsers: users}, nil
}

// appDBQuotaError means the install would take the owner past a package limit.
type appDBQuotaError struct {
	resource string // "databases" or "database_users", as in the quota_exceeded body
	limit    uint32
}

func (e *appDBQuotaError) Error() string {
	noun := "databases"
	if e.resource == "database_users" {
		noun = "database users"
	}
	return fmt.Sprintf("the hosting package allows no more %s (limit %d)", noun, e.limit)
}

// createDatabase inserts row, held to the databases limit at the insert (the
// count and the insert share one transaction, so two installs racing for the
// last slot cannot both get it).
func (l appDBLimits) createDatabase(ctx context.Context, repo repository.DatabaseRepository, row *models.Database) error {
	if l.databases == 0 {
		return repo.Create(ctx, row)
	}
	err := repo.CreateWithinCap(ctx, row, l.databases)
	if errors.Is(err, repository.ErrDatabaseCapExceeded) {
		return &appDBQuotaError{resource: "databases", limit: l.databases}
	}
	return err
}

// createDatabaseUser is createDatabase for the database user row.
func (l appDBLimits) createDatabaseUser(ctx context.Context, repo repository.DatabaseUserRepository, row *models.DatabaseUser) error {
	if l.databaseUsers == 0 {
		return repo.Create(ctx, row)
	}
	err := repo.CreateWithinCap(ctx, row, l.databaseUsers)
	if errors.Is(err, repository.ErrDatabaseUserCapExceeded) {
		return &appDBQuotaError{resource: "database_users", limit: l.databaseUsers}
	}
	return err
}

// respondAppDBLimitErr answers err when it is a package-limit refusal and
// reports whether it did: 409 quota_exceeded (the body POST /database-users
// gives) or 503 quota_unavailable.
func respondAppDBLimitErr(c *gin.Context, err error) bool {
	var qe *appDBQuotaError
	switch {
	case errors.As(err, &qe):
		c.JSON(http.StatusConflict, gin.H{"error": "quota_exceeded", "resource": qe.resource, "limit": int64(qe.limit)})
		return true
	case errors.Is(err, dbops.ErrQuotaUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "quota_unavailable", "detail": "the hosting package's limits cannot be loaded right now"})
		return true
	}
	return false
}
