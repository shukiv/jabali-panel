package models

import "testing"

// validEventSeverities mirrors the documented Severity domain on
// NotificationEventKindMeta.
var validEventSeverities = map[string]bool{
	"info": true, "warning": true, "error": true, "critical": true,
}

// TestAllNotificationEventKinds_WellFormed guards the registry: unique kinds,
// every kind carries a label + description, and a valid severity. A malformed
// entry would render a broken row in the admin Notifications → Events tab.
func TestAllNotificationEventKinds_WellFormed(t *testing.T) {
	seen := make(map[string]bool, len(AllNotificationEventKinds))
	for _, m := range AllNotificationEventKinds {
		if m.Kind == "" {
			t.Errorf("event kind with empty Kind: %+v", m)
			continue
		}
		if seen[m.Kind] {
			t.Errorf("duplicate event kind %q", m.Kind)
		}
		seen[m.Kind] = true
		if m.Label == "" {
			t.Errorf("%q: empty Label", m.Kind)
		}
		if m.Description == "" {
			t.Errorf("%q: empty Description", m.Kind)
		}
		if !validEventSeverities[m.Severity] {
			t.Errorf("%q: invalid Severity %q", m.Kind, m.Severity)
		}
		if LookupNotificationEventKind(m.Kind) == nil {
			t.Errorf("%q: not found via LookupNotificationEventKind", m.Kind)
		}
	}
}

// TestNotificationEventKinds_EmittedKindsAreRegistered pins GH #979: the kinds
// publishers actually fire MUST appear in AllNotificationEventKinds, otherwise
// they never surface in the Events tab and can't be silenced (an unlisted kind
// falls back to always-on). `emitted` is the hand-maintained inventory of kinds
// fired across the codebase (non-test), gathered by auditing every EventKind
// literal, shouldFire() arg, and notify/publish helper. NOTE: this catches
// REMOVING a listed kind from the registry, not a brand-new unregistered
// emitter — keep this list in sync when you wire a new publisher.
//
// Deliberately EXCLUDED (must stay always-on, never user-toggleable):
//   - notifications.broadcast   — admin broadcast, must always deliver
//   - notifications.channel.test — fires only on an explicit "send test" click
func TestNotificationEventKinds_EmittedKindsAreRegistered(t *testing.T) {
	emitted := []string{
		// pre-existing (sanity — these were already registered)
		"admin.login", "ssh.login", "backup.fail", "backup.limit.reached",
		"cert.renew.fail", "cert.renew.ok", "crowdsec.ban.spike", "digest.daily",
		"disk.quota.warn", "docker_app.update_available", "load.high",
		"security.decision.fired", "security.root_terminal.opened", "service.down",
		"snuffleupagus.incident.detected", "system.update.available",
		"notifications.channel.auto_disabled",
		// GH #979 additions
		"update.completed", "aide.tamper.detected", "nginx.config.invalid",
		"malware.realtime.critical", "malware.quarantine.added",
		"egress.drop.burst", "exec.audit.burst",
		"domain.ghost_detected.mismatch", "domain.ghost_detected.nxdomain",
		"domain.ghost_detected.partial",
		"mail.rbl.listed", "mail.rbl.cleared", "mail.dmarc.report_received",
		"mail.tls.report_received", "mail.feedback.received",
		"docker_app.entitlement_stopped", "docker_app.disk_quota_stopped",
		"docker_app.removed_from_package",
		"db.admin.config_apply_failed_unrecoverable", "db.admin.config_applied",
		"db.admin.maintenance_finished", "db.admin.root_password_rotated",
		// automation API writes (notifyWrite)
		"automation.user.created", "automation.user.deleted",
		"automation.user.disabled", "automation.user.enabled",
		"automation.domain.created", "automation.domain.suspended",
		"automation.domain.unsuspended",
	}
	for _, kind := range emitted {
		if LookupNotificationEventKind(kind) == nil {
			t.Errorf("emitted kind %q is not registered in AllNotificationEventKinds — "+
				"it will never appear in Notifications → Events and cannot be disabled (GH #979)", kind)
		}
	}
}
