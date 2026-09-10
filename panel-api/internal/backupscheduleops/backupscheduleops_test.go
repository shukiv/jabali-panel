package backupscheduleops

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

const validCron = "0 3 * * *"

// --- fakes ---

type fakeUserFinder struct {
	users map[string]*models.User
}

func (f *fakeUserFinder) FindByID(ctx context.Context, id string) (*models.User, error) {
	u, ok := f.users[id]
	if !ok {
		return nil, nil // not found — the leaf treats a nil user as ErrUserNotFound
	}
	return u, nil
}

type recordedCreate struct {
	s       *models.BackupSchedule
	destIDs []string
	userIDs []string
}

type fakeScheduleWriter struct {
	calls []recordedCreate
	err   error
}

func (f *fakeScheduleWriter) CreateWithMemberships(ctx context.Context, s *models.BackupSchedule, destIDs, userIDs []string) error {
	f.calls = append(f.calls, recordedCreate{s: s, destIDs: destIDs, userIDs: userIDs})
	return f.err
}

func accountDeps(users map[string]*models.User) (Deps, *fakeScheduleWriter) {
	w := &fakeScheduleWriter{}
	return Deps{Schedules: w, Users: &fakeUserFinder{users: users}}, w
}

// An admin account is panel-only — scheduling it as a backup target is refused
// before any write. Falsify: delete the u.IsAdmin branch → the writer records a
// create.
func TestCreate_AccountRejectsAdminUser_ZeroWrites(t *testing.T) {
	d, w := accountDeps(map[string]*models.User{"u1": {ID: "u1", IsAdmin: true}})
	s, err := Create(context.Background(), d, CreateInput{
		Kind: models.BackupScheduleKindAccount, UserIDs: []string{"u1"}, CronExpr: validCron,
	})
	require.Nil(t, s)
	require.ErrorIs(t, err, ErrAdminUser)
	var ure *UserRejectedError
	require.ErrorAs(t, err, &ure)
	require.Equal(t, "u1", ure.UserID)
	require.Empty(t, w.calls, "no write when a target is rejected")
}

// A target that does not resolve is refused with zero writes and names the id.
func TestCreate_AccountRejectsMissingUser_ZeroWrites(t *testing.T) {
	d, w := accountDeps(map[string]*models.User{})
	s, err := Create(context.Background(), d, CreateInput{
		Kind: models.BackupScheduleKindAccount, UserIDs: []string{"ghost"}, CronExpr: validCron,
	})
	require.Nil(t, s)
	require.ErrorIs(t, err, ErrUserNotFound)
	var ure *UserRejectedError
	require.ErrorAs(t, err, &ure)
	require.Equal(t, "ghost", ure.UserID)
	require.Empty(t, w.calls)
}

func TestCreate_InvalidKind_ZeroWrites(t *testing.T) {
	d, w := accountDeps(nil)
	s, err := Create(context.Background(), d, CreateInput{Kind: "bogus", CronExpr: validCron})
	require.Nil(t, s)
	require.ErrorIs(t, err, ErrInvalidKind)
	require.Empty(t, w.calls)
}

// Invalid cron aborts before any write. Falsify: move NextFire below the
// CreateWithMemberships call → the writer records a create.
func TestCreate_InvalidCron_ZeroWrites(t *testing.T) {
	d, w := accountDeps(map[string]*models.User{"u1": {ID: "u1"}})
	s, err := Create(context.Background(), d, CreateInput{
		Kind: models.BackupScheduleKindAccount, UserIDs: []string{"u1"}, CronExpr: "not a cron",
	})
	require.Nil(t, s)
	require.ErrorIs(t, err, ErrInvalidCron)
	require.Empty(t, w.calls)
}

// A system schedule carries no users and no include-system flag — and the
// admin-target check does NOT apply (so even an admin id in the input is simply
// dropped, not an error). Guards the kind normalization.
func TestCreate_SystemKind_DropsUsersAndIncludeFlag(t *testing.T) {
	d, w := accountDeps(map[string]*models.User{"u1": {ID: "u1", IsAdmin: true}})
	s, err := Create(context.Background(), d, CreateInput{
		Kind: models.BackupScheduleKindSystem, UserIDs: []string{"u1"}, IncludeSystemBackup: true, CronExpr: validCron,
	})
	require.NoError(t, err)
	require.Len(t, w.calls, 1)
	require.Nil(t, w.calls[0].userIDs, "system schedule has no user membership")
	require.False(t, s.IncludeSystemBackup, "include-system normalised off for a system schedule")
}

func TestCreate_AccountHappyPath_PersistsWithMemberships(t *testing.T) {
	d, w := accountDeps(map[string]*models.User{"u1": {ID: "u1"}})
	s, err := Create(context.Background(), d, CreateInput{
		Kind:           models.BackupScheduleKindAccount,
		UserIDs:        []string{"u1", ""}, // empty id is cleaned out
		DestinationIDs: []string{"d1"},
		CronExpr:       validCron,
	})
	require.NoError(t, err)
	require.Len(t, w.calls, 1)
	require.Equal(t, []string{"u1"}, w.calls[0].userIDs)
	require.Equal(t, []string{"d1"}, w.calls[0].destIDs)
	require.Equal(t, models.BackupScheduleKindAccount, s.Kind)
	require.True(t, s.Enabled, "enabled defaults true when unset")
	require.Equal(t, []string{"u1"}, s.UserIDs)
	require.NotNil(t, s.NextRunAt)
	require.NotEmpty(t, s.ID)
}

func TestCreate_EnabledFalseHonored(t *testing.T) {
	d, _ := accountDeps(map[string]*models.User{"u1": {ID: "u1"}})
	no := false
	s, err := Create(context.Background(), d, CreateInput{
		Kind: models.BackupScheduleKindAccount, UserIDs: []string{"u1"}, CronExpr: validCron, Enabled: &no,
	})
	require.NoError(t, err)
	require.False(t, s.Enabled)
}

// A nil Users finder skips the admin check (the REST test seam) — the create
// still persists.
func TestCreate_NilUserFinder_SkipsAdminCheck(t *testing.T) {
	w := &fakeScheduleWriter{}
	s, err := Create(context.Background(), Deps{Schedules: w, Users: nil}, CreateInput{
		Kind: models.BackupScheduleKindAccount, UserIDs: []string{"u1"}, CronExpr: validCron,
	})
	require.NoError(t, err)
	require.Len(t, w.calls, 1)
	require.Equal(t, []string{"u1"}, s.UserIDs)
}

func TestCreate_WriterError_Propagates(t *testing.T) {
	sentinel := errors.New("db down")
	w := &fakeScheduleWriter{err: sentinel}
	d := Deps{Schedules: w, Users: &fakeUserFinder{users: map[string]*models.User{"u1": {ID: "u1"}}}}
	s, err := Create(context.Background(), d, CreateInput{
		Kind: models.BackupScheduleKindAccount, UserIDs: []string{"u1"}, CronExpr: validCron,
	})
	require.Nil(t, s)
	require.ErrorIs(t, err, sentinel)
}
