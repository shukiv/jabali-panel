package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// M49 (GH #170): the tenant-scoped reads must filter on user_id AND exclude the
// deleted tombstone, and FindByIDForUser must scope by owner so a tenant can't
// probe another tenant's app IDs.

func TestDockerApp_ListByUserID_FiltersOwnerAndNotDeleted(t *testing.T) {
	db, mock, raw := newMockPackageDB(t)
	defer raw.Close()
	repo := NewDockerAppRepository(db)

	mock.ExpectQuery(`SELECT \* FROM .docker_apps. WHERE user_id = \? AND status <> \?.*ORDER BY created_at DESC`).
		WithArgs("u1", "deleted").
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "slug", "name"}).
			AddRow("a1", "u1", "memos", "notes"))

	apps, err := repo.ListByUserID(context.Background(), "u1")
	require.NoError(t, err)
	require.Len(t, apps, 1)
	require.Equal(t, "a1", apps[0].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDockerApp_CountByUserID_ExcludesDeleted(t *testing.T) {
	db, mock, raw := newMockPackageDB(t)
	defer raw.Close()
	repo := NewDockerAppRepository(db)

	mock.ExpectQuery(`SELECT count\(\*\) FROM .docker_apps. WHERE user_id = \? AND status <> \?`).
		WithArgs("u1", "deleted").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))

	n, err := repo.CountByUserID(context.Background(), "u1")
	require.NoError(t, err)
	require.Equal(t, int64(2), n)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDockerApp_FindByIDForUser_ScopesByOwner(t *testing.T) {
	db, mock, raw := newMockPackageDB(t)
	defer raw.Close()
	repo := NewDockerAppRepository(db)

	mock.ExpectQuery(`SELECT \* FROM .docker_apps. WHERE id = \? AND user_id = \?`).
		WithArgs("a1", "u1", 1). // First appends LIMIT 1
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id"}).AddRow("a1", "u1"))

	app, err := repo.FindByIDForUser(context.Background(), "a1", "u1")
	require.NoError(t, err)
	require.Equal(t, "a1", app.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDockerApp_FindByIDForUser_WrongOwnerIsNotFound(t *testing.T) {
	db, mock, raw := newMockPackageDB(t)
	defer raw.Close()
	repo := NewDockerAppRepository(db)

	mock.ExpectQuery(`SELECT \* FROM .docker_apps. WHERE id = \? AND user_id = \?`).
		WithArgs("a1", "intruder", 1).
		WillReturnError(sqlmock.ErrCancelled) // any miss -> translated error, nil app

	app, err := repo.FindByIDForUser(context.Background(), "a1", "intruder")
	require.Error(t, err)
	require.Nil(t, app)
	require.NoError(t, mock.ExpectationsWereMet())
}

// A host port is one per protocol on the server: 127.0.0.1:P and 0.0.0.0:P
// collide, so the allocator counts every bind interface's rows. skip lets the
// caller pass over ports it knows are taken (the request's own picks, ports
// in use on the host).
func TestDockerApp_FindFreeHostPort_CountsEveryBindInterface(t *testing.T) {
	db, mock, raw := newMockPackageDB(t)
	defer raw.Close()
	repo := NewDockerAppRepository(db)

	mock.ExpectQuery(`SELECT .host_port. FROM .docker_app_published_ports. WHERE protocol = \? ORDER BY host_port ASC`).
		WithArgs("tcp").
		WillReturnRows(sqlmock.NewRows([]string{"host_port"}).AddRow(10000).AddRow(10001))

	got, err := repo.FindFreeHostPort(context.Background(), "tcp", func(p int) bool { return p == 10002 })
	require.NoError(t, err)
	require.Equal(t, 10003, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDockerApp_FindFreeHostPort_ExhaustedPool(t *testing.T) {
	db, mock, raw := newMockPackageDB(t)
	defer raw.Close()
	repo := NewDockerAppRepository(db)

	mock.ExpectQuery(`SELECT .host_port. FROM .docker_app_published_ports. WHERE protocol = \?`).
		WithArgs("udp").
		WillReturnRows(sqlmock.NewRows([]string{"host_port"}))

	_, err := repo.FindFreeHostPort(context.Background(), "udp", func(int) bool { return true })
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDockerApp_HostPortInUse_CountsEveryBindInterface(t *testing.T) {
	db, mock, raw := newMockPackageDB(t)
	defer raw.Close()
	repo := NewDockerAppRepository(db)

	mock.ExpectQuery(`SELECT count\(\*\) FROM .docker_app_published_ports. WHERE \(protocol = \? AND host_port = \?\) AND app_id <> \?`).
		WithArgs("tcp", 10000, "a1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	inUse, err := repo.HostPortInUse(context.Background(), "tcp", 10000, "a1")
	require.NoError(t, err)
	require.True(t, inUse)
	require.NoError(t, mock.ExpectationsWereMet())
}
