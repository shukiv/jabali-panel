//go:build integration

// GH #2276: the domain_composer_settings migration adds a (user_id, state)
// key to composer_runs. InnoDB lets that key take over from the index it made
// for fk_composer_runs_user and drops the old one, so a down migration that
// only drops the new key fails (ERROR 1553: needed in a foreign key
// constraint). It did on the test server. Nothing else in CI runs a down.
//
//	JABALI_TEST_DATABASE_URL=... go test -tags integration ./panel-api/internal/db/ -run ComposerSettings

package db

import (
	"io/fs"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// composerSettingsVersion is the migration's number, read from its file name
// so a renumber on rebase doesn't break this test.
func composerSettingsVersion(t *testing.T) uint {
	t.Helper()
	names, err := fs.Glob(migrationsFS, "migrations/*_domain_composer_settings.up.sql")
	require.NoError(t, err)
	require.Len(t, names, 1)
	n, err := strconv.ParseUint(strings.SplitN(strings.TrimPrefix(names[0], "migrations/"), "_", 2)[0], 10, 32)
	require.NoError(t, err)
	return uint(n)
}

func TestComposerSettings_DownAndUpAgain(t *testing.T) {
	dsn := ft264DSN(t)
	v := composerSettingsVersion(t)
	migrateToExact(t, dsn, v)

	m, closeM, err := newMigrator(dsn)
	require.NoError(t, err)
	defer closeM()
	require.NoError(t, m.Migrate(v-1), "down from %d", v)
	got, dirty, err := m.Version()
	require.NoError(t, err)
	require.False(t, dirty)
	require.EqualValues(t, v-1, got)
	require.NoError(t, m.Migrate(v), "up to %d again", v)
}
