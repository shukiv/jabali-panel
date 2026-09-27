package repository

import (
	"context"

	"gorm.io/gorm"
)

// PGDatabaseGrantRepository lists, per Postgres database, the roles the
// panel granted on it. The reconciler sends the map to the Agent's
// db.postgres.revoke_public_access, which re-grants each role its own
// database access before it revokes PUBLIC's.
type PGDatabaseGrantRepository interface {
	ListPGDatabaseGrants(ctx context.Context) (map[string][]string, error)
}

type pgDatabaseGrantRepo struct{ db *gorm.DB }

// NewPGDatabaseGrantRepository returns a GORM-backed repo.
func NewPGDatabaseGrantRepository(db *gorm.DB) PGDatabaseGrantRepository {
	return &pgDatabaseGrantRepo{db: db}
}

// ListPGDatabaseGrants returns database name → granted role names, each list
// sorted. `databases` is quoted: it is a reserved word in MariaDB.
func (r *pgDatabaseGrantRepo) ListPGDatabaseGrants(ctx context.Context) (map[string][]string, error) {
	var rows []struct {
		DBName string
		Role   string
	}
	err := r.db.WithContext(ctx).
		Table("`databases` AS d").
		Select("d.name AS db_name, du.username AS role").
		Joins("INNER JOIN database_user_grants g ON g.database_id = d.id").
		Joins("INNER JOIN database_users du ON du.id = g.database_user_id").
		Where("d.engine = ? AND du.engine = ?", "postgres", "postgres").
		Order("d.name ASC, du.username ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, translate(err)
	}
	out := make(map[string][]string, len(rows))
	for _, row := range rows {
		out[row.DBName] = append(out[row.DBName], row.Role)
	}
	return out, nil
}
