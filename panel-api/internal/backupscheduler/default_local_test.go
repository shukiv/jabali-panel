package backupscheduler

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type dlFakeSettings struct {
	repository.ServerSettingsRepository
	enabled bool
}

func (f dlFakeSettings) Get(context.Context) (*models.ServerSettings, error) {
	return &models.ServerSettings{DefaultLocalBackupsEnabled: f.enabled}, nil
}

type dlFakeScheds struct {
	repository.BackupScheduleRepository
	rows      []models.BackupSchedule
	created   *models.BackupSchedule
	updated   *models.BackupSchedule
	setUsers  bool
	setDests  bool
	dests     []models.BackupDestination
	createErr error
}

func (f *dlFakeScheds) List(context.Context) ([]models.BackupSchedule, error) { return f.rows, nil }
func (f *dlFakeScheds) Create(_ context.Context, s *models.BackupSchedule) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.created = s
	f.rows = append(f.rows, *s)
	return nil
}
func (f *dlFakeScheds) Update(_ context.Context, s *models.BackupSchedule) error {
	f.updated = s
	return nil
}
func (f *dlFakeScheds) ReplaceUsers(_ context.Context, _ string, _ []string) error {
	f.setUsers = true
	return nil
}
func (f *dlFakeScheds) ReplaceDestinations(_ context.Context, _ string, _ []string) error {
	f.setDests = true
	return nil
}
func (f *dlFakeScheds) GetDestinations(context.Context, string) ([]models.BackupDestination, error) {
	return f.dests, nil
}

type dlFakeDests struct {
	repository.BackupDestinationRepository
	rows    []models.BackupDestination
	created *models.BackupDestination
}

func (f *dlFakeDests) List(context.Context) ([]models.BackupDestination, error) { return f.rows, nil }
func (f *dlFakeDests) Create(_ context.Context, d *models.BackupDestination) error {
	f.created = d
	f.rows = append(f.rows, *d)
	return nil
}

func dlScheduler(enabled bool, sch *dlFakeScheds, dst *dlFakeDests) *Scheduler {
	return &Scheduler{deps: Deps{
		Settings:     dlFakeSettings{enabled: enabled},
		Schedules:    sch,
		Destinations: dst,
		Log:          slog.Default(),
	}}
}

func TestEnsureDefaultLocalBackup_CreatesWhenOn(t *testing.T) {
	sch := &dlFakeScheds{}
	dst := &dlFakeDests{}
	dlScheduler(true, sch, dst).ensureDefaultLocalBackup(context.Background())

	require.NotNil(t, sch.created, "managed schedule must be created")
	assert.True(t, sch.created.IsManagedDefault)
	assert.Equal(t, models.BackupScheduleKindAccount, sch.created.Kind)
	assert.Nil(t, sch.created.UserID)
	assert.Equal(t, "0 5 * * *", sch.created.CronExpr)
	assert.Equal(t, "full", sch.created.Content)
	assert.True(t, sch.created.Enabled)
	require.NotNil(t, sch.created.KeepDaily)
	assert.Equal(t, 3, *sch.created.KeepDaily)
	// NextRunAt MUST be set — a NULL next_run_at is never due (ListDue filters it),
	// so the schedule would never fire.
	require.NotNil(t, sch.created.NextRunAt, "NextRunAt must be set from the cron")
	assert.True(t, sch.created.NextRunAt.After(time.Now()), "first run should be in the future")
	assert.True(t, sch.setUsers, "empty user list (all tenants) must be set")
	assert.True(t, sch.setDests, "local destination must be linked")
	require.NotNil(t, dst.created, "a Local destination must be created")
	assert.Equal(t, models.BackupDestinationKindLocal, dst.created.Kind)
}

func TestEnsureDefaultLocalBackup_ReusesExistingLocalDest(t *testing.T) {
	sch := &dlFakeScheds{}
	dst := &dlFakeDests{rows: []models.BackupDestination{{ID: "d1", Kind: models.BackupDestinationKindLocal}}}
	dlScheduler(true, sch, dst).ensureDefaultLocalBackup(context.Background())
	assert.Nil(t, dst.created, "an existing local destination must be reused, not re-created")
	require.NotNil(t, sch.created)
}

func TestEnsureDefaultLocalBackup_DisablesWhenOff(t *testing.T) {
	keep := 3
	sch := &dlFakeScheds{rows: []models.BackupSchedule{
		{ID: "m1", IsManagedDefault: true, Enabled: true, KeepDaily: &keep},
		{ID: "t1", IsManagedDefault: false, Enabled: true}, // a tenant schedule — must NOT be touched
	}}
	dlScheduler(false, sch, &dlFakeDests{}).ensureDefaultLocalBackup(context.Background())
	require.NotNil(t, sch.updated, "the managed schedule must be disabled")
	assert.Equal(t, "m1", sch.updated.ID)
	assert.False(t, sch.updated.Enabled)
}

func TestEnsureDefaultLocalBackup_OffNoManaged_NoOp(t *testing.T) {
	sch := &dlFakeScheds{}
	dlScheduler(false, sch, &dlFakeDests{}).ensureDefaultLocalBackup(context.Background())
	assert.Nil(t, sch.created)
	assert.Nil(t, sch.updated)
}

func TestEnsureDefaultLocalBackup_ConcurrentCreateConflictIsBenign(t *testing.T) {
	// A second panel instance loses the create race → the unique index rejects it
	// with ErrConflict. The provisioner must swallow it (the winner's row exists),
	// not spin or wire up a phantom schedule.
	sch := &dlFakeScheds{createErr: repository.ErrConflict}
	dlScheduler(true, sch, &dlFakeDests{}).ensureDefaultLocalBackup(context.Background())
	assert.False(t, sch.setUsers, "on conflict, bail before wiring users/destinations")
}
