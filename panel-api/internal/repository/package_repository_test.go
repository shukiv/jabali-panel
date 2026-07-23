package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"database/sql"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newMockPackageDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock, *sql.DB) {
	t.Helper()
	raw, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT VERSION()")).
		WillReturnRows(sqlmock.NewRows([]string{"VERSION()"}).AddRow("10.11.6-MariaDB"))
	gdb, err := gorm.Open(mysql.New(mysql.Config{Conn: raw, SkipInitializeWithVersion: false}),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	return gdb, mock, raw
}

func TestPackage_EnsureDefaults_NoOpWhenPopulated(t *testing.T) {
	db, mock, raw := newMockPackageDB(t)
	defer raw.Close()
	repo := NewPackageRepository(db)

	// One existing package -> EnsureDefaults must not INSERT anything.
	mock.ExpectQuery("SELECT count.* FROM .hosting_packages.").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))

	require.NoError(t, repo.EnsureDefaults(context.Background()))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPackage_EnsureDefaults_SeedsThreeWhenEmpty(t *testing.T) {
	db, mock, raw := newMockPackageDB(t)
	defer raw.Close()
	repo := NewPackageRepository(db)

	mock.ExpectQuery("SELECT count.* FROM .hosting_packages.").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectBegin()
	for i := 0; i < 3; i++ {
		mock.ExpectExec("INSERT INTO .hosting_packages.").
			WillReturnResult(sqlmock.NewResult(int64(i+1), 1))
	}
	mock.ExpectCommit()

	require.NoError(t, repo.EnsureDefaults(context.Background()))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPackage_defaultPackages_ShapeAndTiers(t *testing.T) {
	defs := defaultPackages(time.Now().UTC())
	require.Len(t, defs, 3)
	require.Equal(t, "Tier 1", defs[0].Name)
	require.Equal(t, "Tier 2", defs[1].Name)
	require.Equal(t, "Tier 3", defs[2].Name)
	// Tiers must be strictly increasing on disk so the UI sorts sensibly.
	require.Less(t, defs[0].DiskQuotaMB, defs[1].DiskQuotaMB)
	require.Less(t, defs[1].DiskQuotaMB, defs[2].DiskQuotaMB)
	// Every default sets a real (non-zero) cap — zero means unlimited.
	for _, d := range defs {
		require.NotZero(t, d.DiskQuotaMB)
		require.NotZero(t, d.MaxDomains)
		require.NotEmpty(t, d.ID)
		require.False(t, d.CreatedAt.IsZero())
	}
}

// Regression (GH #170 / #402): the Update Select allowlist must carry every
// column the API handler can set. docker_app_slugs and php_exec_enabled were
// both silently dropped — selecting apps / toggling PHP-exec on edit never
// persisted. Guard each critical column appears in the emitted UPDATE.
func updatePersistsColumn(t *testing.T, colRegex string) {
	t.Helper()
	db, mock, raw := newMockPackageDB(t)
	defer raw.Close()
	repo := NewPackageRepository(db)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE .hosting_packages. SET .*" + colRegex).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	pkg := &models.HostingPackage{
		ID:             "01HXPKG0000000000000000000",
		Name:           "p",
		DockerAppSlugs: "wordpress,ghost",
		PHPExecEnabled: true,
		MaxDockerApps:  5,
		UpdatedAt:      time.Now().UTC(),
	}
	require.NoError(t, repo.Update(context.Background(), pkg))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPackage_Update_PersistsDockerAppSlugs(t *testing.T) {
	updatePersistsColumn(t, "docker_app_slugs")
}

func TestPackage_Update_PersistsPHPExecEnabled(t *testing.T) {
	updatePersistsColumn(t, "php_exec_enabled")
}

// GH #454: the tenant-backup entitlement columns were added to the model + API
// update handler but missed from the Update Select allowlist, so admins saw a
// success toast yet the backup limits never persisted (reverted on reload) —
// tenants never received the backup permissions. Guard every backup column
// appears in the emitted UPDATE (each would be absent with the old allowlist).
func TestPackage_Update_PersistsBackupLimits(t *testing.T) {
	updatePersistsColumn(t, "max_backups")
	updatePersistsColumn(t, "scheduled_backups_enabled")
	updatePersistsColumn(t, "allowed_backup_destination_kinds")
	updatePersistsColumn(t, "backup_retention_policy")
}
