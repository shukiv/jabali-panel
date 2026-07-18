// CLI-only operator runbooks — the single source of truth the admin Support
// page renders. Mirrors docs/operations/cli-only-commands.md; a drift test
// (cli-only-runbooks.test.ts) asserts every `command` below still appears in
// that doc so the GUI list can't silently diverge from the recorded intent.
//
// These are deliberately read-only reference entries, NOT action buttons. The
// doc's own GUI note is explicit: the admin UI should surface where the
// recovery levers live, not grow one-click buttons for destructive ops.

export type RunbookCategory = "escape-hatch" | "gui-candidate";

export type RunbookRisk =
  | "destructive"
  | "recovery"
  | "one-time"
  | "key-material"
  | "maintenance"
  | "low";

export interface CliRunbook {
  // The `jabali …` subcommand, verbatim as it appears in the doc (the drift
  // test matches on this substring).
  command: string;
  // Source file that implements it, for operators reading the code.
  file: string;
  category: RunbookCategory;
  risk: RunbookRisk;
  // Whether the command supports a `--dry-run` preview before it mutates.
  dryRun: boolean;
  // One-line purpose / why it stays CLI-only. Condensed from the doc; no new
  // claims beyond what the doc records.
  summary: string;
  // Associated ADR, when the doc cites one.
  adr?: string;
}

// Intentionally CLI-only: escape hatches, recovery, one-time migrations, and
// key-material ops that don't belong behind a click.
export const CLI_ONLY_ESCAPE_HATCHES: readonly CliRunbook[] = [
  {
    command: "admin backfill-usernames",
    file: "backfill_usernames_cmd.go",
    category: "escape-hatch",
    risk: "one-time",
    dryRun: false,
    summary: "One-time data backfill; idempotent but bulk-mutating.",
  },
  {
    command: "admin purge-orphan-identities",
    file: "purge_orphan_identities_cmd.go",
    category: "escape-hatch",
    risk: "destructive",
    dryRun: false,
    summary: "Destructive Kratos identity GC; run after a manual cleanup.",
  },
  {
    command: "admin rebuild-kratos",
    file: "kratos_rebuild_cmd.go",
    category: "escape-hatch",
    risk: "recovery",
    dryRun: false,
    summary: "Disaster recovery — reprovisions the identity store.",
  },
  {
    command: "admin relabel-identifiers",
    file: "relabel_identifiers_cmd.go",
    category: "escape-hatch",
    risk: "one-time",
    dryRun: false,
    summary: "One-time identifier migration.",
  },
  {
    command: "admin slice-cutover",
    file: "admin_slice_cutover.go",
    category: "escape-hatch",
    risk: "destructive",
    dryRun: true,
    summary:
      "FPM master to per-user-slice cutover; masks distro units. Run --dry-run first.",
  },
  {
    command: "sso rotate-key",
    file: "sso_rotate_key.go",
    category: "escape-hatch",
    risk: "key-material",
    dryRun: false,
    summary: "Rotates SSO signing key material + reloads; needs careful rollback.",
  },
  {
    command: "sso prune-tokens",
    file: "sso_rotate_key.go",
    category: "escape-hatch",
    risk: "maintenance",
    dryRun: false,
    summary: "Maintenance GC; also timer-backed (jabali-sso-reap).",
  },
  {
    command: "migrate pull-source",
    file: "migrate_pull_cmd.go",
    category: "escape-hatch",
    risk: "one-time",
    dryRun: false,
    summary: "Migration-wizard internal sub-step.",
  },
  {
    command: "migrate reap-secrets",
    file: "migrate_reap_cmd.go",
    category: "escape-hatch",
    risk: "maintenance",
    dryRun: false,
    summary: "Migration secret GC.",
  },
  {
    command: "domain prune-orphans",
    file: "domain_orphan_prune_cmd.go",
    category: "escape-hatch",
    risk: "destructive",
    dryRun: false,
    summary: "Destructive orphan-vhost cleanup.",
  },
  {
    command: "pdns backfill",
    file: "pdns_cmd.go",
    category: "escape-hatch",
    risk: "one-time",
    dryRun: false,
    summary: "One-time PowerDNS backfill.",
    adr: "ADR-0047",
  },
  {
    command: "appsec render-config",
    file: "appsec_cmd.go",
    category: "escape-hatch",
    risk: "one-time",
    dryRun: false,
    summary: "Install-time config render; not an operator action.",
  },
  {
    command: "ufw migrate-ip-bans",
    file: "ufw_cmd.go",
    category: "escape-hatch",
    risk: "one-time",
    dryRun: false,
    summary: "One-time M43 ban migration.",
    adr: "ADR-0089",
  },
  {
    command: "audit verify",
    file: "audit CLI",
    category: "escape-hatch",
    risk: "recovery",
    dryRun: false,
    summary:
      "Hash-chain integrity verification — a forensic operator action; a GUI 'chain valid' button is a weak assurance a compromised panel could forge.",
  },
  {
    command: "audit prune",
    file: "audit CLI",
    category: "escape-hatch",
    risk: "destructive",
    dryRun: false,
    summary: "Destructive audit-event retention pruning.",
  },
  {
    command: "nspawn build",
    file: "nspawn CLI",
    category: "escape-hatch",
    risk: "maintenance",
    dryRun: false,
    summary: "Sealed nspawn image build (minutes-long, disk-heavy).",
  },
  {
    command: "nspawn prune",
    file: "nspawn CLI",
    category: "escape-hatch",
    risk: "destructive",
    dryRun: false,
    summary: "Destructive nspawn image prune.",
  },
];

// GUI candidates: backend verb already ships, only a button is missing. Listed
// here so operators can find the CLI today; a future GUI action may replace the
// entry (starting with domain email-dkim-rotate per the doc's recommendation).
export const CLI_ONLY_GUI_CANDIDATES: readonly CliRunbook[] = [
  {
    command: "domain email-dkim-rotate",
    file: "domain.email_dkim_rotate",
    category: "gui-candidate",
    risk: "low",
    dryRun: false,
    summary:
      "Best candidate — DKIM rotation is a normal per-domain mail op; verb + reconciler already converge the new key.",
  },
  {
    command: "apparmor flip-mature",
    file: "security.apparmor.set_mode",
    category: "gui-candidate",
    risk: "low",
    dryRun: true,
    summary:
      "Graduate complain to enforce. Risk: premature enforce can break a profile.",
  },
  {
    command: "per-user-egress flip-mature",
    file: "user.egress.apply",
    category: "gui-candidate",
    risk: "low",
    dryRun: true,
    summary: "Graduate the egress firewall from log to enforce.",
  },
  {
    command: "malware-purge",
    file: "security.malware.quarantine.delete",
    category: "gui-candidate",
    risk: "low",
    dryRun: false,
    summary:
      "Per-item quarantine delete already exists in the UI; only a bulk 'purge all' is missing.",
  },
];

export const ALL_CLI_ONLY_RUNBOOKS: readonly CliRunbook[] = [
  ...CLI_ONLY_ESCAPE_HATCHES,
  ...CLI_ONLY_GUI_CANDIDATES,
];

// Where the full doc lives, for a "read the source" deep link. Served from the
// docs site; the repo path is docs/operations/cli-only-commands.md.
export const CLI_ONLY_DOC_URL =
  "https://jabali-panel.com/docs/operations/cli-only-commands/";

export const RISK_LABELS: Record<RunbookRisk, string> = {
  destructive: "Destructive",
  recovery: "Recovery",
  "one-time": "One-time",
  "key-material": "Key material",
  maintenance: "Maintenance",
  low: "Low risk",
};

// AntD Tag colors per risk tier.
export const RISK_COLORS: Record<RunbookRisk, string> = {
  destructive: "red",
  recovery: "volcano",
  "one-time": "gold",
  "key-material": "magenta",
  maintenance: "blue",
  low: "green",
};
