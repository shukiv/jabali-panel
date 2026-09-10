// Package backupscheduleops is the transport-neutral Backup Schedule Lifecycle
// leaf (ADR-0083 <area>ops). It owns the create- and update-side policy that
// the admin REST handler, the operator CLI, and (future) the tenant path must
// all apply identically: kind defaulting/validation, admin-user rejection,
// system-kind normalization, cron validation before any write, and a single
// transactional commit of the schedule row together with its destination and
// user links.
//
// Adapters keep what is genuinely theirs: id resolution (CLI resolves
// --user email|username and --destination name into ids; the legacy REST
// user_id single field is promoted into UserIDs), authorization, and output
// shaping. The leaf receives already-resolved ids and returns the persisted
// row or a typed error the adapter maps to its own status codes.
//
// Scope (JAB-307): create, plus the operator-CLI update path (which previously
// skipped the admin-target rejection entirely and wrote the row and its
// memberships in three separate statements). The admin REST update still
// applies this same policy inline; routing it onto Update to drop that
// duplication is the next slice. run-now and the tenant variant remain
// adapter-local. The parent stays open.
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

// Sentinel errors. Adapters test with errors.Is and map to their own codes.
var (
	ErrInvalidKind  = errors.New("invalid backup schedule kind")
	ErrInvalidCron  = errors.New("invalid cron expression")
	ErrAdminUser    = errors.New("admin accounts cannot be backup-schedule targets")
	ErrUserNotFound = errors.New("backup-schedule target user not found")
)

// UserRejectedError names the offending user id and wraps the reason
// (ErrAdminUser or ErrUserNotFound) so an adapter can both branch on the
// reason (errors.Is) and echo the id (the REST wire returns detail: <uid>).
type UserRejectedError struct {
	UserID string
	Reason error
}

func (e *UserRejectedError) Error() string {
	return fmt.Sprintf("%s: %s", e.Reason.Error(), e.UserID)
}

func (e *UserRejectedError) Unwrap() error { return e.Reason }

// UserFinder is the narrow slice of the user repository the leaf needs to
// enforce the admin-target rule. A nil UserFinder skips the check (mirrors the
// REST handler's Users == nil test seam).
type UserFinder interface {
	FindByID(ctx context.Context, id string) (*models.User, error)
}

// scheduleWriter is the repository surface the lifecycle needs. Create commits
// the row and both membership sets in one transaction; Get loads the existing
// row so Update can validate a patch against the stored (immutable) kind; and
// UpdateWithMemberships commits the field changes together with any membership
// replacement in one transaction, so a partial update can't leave the row's
// membership silently diverged from what was asked.
type scheduleWriter interface {
	CreateWithMemberships(ctx context.Context, s *models.BackupSchedule, destIDs, userIDs []string) error
	Get(ctx context.Context, id string) (*models.BackupSchedule, error)
	UpdateWithMemberships(ctx context.Context, s *models.BackupSchedule, destIDs, userIDs *[]string) error
}

// Deps are the leaf's collaborators, injected by the adapter.
type Deps struct {
	Schedules scheduleWriter
	Users     UserFinder
}

// CreateInput is the resolved, transport-neutral create request. UserIDs and
// DestinationIDs are already resolved to ids by the adapter.
type CreateInput struct {
	Kind                string
	UserIDs             []string
	IncludeSystemBackup bool
	CronExpr            string
	Enabled             *bool
	KeepDaily           *int
	KeepWeekly          *int
	KeepMonthly         *int
	DestinationIDs      []string
}

// Create validates and persists a backup schedule with its memberships. The
// order of refusals is deliberate and shared: an invalid kind, an admin or
// missing target user, and an invalid cron each abort with ZERO writes.
func Create(ctx context.Context, d Deps, in CreateInput) (*models.BackupSchedule, error) {
	kind := in.Kind
	if kind == "" {
		kind = models.BackupScheduleKindAccount
	}
	if kind != models.BackupScheduleKindAccount && kind != models.BackupScheduleKindSystem {
		return nil, ErrInvalidKind
	}

	userIDs := cleanIDs(in.UserIDs)
	includeSys := in.IncludeSystemBackup

	if kind == models.BackupScheduleKindSystem {
		// A system schedule has no per-user membership and the
		// include-system flag is meaningless (it already is the system
		// backup) — normalise both so a kind flip can't leave leftovers.
		userIDs = nil
		includeSys = false
	} else if d.Users != nil {
		// account_backup: every explicit target must exist and must not
		// be an admin (an admin account is panel-only, nothing to back up).
		for _, uid := range userIDs {
			u, err := d.Users.FindByID(ctx, uid)
			if err != nil || u == nil {
				return nil, &UserRejectedError{UserID: uid, Reason: ErrUserNotFound}
			}
			if u.IsAdmin {
				return nil, &UserRejectedError{UserID: uid, Reason: ErrAdminUser}
			}
		}
	}

	next, err := internalbackup.NextFire(in.CronExpr, time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCron, err)
	}

	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}

	s := &models.BackupSchedule{
		ID:                  ids.NewULID(),
		Kind:                kind,
		IncludeSystemBackup: includeSys,
		CronExpr:            strings.TrimSpace(in.CronExpr),
		Enabled:             enabled,
		KeepDaily:           in.KeepDaily,
		KeepWeekly:          in.KeepWeekly,
		KeepMonthly:         in.KeepMonthly,
		NextRunAt:           &next,
	}
	if err := d.Schedules.CreateWithMemberships(ctx, s, in.DestinationIDs, userIDs); err != nil {
		return nil, err
	}
	s.UserIDs = userIDs
	return s, nil
}

// UpdateInput is a resolved, transport-neutral partial update. Every scalar is
// a pointer: nil leaves that field unchanged, non-nil sets it. DestinationIDs
// and UserIDs are likewise nil = leave the membership untouched, non-nil =
// replace it (an empty account UserIDs fans out to every non-admin at tick
// time, exactly as in Create). Ids are already resolved by the adapter. Kind is
// deliberately absent: a schedule's kind is immutable, so the patch is always
// validated against the stored kind.
type UpdateInput struct {
	ID                  string
	CronExpr            *string
	Enabled             *bool
	IncludeSystemBackup *bool
	KeepDaily           *int
	KeepWeekly          *int
	KeepMonthly         *int
	DestinationIDs      *[]string
	UserIDs             *[]string
}

// Update applies a partial change to an existing schedule and persists the row
// and any membership replacement in one transaction. It enforces the SAME
// policy as Create against the stored, immutable kind: on an account schedule
// every explicit target user must exist and must not be an admin, and an
// invalid cron aborts — each with ZERO writes; a system schedule keeps no
// per-user membership and no include-system flag, so those patch fields are
// ignored for it. This is the guard the operator CLI's hand-rolled update path
// lacked: it wrote arbitrary --user ids straight through, so an admin account
// could be attached to a schedule the create path would have refused.
func Update(ctx context.Context, d Deps, in UpdateInput) (*models.BackupSchedule, error) {
	s, err := d.Schedules.Get(ctx, in.ID)
	if err != nil {
		return nil, err
	}

	// Resolve the user set to persist. Only account schedules carry per-user
	// membership; for those, validate every target before any write. A system
	// schedule ignores the patch (nil = leave untouched).
	var userIDs *[]string
	if in.UserIDs != nil && s.Kind == models.BackupScheduleKindAccount {
		ids := cleanIDs(*in.UserIDs)
		if d.Users != nil {
			for _, uid := range ids {
				u, err := d.Users.FindByID(ctx, uid)
				if err != nil || u == nil {
					return nil, &UserRejectedError{UserID: uid, Reason: ErrUserNotFound}
				}
				if u.IsAdmin {
					return nil, &UserRejectedError{UserID: uid, Reason: ErrAdminUser}
				}
			}
		}
		userIDs = &ids
	}

	if in.CronExpr != nil {
		next, err := internalbackup.NextFire(*in.CronExpr, time.Now().UTC())
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidCron, err)
		}
		s.CronExpr = strings.TrimSpace(*in.CronExpr)
		s.NextRunAt = &next
	}
	if in.Enabled != nil {
		s.Enabled = *in.Enabled
	}
	if in.IncludeSystemBackup != nil && s.Kind == models.BackupScheduleKindAccount {
		// meaningless on a system schedule — it already is the system backup.
		s.IncludeSystemBackup = *in.IncludeSystemBackup
	}
	if in.KeepDaily != nil {
		s.KeepDaily = in.KeepDaily
	}
	if in.KeepWeekly != nil {
		s.KeepWeekly = in.KeepWeekly
	}
	if in.KeepMonthly != nil {
		s.KeepMonthly = in.KeepMonthly
	}

	if err := d.Schedules.UpdateWithMemberships(ctx, s, in.DestinationIDs, userIDs); err != nil {
		return nil, err
	}
	if userIDs != nil {
		s.UserIDs = *userIDs
	}
	return s, nil
}

// cleanIDs drops empty ids, preserving order.
func cleanIDs(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" {
			continue
		}
		out = append(out, v)
	}
	return out
}
