package backupscheduleops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/paneltime"
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

type recordedUpdate struct {
	s       *models.BackupSchedule
	destIDs *[]string
	userIDs *[]string
}

type fakeScheduleWriter struct {
	calls   []recordedCreate
	err     error
	getRow  *models.BackupSchedule
	getErr  error
	updates []recordedUpdate
	updErr  error
}

func (f *fakeScheduleWriter) CreateWithMemberships(ctx context.Context, s *models.BackupSchedule, destIDs, userIDs []string) error {
	f.calls = append(f.calls, recordedCreate{s: s, destIDs: destIDs, userIDs: userIDs})
	return f.err
}

func (f *fakeScheduleWriter) Get(ctx context.Context, id string) (*models.BackupSchedule, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.getRow, nil
}

func (f *fakeScheduleWriter) UpdateWithMemberships(ctx context.Context, s *models.BackupSchedule, destIDs, userIDs *[]string) error {
	f.updates = append(f.updates, recordedUpdate{s: s, destIDs: destIDs, userIDs: userIDs})
	return f.updErr
}

// updateDeps builds a leaf bound to a stored schedule of the given kind.
func updateDeps(kind string, users map[string]*models.User) (Deps, *fakeScheduleWriter) {
	w := &fakeScheduleWriter{getRow: &models.BackupSchedule{ID: "s1", Kind: kind, Enabled: true}}
	return Deps{Schedules: w, Users: &fakeUserFinder{users: users}}, w
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

// --- Update ---

// The load-bearing guard for this slice: the update path must refuse an admin
// target with ZERO writes, exactly as Create does. This is what the operator
// CLI's hand-rolled update lacked. Falsify: delete the u.IsAdmin branch in
// Update → the writer records an update.
func TestUpdate_AccountRejectsAdminUser_ZeroWrites(t *testing.T) {
	d, w := updateDeps(models.BackupScheduleKindAccount, map[string]*models.User{"u1": {ID: "u1", IsAdmin: true}})
	s, err := Update(context.Background(), d, UpdateInput{ID: "s1", UserIDs: &[]string{"u1"}})
	require.Nil(t, s)
	require.ErrorIs(t, err, ErrAdminUser)
	var ure *UserRejectedError
	require.ErrorAs(t, err, &ure)
	require.Equal(t, "u1", ure.UserID)
	require.Empty(t, w.updates, "no write when a target is rejected")
}

func TestUpdate_AccountRejectsMissingUser_ZeroWrites(t *testing.T) {
	d, w := updateDeps(models.BackupScheduleKindAccount, map[string]*models.User{})
	s, err := Update(context.Background(), d, UpdateInput{ID: "s1", UserIDs: &[]string{"ghost"}})
	require.Nil(t, s)
	require.ErrorIs(t, err, ErrUserNotFound)
	require.Empty(t, w.updates)
}

// Invalid cron aborts before any write. Falsify: move NextFire below the
// UpdateWithMemberships call → the writer records an update.
func TestUpdate_InvalidCron_ZeroWrites(t *testing.T) {
	d, w := updateDeps(models.BackupScheduleKindAccount, map[string]*models.User{})
	bad := "not a cron"
	s, err := Update(context.Background(), d, UpdateInput{ID: "s1", CronExpr: &bad})
	require.Nil(t, s)
	require.ErrorIs(t, err, ErrInvalidCron)
	require.Empty(t, w.updates)
}

// A system schedule keeps no per-user membership and no include-system flag, so
// those patch fields are ignored — and because the membership isn't touched, an
// admin id in the patch is neither validated nor attached (no error, no write of
// users). Falsify: drop the `s.Kind == account` gate on the user branch → the
// admin id is validated and the call errors.
func TestUpdate_SystemKind_IgnoresUserAndIncludeFlag(t *testing.T) {
	d, w := updateDeps(models.BackupScheduleKindSystem, map[string]*models.User{"u1": {ID: "u1", IsAdmin: true}})
	inc := true
	s, err := Update(context.Background(), d, UpdateInput{ID: "s1", UserIDs: &[]string{"u1"}, IncludeSystemBackup: &inc})
	require.NoError(t, err)
	require.Len(t, w.updates, 1)
	require.Nil(t, w.updates[0].userIDs, "system schedule membership left untouched")
	require.False(t, s.IncludeSystemBackup, "include-system ignored on a system schedule")
}

// nil membership pointers leave both memberships untouched (only fields change).
func TestUpdate_NilMembership_LeavesUntouched(t *testing.T) {
	d, w := updateDeps(models.BackupScheduleKindAccount, nil)
	on := true
	s, err := Update(context.Background(), d, UpdateInput{ID: "s1", Enabled: &on})
	require.NoError(t, err)
	require.Len(t, w.updates, 1)
	require.Nil(t, w.updates[0].destIDs)
	require.Nil(t, w.updates[0].userIDs)
	require.True(t, s.Enabled)
}

// An empty (non-nil) account user patch clears the membership → fan-out to every
// non-admin at tick time. The distinction from nil is load-bearing.
func TestUpdate_EmptyUserPatch_ClearsMembership(t *testing.T) {
	d, w := updateDeps(models.BackupScheduleKindAccount, map[string]*models.User{})
	s, err := Update(context.Background(), d, UpdateInput{ID: "s1", UserIDs: &[]string{}})
	require.NoError(t, err)
	require.Len(t, w.updates, 1)
	require.NotNil(t, w.updates[0].userIDs, "empty patch still replaces (clears) the set")
	require.Empty(t, *w.updates[0].userIDs)
	require.Empty(t, s.UserIDs)
}

// A load error from Get propagates unchanged and nothing is written.
func TestUpdate_GetError_Propagates(t *testing.T) {
	sentinel := errors.New("row gone")
	d, w := updateDeps(models.BackupScheduleKindAccount, nil)
	w.getErr = sentinel
	s, err := Update(context.Background(), d, UpdateInput{ID: "s1", Enabled: boolPtr(true)})
	require.Nil(t, s)
	require.ErrorIs(t, err, sentinel)
	require.Empty(t, w.updates)
}

func TestUpdate_HappyPath_AppliesPatch(t *testing.T) {
	d, w := updateDeps(models.BackupScheduleKindAccount, map[string]*models.User{"u1": {ID: "u1"}})
	cron := "0 5 * * *"
	off := false
	keep := 7
	s, err := Update(context.Background(), d, UpdateInput{
		ID: "s1", CronExpr: &cron, Enabled: &off, KeepDaily: &keep, UserIDs: &[]string{"u1"},
	})
	require.NoError(t, err)
	require.Len(t, w.updates, 1)
	require.Equal(t, cron, s.CronExpr)
	require.False(t, s.Enabled)
	require.NotNil(t, s.KeepDaily)
	require.Equal(t, 7, *s.KeepDaily)
	require.NotNil(t, s.NextRunAt)
	require.Equal(t, []string{"u1"}, s.UserIDs)
}

func boolPtr(b bool) *bool { return &b }

// --- GH #2246: an account's own (interval) schedule edited from the admin side ---

type fakeSettings struct{ s *models.ServerSettings }

func (f fakeSettings) Get(ctx context.Context) (*models.ServerSettings, error) { return f.s, nil }

// tenantRowDeps binds the leaf to an account's own schedule as the tenant
// Backups page creates it: user_id set, a cadence, a content, no cron_expr.
func tenantRowDeps(cadence string) (Deps, *fakeScheduleWriter, time.Time) {
	owner := "u1"
	next := time.Now().UTC().Add(90 * time.Minute).Truncate(time.Minute)
	w := &fakeScheduleWriter{getRow: &models.BackupSchedule{
		ID: "s1", Kind: models.BackupScheduleKindAccount, UserID: &owner,
		Cadence: cadence, Content: models.BackupContentDatabase, Enabled: true, NextRunAt: &next,
	}}
	return Deps{Schedules: w, Users: &fakeUserFinder{users: map[string]*models.User{
		"u1": {ID: "u1"}, "u2": {ID: "u2"},
	}}}, w, next
}

// The bug: the admin form sent the "Daily 03:00" it fell back to as cron_expr,
// and the leaf moved the interval schedule's next run to the next 03:00 while
// it kept firing on its interval. Falsify: drop the s.Cadence != "" refusal
// → the writer records an update with the cron time.
func TestUpdate_IntervalSchedule_RefusesCron_ZeroWrites(t *testing.T) {
	d, w, _ := tenantRowDeps(internalbackup.CadenceEvery6h)
	cron := validCron
	s, err := Update(context.Background(), d, UpdateInput{ID: "s1", CronExpr: &cron})
	require.Nil(t, s)
	require.ErrorIs(t, err, ErrIntervalSchedule)
	require.Empty(t, w.updates)
}

// A new cadence re-times the schedule on that interval (whole-day window when
// no settings are wired). Falsify: skip the NextTenantRun assignment → the
// next run stays 90 minutes out instead of moving to one hour.
func TestUpdate_IntervalSchedule_CadenceChangeRetimes(t *testing.T) {
	d, w, _ := tenantRowDeps(internalbackup.CadenceEvery6h)
	hourly := internalbackup.CadenceHourly
	before := time.Now().UTC()
	s, err := Update(context.Background(), d, UpdateInput{ID: "s1", Cadence: &hourly})
	require.NoError(t, err)
	require.Len(t, w.updates, 1)
	require.Equal(t, internalbackup.CadenceHourly, s.Cadence)
	require.Empty(t, s.CronExpr, "an interval schedule keeps no cron expression")
	require.NotNil(t, s.NextRunAt)
	require.WithinDuration(t, before.Add(time.Hour), *s.NextRunAt, 2*time.Minute)
}

// With the window enforced, the re-timed run lands inside it, as on the tenant
// page. Falsify: pass TenantWindow{} instead of the settings window → the run
// lands one hour out, outside a window that excludes it.
func TestUpdate_IntervalSchedule_CadenceChangeHonoursWindow(t *testing.T) {
	d, _, _ := tenantRowDeps(internalbackup.CadenceEvery6h)
	// A one-hour window that starts three hours from now, on the panel clock.
	start := time.Now().Add(3 * time.Hour).In(paneltime.Location())
	set := &models.ServerSettings{
		TenantBackupWindowEnforce: true,
		TenantBackupWindowStart:   start.Format("15:04"),
		TenantBackupWindowEnd:     start.Add(time.Hour).Format("15:04"),
	}
	d.Settings = fakeSettings{s: set}
	hourly := internalbackup.CadenceHourly
	s, err := Update(context.Background(), d, UpdateInput{ID: "s1", Cadence: &hourly})
	require.NoError(t, err)
	want := internalbackup.NextTenantRun(time.Now().UTC(), hourly, internalbackup.EffectiveTenantWindow(true, set.TenantBackupWindowStart, set.TenantBackupWindowEnd), "s1")
	require.WithinDuration(t, want, *s.NextRunAt, 2*time.Minute)
	require.Greater(t, s.NextRunAt.Sub(time.Now().UTC()), 90*time.Minute, "the run waits for the window")
}

// Saving without changing the cadence (retention, destination, on/off) keeps
// the next run, so a weekly schedule is not pushed a week out by an edit.
func TestUpdate_IntervalSchedule_SameCadenceKeepsNextRun(t *testing.T) {
	d, _, next := tenantRowDeps(internalbackup.CadenceWeekly)
	weekly := internalbackup.CadenceWeekly
	keep := 3
	s, err := Update(context.Background(), d, UpdateInput{ID: "s1", Cadence: &weekly, KeepDaily: &keep})
	require.NoError(t, err)
	require.Equal(t, next, *s.NextRunAt)
}

func TestUpdate_InvalidCadence_ZeroWrites(t *testing.T) {
	d, w, _ := tenantRowDeps(internalbackup.CadenceDaily)
	for _, c := range []string{"", "monthly", "every_2h"} {
		c := c
		s, err := Update(context.Background(), d, UpdateInput{ID: "s1", Cadence: &c})
		require.Nil(t, s, c)
		require.ErrorIs(t, err, ErrInvalidCadence, c)
	}
	require.Empty(t, w.updates)
}

// A cron schedule has no cadence; setting one would silently move it onto the
// tenant window path. Falsify: drop the s.Cadence == "" refusal.
func TestUpdate_CronSchedule_RefusesCadence_ZeroWrites(t *testing.T) {
	d, w := updateDeps(models.BackupScheduleKindAccount, nil)
	daily := internalbackup.CadenceDaily
	s, err := Update(context.Background(), d, UpdateInput{ID: "s1", Cadence: &daily})
	require.Nil(t, s)
	require.ErrorIs(t, err, ErrCronSchedule)
	require.Empty(t, w.updates)
}

// Content is what each run backs up; the admin can now change it, and it is
// validated with the tenant's allow-list.
func TestUpdate_Content_AppliedAndValidated(t *testing.T) {
	d, w, _ := tenantRowDeps(internalbackup.CadenceDaily)
	files := models.BackupContentFiles
	s, err := Update(context.Background(), d, UpdateInput{ID: "s1", Content: &files})
	require.NoError(t, err)
	require.Equal(t, models.BackupContentFiles, s.Content)

	bogus := "everything"
	s, err = Update(context.Background(), d, UpdateInput{ID: "s1", Content: &bogus})
	require.Nil(t, s)
	require.ErrorIs(t, err, ErrInvalidContent)
	require.Len(t, w.updates, 1, "the invalid patch wrote nothing")
}

func TestUpdate_SystemKind_IgnoresContent(t *testing.T) {
	d, w := updateDeps(models.BackupScheduleKindSystem, nil)
	w.getRow.Content = models.BackupContentFull
	db := models.BackupContentDatabase
	s, err := Update(context.Background(), d, UpdateInput{ID: "s1", Content: &db})
	require.NoError(t, err)
	require.Equal(t, models.BackupContentFull, s.Content)
}

// An account's own schedule backs up that account only: the patch may not
// widen it to other users, every user, or the system backup. Falsify: drop the
// owner check → the every-user patch is written.
func TestUpdate_AccountSchedule_StaysOnItsAccount(t *testing.T) {
	for name, ids := range map[string][]string{
		"every user": {},
		"other user": {"u2"},
		"added user": {"u1", "u2"},
	} {
		d, w, _ := tenantRowDeps(internalbackup.CadenceDaily)
		ids := ids
		s, err := Update(context.Background(), d, UpdateInput{ID: "s1", UserIDs: &ids})
		require.Nil(t, s, name)
		require.ErrorIs(t, err, ErrAccountSchedule, name)
		require.Empty(t, w.updates, name)
	}

	d, w, _ := tenantRowDeps(internalbackup.CadenceDaily)
	inc := true
	s, err := Update(context.Background(), d, UpdateInput{ID: "s1", IncludeSystemBackup: &inc})
	require.Nil(t, s)
	require.ErrorIs(t, err, ErrAccountSchedule)
	require.Empty(t, w.updates)

	// Re-sending the owner, or include-system off, is what an unchanged save
	// sends; it goes through.
	off := false
	s, err = Update(context.Background(), d, UpdateInput{ID: "s1", UserIDs: &[]string{"u1"}, IncludeSystemBackup: &off})
	require.NoError(t, err)
	require.Equal(t, []string{"u1"}, s.UserIDs)
}

// Create stores the content each run backs up; a system schedule is always full.
func TestCreate_Content(t *testing.T) {
	d, w := accountDeps(map[string]*models.User{"u1": {ID: "u1"}})
	s, err := Create(context.Background(), d, CreateInput{
		Kind: models.BackupScheduleKindAccount, UserIDs: []string{"u1"}, CronExpr: validCron,
		Content: models.BackupContentDatabase,
	})
	require.NoError(t, err)
	require.Equal(t, models.BackupContentDatabase, w.calls[0].s.Content)
	require.Equal(t, models.BackupContentDatabase, s.Content)

	s, err = Create(context.Background(), d, CreateInput{Kind: models.BackupScheduleKindAccount, CronExpr: validCron})
	require.NoError(t, err)
	require.Equal(t, models.BackupContentFull, s.Content, `"" = full`)

	s, err = Create(context.Background(), d, CreateInput{
		Kind: models.BackupScheduleKindSystem, CronExpr: validCron, Content: models.BackupContentFiles,
	})
	require.NoError(t, err)
	require.Equal(t, models.BackupContentFull, s.Content)

	s, err = Create(context.Background(), d, CreateInput{Kind: models.BackupScheduleKindAccount, CronExpr: validCron, Content: "bogus"})
	require.Nil(t, s)
	require.ErrorIs(t, err, ErrInvalidContent)
	require.Len(t, w.calls, 3)
}
