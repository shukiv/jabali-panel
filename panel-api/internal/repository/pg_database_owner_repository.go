package repository

import (
	"context"

	"gorm.io/gorm"
)

// PGDatabaseOwnerRepository lists every Postgres database the panel knows
// with the user its objects belong to (GH #2004). The reconciler sends the
// map to the Agent's db.postgres.reown_superuser_objects, which hands what a
// superuser owns in each database to that user.
type PGDatabaseOwnerRepository interface {
	ListPGDatabaseOwners(ctx context.Context) (map[string]string, error)
}

type pgDatabaseOwnerRepo struct{ db *gorm.DB }

// NewPGDatabaseOwnerRepository returns a GORM-backed repo.
func NewPGDatabaseOwnerRepository(db *gorm.DB) PGDatabaseOwnerRepository {
	return &pgDatabaseOwnerRepo{db: db}
}

// ListPGDatabaseOwners returns database name → the Postgres user granted on
// it first, or "" for a database with none. Only a user of the database's own
// account counts: a grant to another account's user never hands it the
// database's objects. `databases` is quoted: it is a reserved word in MariaDB.
func (r *pgDatabaseOwnerRepo) ListPGDatabaseOwners(ctx context.Context) (map[string]string, error) {
	var rows []struct {
		DBName string
		Role   *string
	}
	err := r.db.WithContext(ctx).
		Table("`databases` AS d").
		Select("d.name AS db_name, du.username AS role").
		Joins("LEFT JOIN database_user_grants g ON g.database_id = d.id").
		Joins("LEFT JOIN database_users du ON du.id = g.database_user_id AND du.engine = ? AND du.user_id = d.user_id", "postgres").
		Where("d.engine = ?", "postgres").
		Order("d.name ASC, g.created_at ASC, g.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, translate(err)
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		owner, seen := out[row.DBName]
		if !seen {
			out[row.DBName] = ""
		}
		if owner == "" && row.Role != nil && *row.Role != "" {
			out[row.DBName] = *row.Role
		}
	}
	return out, nil
}
