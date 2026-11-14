package ftpops

import (
	"context"
	"errors"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/limits"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// TenantLimits computes a tenant's resource-limits plan from stored state with
// the given quota mount and isolated-account shares (limitsync.Tenant).
type TenantLimits interface {
	PlanFor(ctx context.Context, userID, quotaMount string, shares limits.IsolatedShares) (limits.LimitsPlan, error)
}

// Disk room failures of an isolated create, as *ValidationError reasons.
var (
	// ErrNoDiskRoom: the tenant's own files already use more than its disk
	// quota would leave it once the new account's quota is set aside.
	ErrNoDiskRoom = errors.New("ftpops: not enough free disk quota")
	// ErrDiskRoomUnknown: the tenant's disk usage or limits could not be read,
	// so the new account's quota could not be set aside.
	ErrDiskRoomUnknown = errors.New("ftpops: disk usage unknown")
)

const diskUnknownDetail = "your disk usage could not be checked; try again"

// tenantDiskOff reports whether plan sets no disk limit on the tenant's own
// uid: no package or override (Clear), no disk quota, or quotas off on this
// host. Then an isolated account's quota takes nothing from the tenant.
func tenantDiskOff(plan limits.LimitsPlan) bool {
	return plan.Kind != limits.PlanApply || plan.QuotaMount == "" || plan.Effective.DiskQuotaMB == 0
}

// tenantDisk reads the tenant's own quota on the host.
func tenantDisk(ctx context.Context, d Deps, tenantUsername string) (limits.HostDisk, bool) {
	if d.Agent == nil {
		return limits.HostDisk{}, false
	}
	callCtx, cancel := context.WithTimeout(ctx, AgentTimeout)
	defer cancel()
	disks, err := limits.ReadHostDisks(callCtx, d.Agent, d.QuotaMount)
	return limits.HostDiskOf(disks, err, tenantUsername)
}

// setAsideDiskRoom lowers the tenant's own disk limit by the quotas of its
// isolated accounts, the one just reserved included, before that account
// exists on the host — so the tenant and its isolated accounts together never
// hold more than the tenant's quota.
//
// A tenant whose files already use more than its new limit is refused
// (ErrNoDiskRoom), and so is one whose usage or limits can't be read
// (ErrDiskRoomUnknown).
//
// The returned host reading is the tenant's quota before the change, for
// restoreTenantDisk once the reserved account is gone; nil when the limit was
// not touched. On an error it is non-nil exactly when the limit may have
// changed, so the caller releases the reservation and then restores.
func setAsideDiskRoom(ctx context.Context, d Deps, owner Owner) (*limits.HostDisk, error) {
	shares, err := d.Accounts.SumIsolatedQuotaByUserID(ctx, owner.UserID)
	if err != nil {
		return nil, invalid(ErrDiskRoomUnknown, diskUnknownDetail)
	}
	plan, err := d.Limits.PlanFor(ctx, owner.UserID, d.QuotaMount, limits.IsolatedShares{SharesMB: shares})
	if err != nil {
		return nil, invalid(ErrDiskRoomUnknown, diskUnknownDetail)
	}
	if tenantDiskOff(plan) {
		return nil, nil
	}
	// The reservation checked the package's quota; a limit override can set
	// the tenant's quota lower.
	if shares > uint64(plan.Effective.DiskQuotaMB) {
		return nil, repository.ErrFtpQuotaSplitExceeded
	}
	limitMB := plan.TenantDiskQuotaMB()
	pre, ok := tenantDisk(ctx, d, owner.Username)
	if !ok {
		return nil, invalid(ErrDiskRoomUnknown, diskUnknownDetail)
	}
	if pre.UsedKB > uint64(limitMB)*1024 {
		return nil, noRoom(pre.UsedKB, limitMB)
	}
	if err := agentExecute(ctx, d, plan, owner.Username); err != nil {
		return &pre, err
	}
	// Files written between the reading and the new limit could have taken
	// the room already; then put the limit back rather than leave the tenant
	// over it.
	post, ok := tenantDisk(ctx, d, owner.Username)
	if !ok {
		return &pre, invalid(ErrDiskRoomUnknown, diskUnknownDetail)
	}
	if post.UsedKB > uint64(limitMB)*1024 {
		return &pre, noRoom(post.UsedKB, limitMB)
	}
	return &pre, nil
}

func noRoom(usedKB uint64, limitMB uint32) error {
	return invalid(ErrNoDiskRoom, fmt.Sprintf(
		"your files use %d MB, more than the %d MB of your disk quota this account's quota would leave you; free some space or pick a smaller quota",
		(usedKB+1023)/1024, limitMB))
}

// restoreTenantDisk converges the tenant's own disk limit to the isolated
// accounts left in the database, from pre (its quota before a create changed
// it), after that create did not go through and its reservation is released.
// Best effort: on failure the limits reconciler converges it on its next pass.
func restoreTenantDisk(ctx context.Context, d Deps, owner Owner, pre limits.HostDisk) {
	ctx = context.WithoutCancel(ctx)
	shares, err := d.Accounts.SumIsolatedQuotaByUserID(ctx, owner.UserID)
	if err == nil {
		var plan limits.LimitsPlan
		if plan, err = d.Limits.PlanFor(ctx, owner.UserID, d.QuotaMount, limits.IsolatedShares{SharesMB: shares, Host: &pre}); err == nil {
			err = agentExecute(ctx, d, plan, owner.Username)
		}
	}
	if err != nil {
		logWarn(d, "ftp account: restore the tenant's disk limit failed; the limits reconciler converges it", "tenant", owner.Username, "err", err)
	}
}

// convergeTenantDisk gives a tenant back the room an isolated account it no
// longer has held. Best effort, like restoreTenantDisk.
func convergeTenantDisk(ctx context.Context, d Deps, owner Owner) {
	if d.Limits == nil || d.QuotaMount == "" {
		return
	}
	ctx = context.WithoutCancel(ctx)
	err := func() error {
		shares, err := d.Accounts.SumIsolatedQuotaByUserID(ctx, owner.UserID)
		if err != nil {
			return err
		}
		plan, err := d.Limits.PlanFor(ctx, owner.UserID, d.QuotaMount, limits.IsolatedShares{SharesMB: shares})
		if err != nil {
			return err
		}
		if tenantDiskOff(plan) {
			return nil
		}
		if shares > 0 {
			// A tenant over its share keeps what it uses (TenantDiskQuotaMB),
			// which needs the host's reading.
			cur, ok := tenantDisk(ctx, d, owner.Username)
			if !ok {
				return errors.New("no host quota reading")
			}
			plan.Shares.Host = &cur
		}
		return agentExecute(ctx, d, plan, owner.Username)
	}()
	if err != nil {
		logWarn(d, "ftp account: converge the tenant's disk limit failed; the limits reconciler converges it", "tenant", owner.Username, "err", err)
	}
}

func agentExecute(ctx context.Context, d Deps, plan limits.LimitsPlan, username string) error {
	if d.Agent == nil {
		return errors.New("agent unavailable")
	}
	callCtx, cancel := context.WithTimeout(ctx, AgentTimeout)
	defer cancel()
	return plan.Execute(callCtx, d.Agent, username)
}

func logWarn(d Deps, msg string, args ...any) {
	if d.Log != nil {
		d.Log.Warn(msg, args...)
	}
}
