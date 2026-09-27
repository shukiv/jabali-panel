package repository

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// nonNilTime matches a non-NULL time argument.
type nonNilTime struct{}

func (nonNilTime) Match(v driver.Value) bool {
	_, ok := v.(time.Time)
	return ok
}

// SeedMissing inserts only for hosting users (a username) that have no row,
// in one statement, and is a no-op for a row that appeared meanwhile.
const seedSQL = "(?s)INSERT INTO user_egress_policies .*SELECT u.id, \\?, JSON_ARRAY\\(\\), \\? FROM users u " +
	"LEFT JOIN user_egress_policies p ON p.user_id = u.id " +
	"WHERE p.user_id IS NULL AND u.username IS NOT NULL AND u.username <> '' " +
	"ON DUPLICATE KEY UPDATE"

func TestUserEgressPolicy_SeedMissing_EnforcedHasNoSoakStart(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewUserEgressPolicyRepository(db)

	mock.ExpectExec(seedSQL).WithArgs("enforced", nil).WillReturnResult(sqlmock.NewResult(0, 3))

	n, err := repo.SeedMissing(context.Background(), "enforced", time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 3, n)
	require.NoError(t, mock.ExpectationsWereMet())
}

// A learning row's soak runs from the day it is seeded; with no
// learning_started_at the nightly flip would never mature it.
func TestUserEgressPolicy_SeedMissing_LearningStampsTheSoakStart(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewUserEgressPolicyRepository(db)

	mock.ExpectExec(seedSQL).WithArgs("learning", nonNilTime{}).WillReturnResult(sqlmock.NewResult(0, 1))

	_, err := repo.SeedMissing(context.Background(), "learning", time.Now())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Only enforced or learning may be seeded: seeding "off" would enroll every
// user with the firewall disabled.
func TestUserEgressPolicy_SeedMissing_RefusesOtherStates(t *testing.T) {
	db, mock, raw := newMockBackupDB(t)
	defer raw.Close()
	repo := NewUserEgressPolicyRepository(db)

	for _, state := range []string{"off", "", "ENFORCED"} {
		_, err := repo.SeedMissing(context.Background(), state, time.Now())
		require.ErrorContains(t, err, "seed state", "state %q must be refused before any SQL", state)
	}
	require.NoError(t, mock.ExpectationsWereMet())
}
