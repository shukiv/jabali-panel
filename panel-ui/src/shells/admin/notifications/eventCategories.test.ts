import { describe, it, expect } from "vitest";

import {
  EVENT_KIND_CATEGORY,
  EVENT_CATEGORY_ORDER,
  categoryForKind,
  groupEventsByCategory,
} from "./eventCategories";

// Snapshot of models.AllNotificationEventKinds
// (panel-api/internal/models/notification_event_setting.go). The Go-side test
// notification_event_categories_sync_test.go fails CI if this drifts from the
// catalog; this copy lets the pure mapping assertions run in the SPA test suite.
const CATALOG_KINDS = [
  "cert.renew.fail",
  "cert.renew.ok",
  "domain.expiry.7d",
  "domain.expiry.1d",
  "disk.full.warn",
  "disk.full.crit",
  "disk.quota.warn",
  "load.high",
  "system.update.available",
  "docker_app.update_available",
  "service.down",
  "crowdsec.ban.spike",
  "backup.fail",
  "backup.success",
  "backup.limit.reached",
  "admin.login",
  "ssh.login",
  "notifications.channel.auto_disabled",
  "panel.welcome",
  "security.root_terminal.opened",
  "security.decision.fired",
  "snuffleupagus.incident.detected",
  "postgres.service_down",
  "postgres.disk_high",
  "postgres.connections_exhausted",
  "agent.dispatch.failure",
  "reconciler.error",
  "agent.unreachable",
  "notifications.dlq.nonzero",
  "panel.api.error",
  "bandwidth.quota.warn",
  "bandwidth.quota.crit",
  "digest.daily",
  "update.completed",
  "aide.tamper.detected",
  "nginx.config.invalid",
  "malware.realtime.critical",
  "malware.quarantine.added",
  "egress.drop.burst",
  "exec.audit.burst",
  "domain.ghost_detected.mismatch",
  "domain.ghost_detected.nxdomain",
  "domain.ghost_detected.partial",
  "mail.rbl.listed",
  "mail.rbl.cleared",
  "mail.dmarc.report_received",
  "mail.tls.report_received",
  "mail.feedback.received",
  "docker_app.entitlement_stopped",
  "docker_app.disk_quota_stopped",
  "docker_app.removed_from_package",
  "db.admin.config_apply_failed_unrecoverable",
  "db.admin.config_applied",
  "db.admin.maintenance_finished",
  "db.admin.root_password_rotated",
  "automation.user.created",
  "automation.user.deleted",
  "automation.user.disabled",
  "automation.user.enabled",
  "automation.domain.created",
  "automation.domain.suspended",
  "automation.domain.unsuspended",
];

const CATEGORY_IDS = new Set(EVENT_CATEGORY_ORDER.map((c) => c.id));

describe("eventCategories mapping", () => {
  it("assigns every catalog kind to exactly one known category", () => {
    for (const kind of CATALOG_KINDS) {
      const id = EVENT_KIND_CATEGORY[kind];
      expect(id, `kind ${kind} is unmapped`).toBeDefined();
      expect(CATEGORY_IDS.has(id), `kind ${kind} → unknown category ${id}`).toBe(true);
      // a real catalog kind must never fall back to "other"
      expect(id).not.toBe("other");
    }
  });

  it("has no stale keys beyond the catalog", () => {
    for (const kind of Object.keys(EVENT_KIND_CATEGORY)) {
      expect(CATALOG_KINDS, `map has stale kind ${kind}`).toContain(kind);
    }
  });

  it("covers the whole catalog (map key count === catalog size)", () => {
    expect(Object.keys(EVENT_KIND_CATEGORY).length).toBe(CATALOG_KINDS.length);
  });

  it("keeps event families together", () => {
    const cat = (k: string) => EVENT_KIND_CATEGORY[k];
    // docker_app.* → apps
    for (const k of CATALOG_KINDS.filter((k) => k.startsWith("docker_app."))) {
      expect(cat(k)).toBe("apps");
    }
    // db.admin.* + postgres.* → services (families not split)
    for (const k of CATALOG_KINDS.filter((k) => k.startsWith("db.admin.") || k.startsWith("postgres."))) {
      expect(cat(k)).toBe("services");
    }
    // mail.* → mail; automation.user.* → users; automation.domain.* → domains
    for (const k of CATALOG_KINDS.filter((k) => k.startsWith("mail."))) expect(cat(k)).toBe("mail");
    for (const k of CATALOG_KINDS.filter((k) => k.startsWith("automation.user."))) expect(cat(k)).toBe("users");
    for (const k of CATALOG_KINDS.filter((k) => k.startsWith("automation.domain."))) expect(cat(k)).toBe("domains");
  });

  it("falls back to 'other' for an unmapped kind (future event stays visible)", () => {
    expect(categoryForKind("some.future.event")).toBe("other");
  });
});

describe("groupEventsByCategory", () => {
  const ev = (kind: string, enabled = false) => ({ kind, enabled });

  it("renders only non-empty categories, in taxonomy order", () => {
    const groups = groupEventsByCategory([
      ev("backup.fail"),
      ev("cert.renew.ok"),
      ev("mail.rbl.listed"),
    ]);
    // ssl before backups before mail (taxonomy order), no empty categories
    expect(groups.map((g) => g.id)).toEqual(["ssl", "backups", "mail"]);
  });

  it("places each event in exactly one group and preserves counts", () => {
    const events = CATALOG_KINDS.map((k, i) => ev(k, i % 2 === 0));
    const groups = groupEventsByCategory(events);
    const total = groups.reduce((n, g) => n + g.events.length, 0);
    expect(total).toBe(CATALOG_KINDS.length); // exactly-once, nothing dropped
    // no category duplicated
    expect(new Set(groups.map((g) => g.id)).size).toBe(groups.length);
  });

  it("routes an unmapped kind into a visible 'other' group", () => {
    const groups = groupEventsByCategory([ev("brand.new.kind")]);
    expect(groups).toHaveLength(1);
    expect(groups[0].id).toBe("other");
  });
});
