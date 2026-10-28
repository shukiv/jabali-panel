package backupscheduleops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// An account's own schedule (user_id set) is one the account can also edit on
// its Backups page. GH #2246: whoever creates or edits it (the account, an
// admin, the CLI), it must stay within what that page allows, or the account
// gets a schedule its page can't take: more schedules than its plan, a
// destination it may not use, more copies than its plan keeps. These are the
// rules the tenant handlers apply (internal/api/backups.go createSchedule and
// validateScheduleUpsert), in the same order.

var (
	// ErrPlanUnavailable fails closed: a plan-bound value can't be checked
	// because the account's plan (or the destination list) can't be read.
	ErrPlanUnavailable            = errors.New("the account's hosting plan could not be read")
	ErrScheduledBackupsNotAllowed = errors.New("the account's hosting plan does not allow scheduled backups")
	ErrSingleSchedulePlan         = errors.New("the account's hosting plan allows one backup schedule, which the account sets on its Backups page; raise the plan's backup schedule limit to add interval schedules")
	ErrScheduleLimit              = errors.New("the account has as many backup schedules as its hosting plan allows")
	ErrUnknownDestination         = errors.New("no such backup destination")
	ErrDestinationDisabled        = errors.New("that backup destination is disabled")
	ErrDestinationNotAllowed      = errors.New("the account's hosting plan does not allow that backup destination")
	ErrDestinationRequired        = errors.New("pick a backup destination to enable the schedule")
	ErrOneDestination             = errors.New("an account's own schedule writes to one destination")
	ErrKeepOutOfRange             = errors.New("copies to keep must be within the account's hosting plan")
	ErrKeepDaysOutOfRange         = errors.New("days to keep must fit in the account's hosting plan's backup limit at this schedule's frequency")
)

// PackageFinder reads an account's hosting plan.
type PackageFinder interface {
	FindByID(ctx context.Context, id string) (*models.HostingPackage, error)
}

// DestinationGetter reads one backup destination.
type DestinationGetter interface {
	Get(ctx context.Context, id string) (*models.BackupDestination, error)
}

// AccountScheduleInput is an account's own interval schedule, as the admin
// creates it for the account.
type AccountScheduleInput struct {
	UserID        string
	Content       string
	Cadence       string
	DestinationID string
	KeepDaily     *int
	// KeepWithinDays is the account's "Days to keep" (GH #2300).
	KeepWithinDays *int
	Enabled        bool
}

// CreateForAccount creates an account's own interval schedule, the kind the
// account adds on its Backups page, under the same plan rules. Every refusal
// comes before any write.
func CreateForAccount(ctx context.Context, d Deps, in AccountScheduleInput) (*models.BackupSchedule, error) {
	u, err := findAccount(ctx, d, in.UserID)
	if err != nil {
		return nil, err
	}
	pkg, err := accountPlan(ctx, d, u)
	if err != nil {
		return nil, err
	}
	if !pkg.BackupsEnabled() || !pkg.ScheduledBackupsEnabled {
		return nil, ErrScheduledBackupsNotAllowed
	}
	limit := pkg.MaxBackupSchedulesOrDefault()
	if limit <= 1 {
		return nil, ErrSingleSchedulePlan
	}
	owned, err := d.Schedules.ListForUser(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	n := 0
	for i := range owned {
		if owned[i].Kind == models.BackupScheduleKindAccount {
			n++
		}
	}
	if n >= limit {
		return nil, fmt.Errorf("%w (%d)", ErrScheduleLimit, limit)
	}
	if !internalbackup.ValidCadence(in.Cadence) {
		return nil, ErrInvalidCadence
	}
	if !models.ValidBackupContent(in.Content) {
		return nil, ErrInvalidContent
	}
	destID := strings.TrimSpace(in.DestinationID)
	dests := []string{}
	if destID != "" {
		if err := checkDestination(ctx, d, pkg, destID); err != nil {
			return nil, err
		}
		dests = append(dests, destID)
	}
	if in.Enabled && destID == "" {
		return nil, ErrDestinationRequired
	}
	if err := checkKeep(pkg, in.KeepDaily); err != nil {
		return nil, err
	}
	if err := checkKeepDays(pkg, internalbackup.CadenceRunsPerWeek(in.Cadence), in.KeepWithinDays); err != nil {
		return nil, err
	}

	id := ids.NewULID()
	uid := u.ID
	next := internalbackup.NextTenantRun(time.Now().UTC(), in.Cadence, tenantWindow(ctx, d.Settings), id)
	s := &models.BackupSchedule{
		ID:        id,
		Kind:      models.BackupScheduleKindAccount,
		UserID:    &uid,
		Cadence:   in.Cadence,
		Content:   models.NormalizeBackupContent(in.Content),
		Enabled:   in.Enabled,
		KeepDaily: in.KeepDaily,
		NextRunAt: &next,
	}
	s.KeepWithinDays = in.KeepWithinDays
	if err := d.Schedules.CreateWithMemberships(ctx, s, dests, []string{uid}); err != nil {
		return nil, err
	}
	s.UserIDs = []string{uid}
	return s, nil
}

// checkAccountPatch applies the plan to a patch of an account's own schedule.
// Only the values that change are checked, so turning a schedule off, or
// changing what it backs up or how often, never needs the plan.
func checkAccountPatch(ctx context.Context, d Deps, s *models.BackupSchedule, owner string, in UpdateInput) error {
	destChange := false
	newDest := ""
	if in.DestinationIDs != nil {
		next := cleanIDs(*in.DestinationIDs)
		if len(next) > 1 {
			return ErrOneDestination
		}
		if len(next) == 1 {
			newDest = next[0]
		}
		cur, err := d.Schedules.GetDestinations(ctx, s.ID)
		if err != nil {
			return err
		}
		destChange = len(cur) != len(next) || (len(cur) == 1 && cur[0].ID != newDest)
	}
	enabled := s.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	if in.DestinationIDs != nil && newDest == "" && enabled {
		return ErrDestinationRequired
	}
	keepChange := in.KeepDaily != nil && (s.KeepDaily == nil || *s.KeepDaily != *in.KeepDaily)
	// Days to keep is checked when it changes, and when the frequency does:
	// the same days hold more backups at a higher frequency.
	keepDays := s.KeepWithinDays
	if in.KeepWithinDays != nil {
		keepDays = in.KeepWithinDays
	}
	daysChange := in.KeepWithinDays != nil && (s.KeepWithinDays == nil || *s.KeepWithinDays != *in.KeepWithinDays)
	cadenceChange := in.Cadence != nil && *in.Cadence != s.Cadence && s.Cadence != ""
	daysCheck := keepDays != nil && (daysChange || cadenceChange)
	if !(destChange && newDest != "") && !keepChange && !daysCheck {
		return nil
	}

	u, err := findAccount(ctx, d, owner)
	if err != nil {
		return err
	}
	pkg, err := accountPlan(ctx, d, u)
	if err != nil {
		return err
	}
	if destChange && newDest != "" {
		if err := checkDestination(ctx, d, pkg, newDest); err != nil {
			return err
		}
	}
	if keepChange {
		if err := checkKeep(pkg, in.KeepDaily); err != nil {
			return err
		}
	}
	if daysCheck {
		return checkKeepDays(pkg, scheduleRunsPerWeek(s, in), keepDays)
	}
	return nil
}

// scheduleRunsPerWeek is how many backups the patched schedule makes a week:
// from its cadence, or for a cron-timed schedule from its cron.
func scheduleRunsPerWeek(s *models.BackupSchedule, in UpdateInput) int {
	cadence := s.Cadence
	if in.Cadence != nil && s.Cadence != "" {
		cadence = *in.Cadence
	}
	if cadence != "" {
		return internalbackup.CadenceRunsPerWeek(cadence)
	}
	expr := s.CronExpr
	if in.CronExpr != nil {
		expr = *in.CronExpr
	}
	n, err := internalbackup.CronRunsPerWeek(expr, time.Now().UTC())
	if err != nil {
		return internalbackup.CadenceRunsPerWeek("") // an invalid cron is refused later
	}
	return n
}

// checkKeepDays: 0 (no rule) up to the most days whose backups fit in the
// plan's backup limit at runsPerWeek, as on the account's page.
func checkKeepDays(pkg *models.HostingPackage, runsPerWeek int, days *int) error {
	if days == nil {
		return nil
	}
	if m := internalbackup.MaxKeepWithinDays(pkg.MaxBackups, runsPerWeek); *days < 0 || *days > m {
		return fmt.Errorf("%w (0 to %d)", ErrKeepDaysOutOfRange, m)
	}
	return nil
}

// findAccount resolves the schedule's account; it must exist and not be an
// admin. Without a user lookup the plan can't be found, so it fails closed.
func findAccount(ctx context.Context, d Deps, id string) (*models.User, error) {
	if d.Users == nil {
		return nil, ErrPlanUnavailable
	}
	u, err := d.Users.FindByID(ctx, id)
	if err != nil || u == nil {
		return nil, &UserRejectedError{UserID: id, Reason: ErrUserNotFound}
	}
	if u.IsAdmin {
		return nil, &UserRejectedError{UserID: id, Reason: ErrAdminUser}
	}
	return u, nil
}

// accountPlan reads the account's hosting plan. No plan means no self-service
// backups, as on the account's Backups page; a plan that can't be read fails
// closed.
func accountPlan(ctx context.Context, d Deps, u *models.User) (*models.HostingPackage, error) {
	if u.PackageID == nil || *u.PackageID == "" {
		return nil, ErrScheduledBackupsNotAllowed
	}
	if d.Packages == nil {
		return nil, ErrPlanUnavailable
	}
	pkg, err := d.Packages.FindByID(ctx, *u.PackageID)
	if err != nil || pkg == nil {
		return nil, ErrPlanUnavailable
	}
	return pkg, nil
}

func checkDestination(ctx context.Context, d Deps, pkg *models.HostingPackage, id string) error {
	if d.Destinations == nil {
		return ErrPlanUnavailable
	}
	dest, err := d.Destinations.Get(ctx, id)
	if err != nil || dest == nil {
		return ErrUnknownDestination
	}
	if !dest.Enabled {
		return ErrDestinationDisabled
	}
	if !pkg.AllowsBackupKind(dest.Kind) {
		return fmt.Errorf("%w (%s)", ErrDestinationNotAllowed, dest.Kind)
	}
	return nil
}

// checkKeep: 1 up to the plan's backup limit, as on the account's page.
func checkKeep(pkg *models.HostingPackage, keep *int) error {
	if keep == nil {
		return nil
	}
	if *keep < 1 || *keep > int(pkg.MaxBackups) {
		return fmt.Errorf("%w (1 to %d)", ErrKeepOutOfRange, pkg.MaxBackups)
	}
	return nil
}
