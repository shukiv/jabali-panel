package migrate

import (
	"time"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type fakeServerSettings struct {
	row *models.ServerSettings
	err error
}

func (f *fakeServerSettings) Get(_ context.Context) (*models.ServerSettings, error) {
	return f.row, f.err
}
func (f *fakeServerSettings) SetDigestLastSent(context.Context, string) error { return nil }
func (f *fakeServerSettings) RecordDRSync(context.Context, string, string, string) error {
	return nil
}

func (f *fakeServerSettings) ReassertDRPairing(context.Context, string, string, *time.Time) error {
	return nil
}
func (f *fakeServerSettings) Upsert(_ context.Context, _ *models.ServerSettings) error {
	return nil
}
func (f *fakeServerSettings) EnsureVAPID(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func TestResolveWorkingFolder_DefaultsWhenNil(t *testing.T) {
	got := ResolveWorkingFolder(context.Background(), nil)
	require.Equal(t, DefaultWorkingFolder, got)
}

func TestResolveWorkingFolder_DefaultsOnEmptyValue(t *testing.T) {
	s := &fakeServerSettings{row: &models.ServerSettings{WorkingFolder: ""}}
	got := ResolveWorkingFolder(context.Background(), s)
	require.Equal(t, DefaultWorkingFolder, got)
}

func TestResolveWorkingFolder_DefaultsOnRepoError(t *testing.T) {
	s := &fakeServerSettings{err: errors.New("db down")}
	got := ResolveWorkingFolder(context.Background(), s)
	require.Equal(t, DefaultWorkingFolder, got)
}

func TestResolveWorkingFolder_HonorsConfiguredValue(t *testing.T) {
	s := &fakeServerSettings{row: &models.ServerSettings{WorkingFolder: "/mnt/storage/jabali"}}
	got := ResolveWorkingFolder(context.Background(), s)
	require.Equal(t, "/mnt/storage/jabali", got)
}

func TestMigrationsRoot_Default(t *testing.T) {
	require.Equal(t, DefaultWorkingFolder+"/migrations", MigrationsRoot(context.Background(), nil))
}

func TestBackupsRoot_Default(t *testing.T) {
	require.Equal(t, DefaultWorkingFolder+"/backups", BackupsRoot(context.Background(), nil))
}

func TestMigrationsRoot_HonorsConfigured(t *testing.T) {
	s := &fakeServerSettings{row: &models.ServerSettings{WorkingFolder: "/mnt/storage/jabali"}}
	require.Equal(t, "/mnt/storage/jabali/migrations", MigrationsRoot(context.Background(), s))
}
