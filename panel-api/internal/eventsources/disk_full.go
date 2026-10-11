package eventsources

import (
	"context"
	"fmt"
	"syscall"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/fsusage"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
)

const (
	diskFullTick     = 10 * time.Minute
	diskFullCoolOff  = 30 * time.Minute
	// 80% / 95% match the seed metadata users see in the admin Events
	// tab; an earlier pass had 85% here which surfaced as silent drift
	// when the toggle copy promised 80%.
	diskWarnPercent  = 80.0
	diskCritPercent  = 95.0
)

// diskFullMounts lists the filesystems we care about. Extra mounts (an
// operator-attached /mnt/backup) can be added here without touching the
// notification pipeline.
var diskFullMounts = []string{"/", "/var/www", "/var/lib/mysql"}

// runDiskFull polls the filesystem usage every 10 minutes and fires a
// warn-or-crit envelope when any mount crosses the thresholds. Both
// tiers are independently deduped: once a mount has fired "warn" it
// won't fire again for 30 minutes — enough breathing room for the
// operator to act, short enough that a transient dip-and-recover path
// doesn't silence real sustained problems.
func runDiskFull(ctx context.Context, d Deps) {
	// Fire a pass immediately on start so an operator booting into a
	// full disk isn't blind for 10 minutes.
	diskFullPass(ctx, d)
	tick := time.NewTicker(diskFullTick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		diskFullPass(ctx, d)
	}
}

func diskFullPass(ctx context.Context, d Deps) {
	for _, mount := range diskFullMounts {
		pct, ok := diskUsedPercent(mount)
		if !ok {
			// Missing mount (dev box without /var/lib/mysql, for
			// instance) is not an error — skip quietly.
			continue
		}
		switch {
		case pct >= diskCritPercent:
			// Critical matches the seed kind metadata; "error" was a
			// pre-Step-4 carryover that broke the severity colour in
			// the admin UI.
			fireDiskEvent(ctx, d, mount, pct, "disk.full.crit", models.NotificationSeverityCritical)
		case pct >= diskWarnPercent:
			fireDiskEvent(ctx, d, mount, pct, "disk.full.warn", models.NotificationSeverityWarning)
		}
	}
}

func fireDiskEvent(ctx context.Context, d Deps, mount string, pct int, kind, severity string) {
	tag := "mount:" + mount
	if !shouldFire(ctx, d, kind, tag, diskFullCoolOff) {
		return
	}
	_, err := d.Queue.Publish(ctx, notifications.Envelope{
		EventKind: kind,
		Severity:  severity,
		Title:     fmt.Sprintf("%s at %d%% full", mount, pct),
		Body:      fmt.Sprintf("Filesystem %s is %d%% full. (%s)", mount, pct, tag),
		Deeplink:  "/admin/system",
	})
	if err != nil {
		d.Log.Warn("eventsources: publish disk event failed", "mount", mount, "err", err)
	}
}

// statfs is syscall.Statfs; tests replace it.
var statfs = syscall.Statfs

// diskUsedPercent is how full mount is, as df's Use% (GH #2029): used as a
// share of the space a non-root process can use, so the blocks ext4 reserves
// for root count as neither used nor free, and the disk is 100% full when
// nothing is left to non-root. ok is false when the mount can't be read
// (it doesn't exist) or has no size.
func diskUsedPercent(mount string) (pct int, ok bool) {
	var st syscall.Statfs_t
	if err := statfs(mount, &st); err != nil {
		return 0, false
	}
	total, used, avail := fsusage.FromStatfs(&st)
	if total == 0 {
		return 0, false
	}
	return fsusage.UsedPercent(used, avail), true
}
