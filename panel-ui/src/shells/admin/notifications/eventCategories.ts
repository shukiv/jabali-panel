// eventCategories.ts — JAB-381. Central, stable mapping of a notification event
// KIND to exactly one display category, for grouping the admin
// Notifications > Events list into collapsible sections.
//
// Assignment decision rule (apply this when a new kind is added):
//   • Families stay together — every docker_app.*, db.admin.*, postgres.*,
//     mail.*, automation.user.*, automation.domain.* lands in ONE category, so
//     an operator hunting "postgres alerts" looks in a single place.
//   • Ties break toward the SUBJECT of the event, not the verb — a docker app
//     "update_available" is filed under Applications & Docker (its subject),
//     not Updates (its verb).
//
// The canonical kind list is the Go catalog models.AllNotificationEventKinds
// (panel-api/internal/models/notification_event_setting.go). This map is kept in
// sync by a cross-boundary test; a kind not present here falls back to "other"
// so it stays VISIBLE in the UI rather than silently disappearing.

export type EventCategoryId =
  | "ssl"
  | "domains"
  | "disk"
  | "system"
  | "services"
  | "backups"
  | "mail"
  | "security"
  | "users"
  | "apps"
  | "updates"
  | "other";

export interface EventCategoryMeta {
  id: EventCategoryId;
  /** i18n key under eventstab.category.* */
  labelKey: string;
}

// Display order follows the ticket taxonomy; "other" (the unmapped fallback) is
// always last and only rendered when non-empty.
export const EVENT_CATEGORY_ORDER: EventCategoryMeta[] = [
  { id: "ssl", labelKey: "eventstab.category.ssl" },
  { id: "domains", labelKey: "eventstab.category.domains" },
  { id: "disk", labelKey: "eventstab.category.disk" },
  { id: "system", labelKey: "eventstab.category.system" },
  { id: "services", labelKey: "eventstab.category.services" },
  { id: "backups", labelKey: "eventstab.category.backups" },
  { id: "mail", labelKey: "eventstab.category.mail" },
  { id: "security", labelKey: "eventstab.category.security" },
  { id: "users", labelKey: "eventstab.category.users" },
  { id: "apps", labelKey: "eventstab.category.apps" },
  { id: "updates", labelKey: "eventstab.category.updates" },
  { id: "other", labelKey: "eventstab.category.other" },
];

export const FALLBACK_CATEGORY: EventCategoryId = "other";

// Explicit kind → category. MUST stay in sync with models.AllNotificationEventKinds
// (enforced by notification_event_categories_sync_test.go on the Go side).
export const EVENT_KIND_CATEGORY: Record<string, EventCategoryId> = {
  // SSL & Certificates
  "cert.renew.fail": "ssl",
  "cert.renew.ok": "ssl",

  // Domains & DNS (expiry, ghost-detection, automation on domains)
  "domain.expiry.7d": "domains",
  "domain.expiry.1d": "domains",
  "domain.ghost_detected.mismatch": "domains",
  "domain.ghost_detected.nxdomain": "domains",
  "domain.ghost_detected.partial": "domains",
  "automation.domain.created": "domains",
  "automation.domain.suspended": "domains",
  "automation.domain.unsuspended": "domains",

  // Disk & Quotas (disk fill, disk quota, bandwidth quota)
  "disk.full.warn": "disk",
  "disk.full.crit": "disk",
  "disk.quota.warn": "disk",
  "bandwidth.quota.warn": "disk",
  "bandwidth.quota.crit": "disk",

  // Server & System (host load, panel/notification infra, config integrity, digest)
  "load.high": "system",
  "panel.welcome": "system",
  "panel.api.error": "system",
  "notifications.channel.auto_disabled": "system",
  "notifications.dlq.nonzero": "system",
  "digest.daily": "system",
  "nginx.config.invalid": "system",

  // Services (service liveness, agent/reconciler pipeline, database engines)
  "service.down": "services",
  "agent.dispatch.failure": "services",
  "agent.unreachable": "services",
  "reconciler.error": "services",
  "postgres.service_down": "services",
  "postgres.disk_high": "services",
  "postgres.connections_exhausted": "services",
  "db.admin.config_apply_failed_unrecoverable": "services",
  "db.admin.config_applied": "services",
  "db.admin.maintenance_finished": "services",
  "db.admin.root_password_rotated": "services",

  // Backups
  "backup.fail": "backups",
  "backup.success": "backups",
  "backup.limit.reached": "backups",
  "dr.sync.stalled": "backups",

  // Mail
  "mail.rbl.listed": "mail",
  "mail.rbl.cleared": "mail",
  "mail.dmarc.report_received": "mail",
  "mail.tls.report_received": "mail",
  "mail.feedback.received": "mail",

  // Security (intrusion/abuse, privileged access, integrity, malware, egress/exec)
  "crowdsec.ban.spike": "security",
  "security.root_terminal.opened": "security",
  "security.decision.fired": "security",
  "snuffleupagus.incident.detected": "security",
  "aide.tamper.detected": "security",
  "malware.realtime.critical": "security",
  "malware.quarantine.added": "security",
  "egress.drop.burst": "security",
  "exec.audit.burst": "security",
  "admin.login": "security",
  "ssh.login": "security",

  // Users & Accounts
  "automation.user.created": "users",
  "automation.user.deleted": "users",
  "automation.user.disabled": "users",
  "automation.user.enabled": "users",

  // Applications & Docker
  "docker_app.update_available": "apps",
  "docker_app.entitlement_stopped": "apps",
  "docker_app.disk_quota_stopped": "apps",
  "docker_app.removed_from_package": "apps",

  // Updates (host / panel updates)
  "system.update.available": "updates",
  "update.completed": "updates",
};

export function categoryForKind(kind: string): EventCategoryId {
  return EVENT_KIND_CATEGORY[kind] ?? FALLBACK_CATEGORY;
}

export interface CategorizedGroup<T> {
  id: EventCategoryId;
  labelKey: string;
  events: T[];
}

// groupEventsByCategory buckets events into the ordered category list, keeping
// each category's events in the order they arrived (the catalog is pre-ordered)
// and dropping categories with no events. An unmapped kind lands in "other".
export function groupEventsByCategory<T extends { kind: string }>(
  events: T[],
): CategorizedGroup<T>[] {
  const byId = new Map<EventCategoryId, T[]>();
  for (const e of events) {
    const id = categoryForKind(e.kind);
    const bucket = byId.get(id);
    if (bucket) {
      bucket.push(e);
    } else {
      byId.set(id, [e]);
    }
  }
  return EVENT_CATEGORY_ORDER.filter((c) => (byId.get(c.id)?.length ?? 0) > 0).map(
    (c) => ({ id: c.id, labelKey: c.labelKey, events: byId.get(c.id) as T[] }),
  );
}
