package eventsources

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
)

const (
	serviceDownTick    = time.Minute
	serviceDownCoolOff = 5 * time.Minute
	// serviceDownGrace covers controlled restarts. `jabali update` and
	// install.sh both run `systemctl restart` against managed units;
	// the deactivating → inactive → activating window can span two
	// ticks. Without this debounce, every `jabali update` ships a
	// false-positive service.down alert per restarted unit (caught on
	// 192.168.100.150 dogfood 2026-05-04: jabali-stalwart fired during
	// the install-time bounce and stuck in the bell).
	serviceDownGrace = 2 * time.Minute
)

// serviceDownInactiveSince tracks the first tick a unit was observed
// in a non-active state. We only fire when the unit has been down for
// >= serviceDownGrace, debouncing controlled restarts.
var serviceDownInactiveSince = map[string]time.Time{}

// jabaliUnits is the hard-coded list of systemd units we monitor. It
// matches what install.sh enables — add new units here when
// install.sh adds them.
var jabaliUnits = []string{
	"jabali-panel.service",
	"jabali-agent.service",
	"jabali-kratos.service",
	"jabali-webmail.service",
	"jabali-stalwart.service",
	"pdns.service",
	"pdns-recursor.service",
}

// runServiceDown polls systemctl is-active for each configured
// jabali-* unit every minute. A non-active result fires service.down.
// Dedupe is per-unit: the event only re-fires every 5 minutes so a
// genuinely broken service doesn't spam the queue, but a flap on one
// unit doesn't silence a separate failure on another.
//
// We exec systemctl rather than talking D-Bus directly to stay aligned
// with ADR-0050 (panel-api has no privileged daemon bus access).
func runServiceDown(ctx context.Context, d Deps) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		d.Log.Info("eventsources: service_down disabled (systemctl not found)")
		return
	}
	serviceDownPass(ctx, d)
	tick := time.NewTicker(serviceDownTick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		serviceDownPass(ctx, d)
	}
}

// serviceStateNotDown reports whether a `systemctl is-active` result should
// NOT be treated as an outage this tick: healthy (active/activating), a
// transient in-between (deactivating/reloading), or — critically — EMPTY,
// which means the check itself failed (systemctl timeout / momentary
// unavailability) rather than the unit being down. GH: 57 false service.down
// alarms fired during an AppArmor enforce-flip when systemctl briefly returned
// empty for every unit.
func serviceStateNotDown(state string) bool {
	switch state {
	case "", "active", "activating", "deactivating", "reloading":
		return true
	}
	return false
}

func serviceDownPass(ctx context.Context, d Deps) {
	for _, unit := range jabaliUnits {
		state, err := unitState(ctx, unit)
		if err != nil {
			// systemctl returns non-zero for inactive/failed — that's
			// the signal we want, not an actual error. Only warn for
			// true execution failures (binary missing, etc.).
			d.Log.Debug("eventsources: service_down lookup failed", "unit", unit, "err", err)
		}
		if serviceStateNotDown(state) {
			// active/activating -> healthy: clear any down-since marker.
			// empty/deactivating/reloading -> can't conclude "down" this
			// tick; skip. Empty in particular means the check itself failed
			// (systemctl timeout / momentary unavailability during a
			// controlled restart or an AppArmor profile flip) — firing here
			// spams a false service.down with a blank state ("<unit> is ").
			if state == "active" || state == "activating" {
				delete(serviceDownInactiveSince, unit)
			}
			continue
		}
		// `failed` always fires — distinct from operator-disabled, this
		// means the unit crashed or its ExecStart returned non-zero.
		if state != "failed" {
			// For everything else (inactive, deactivating, not-found),
			// only fire when the unit is operator-ENABLED. Units that
			// are disabled / static / masked / indirect are deliberately
			// off — install.sh ships several such units (jabali-webmail,
			// jabali-stalwart) that lazy-start on first user action,
			// and treating them as failures spams the inbox on every
			// fresh install. service.down is for "should be running but
			// isn't", not "is configured to start later".
			enabled, _ := unitEnabled(ctx, unit)
			if enabledStateSkips(enabled) {
				continue
			}
		}
		// `failed` is genuinely terminal — fire immediately. For
		// `inactive` (and the catch-all "not-found"), require the
		// unit to have been in this state for at least serviceDownGrace
		// before firing. Suppresses false positives from controlled
		// restarts (jabali update, install.sh upgrade flow).
		if state == "failed" {
			delete(serviceDownInactiveSince, unit)
			fireServiceDown(ctx, d, unit, state)
			continue
		}
		now := d.Now()
		if firstSeen, seen := serviceDownInactiveSince[unit]; seen {
			if now.Sub(firstSeen) < serviceDownGrace {
				continue
			}
		} else {
			serviceDownInactiveSince[unit] = now
			continue
		}
		fireServiceDown(ctx, d, unit, state)
	}
}

// enabledStateSkips reports whether a `systemctl is-enabled <unit>` result means
// an inactive unit should NOT fire service.down. Beyond the operator-off states
// (disabled/static/masked/...), it covers "not-found" and empty: a unit that
// isn't installed at all. GH #726 -- a Custom install without mail/DNS/PostgreSQL
// leaves those units absent (`is-enabled` -> "not-found", `is-active` ->
// "inactive"), and the monitor flooded the inbox with service.down for modules
// the operator never selected. service.down is for "an ENABLED unit that should
// be running is down", not "this module was never installed".
func enabledStateSkips(enabled string) bool {
	switch enabled {
	case "", "not-found", "disabled", "static", "masked", "indirect", "alias", "linked-runtime", "transient":
		return true
	}
	return false
}

// unitEnabled returns the `systemctl is-enabled <unit>` string. Empty
// string + error when the unit is unknown to systemd.
func unitEnabled(ctx context.Context, unit string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "is-enabled", unit).Output()
	return strings.TrimSpace(string(out)), err
}

func fireServiceDown(ctx context.Context, d Deps, unit, state string) {
	tag := "unit:" + unit
	if !shouldFire(ctx, d, "service.down", tag, serviceDownCoolOff) {
		return
	}
	_, err := d.Queue.Publish(ctx, notifications.Envelope{
		EventKind: "service.down",
		Severity:  models.NotificationSeverityError,
		Title:     fmt.Sprintf("%s is %s", unit, state),
		Body:      fmt.Sprintf("systemd reports %s for %s. Run `journalctl -u %s` for details. (%s)", state, unit, unit, tag),
		Deeplink:  "/admin/system",
	})
	if err != nil {
		d.Log.Warn("eventsources: publish service_down failed", "unit", unit, "err", err)
	}
}

func unitState(ctx context.Context, unit string) (string, error) {
	// `systemctl is-active <unit>` prints the state + exit 0 (active)
	// or non-zero (everything else). We want the string either way.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "is-active", unit).Output()
	state := strings.TrimSpace(string(out))
	return state, err
}
