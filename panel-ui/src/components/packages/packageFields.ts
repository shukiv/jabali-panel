// packageFields — the canonical Hosting Package entitlement model (JAB-331).
//
// PackageCreate and PackageEdit used to carry two verbatim copies of the whole
// entitlement form (~340 lines each). A field added to one screen but not the
// other silently shipped the backend default 0 (JAB-328 was exactly that: the
// FTP limit was on Edit but missing from Create). This module is the single
// source of truth both screens now render through, so an entitlement cannot
// exist in one mode and not the other.
//
// The numeric-limit fields live in PACKAGE_LIMIT_FIELDS (data-driven render);
// disk_quota_mb stays special (its enabled/tooltip/extra depend on the global
// disk-quota toggle) and is allowlisted in SPECIAL_LIMIT_FIELDS. PACKAGE_DEFAULTS
// is the create-mode initial values. encode/decode own the CSV round-trip for
// the two multi-select fields (docker_app_slugs, allowed_backup_destination_kinds)
// which are CSV strings on the wire but arrays in the Form.

// Mirrors models.AllBackupDestinationKinds (GH #454). Keep in sync with the
// backend enum in backup_destination.go.
export const BACKUP_DESTINATION_KINDS = ["local", "sftp", "s3", "b2", "azure", "gcs", "rest"] as const;

export type PackageFormValues = {
  name: string;
  disk_quota_mb: number;
  // M18 — per-user resource limits. 0 means unlimited on every field.
  cpu_quota_percent: number;
  memory_limit_mb: number;
  io_read_mbps: number;
  io_write_mbps: number;
  max_tasks: number;
  bandwidth_quota_mb: number;
  max_domains: number;
  max_email_accounts: number;
  max_databases: number;
  max_database_users: number;
  max_docker_apps: number;
  max_python_apps: number;
  max_ftp_accounts: number;
  // Tenant backup limits (GH #454). allowed_backup_destination_kinds is a CSV
  // string on the wire; the multi-Select binds an array (converted on load/save,
  // like docker_app_slugs).
  max_backups: number;
  max_backup_schedules: number;
  scheduled_backups_enabled: boolean;
  allowed_backup_destination_kinds: string | string[];
  backup_retention_policy: string;
  ssh_enabled: boolean;
  cgi_enabled: boolean;
  php_exec_enabled: boolean;
  fpm_user_can_edit: boolean;
  fpm_advanced_mode: boolean;
  fpm_max_children_cap: number;
  fpm_worker_mem_mb: number;
  docker_app_slugs?: string[] | string;
  nspawn_image_version?: string | null;
};

export type PackageRecord = PackageFormValues & { id: string };

// Wire payload: the two multi-select fields are CSV strings, not arrays.
export type PackageWirePayload = Omit<
  PackageFormValues,
  "docker_app_slugs" | "allowed_backup_destination_kinds"
> & {
  docker_app_slugs: string;
  allowed_backup_destination_kinds: string;
};

export type LimitFieldGroup = "resource" | "quota" | "backup" | "fpm";

export type LimitFieldDef = {
  name: keyof PackageFormValues;
  labelKey: string; // suffix under the packageedit.* i18n namespace
  min: number;
  max?: number;
  width: string | number;
  required?: boolean;
  requiredMsg?: string;
  tooltipText?: string; // literal tooltip
  tooltipKey?: string; // suffix under packageedit.* (mutually exclusive with tooltipText)
  group: LimitFieldGroup;
};

// disk_quota_mb is rendered explicitly (its disabled state + tooltip + extra all
// depend on the disk-quota global toggle), so it is allowlisted here rather than
// data-driven. The parity check below treats it as covered.
export const SPECIAL_LIMIT_FIELDS = ["disk_quota_mb"] as const;

// Every numeric entitlement the two screens render, in visual order per group.
export const PACKAGE_LIMIT_FIELDS = [
  // Resource limits (cgroups v2). disk_quota_mb precedes these but is special.
  {
    name: "cpu_quota_percent",
    labelKey: "cpu_quota",
    min: 0,
    max: 10000,
    width: "100%",
    tooltipText: "systemd CPUQuota — 100% = 1 core, 200% = 2 cores. 0 = unlimited.",
    group: "resource",
  },
  {
    name: "memory_limit_mb",
    labelKey: "memory_limit_mb",
    min: 0,
    max: 1048576,
    width: "100%",
    tooltipText: "systemd MemoryMax; MemoryHigh is fixed at 90% of this. 0 = unlimited.",
    group: "resource",
  },
  {
    name: "io_read_mbps",
    labelKey: "io_read_bandwidth_mb_s",
    min: 0,
    max: 10000,
    width: "100%",
    tooltipText: "systemd IOReadBandwidthMax on /. 0 = unlimited.",
    group: "resource",
  },
  {
    name: "io_write_mbps",
    labelKey: "io_write_bandwidth_mb_s",
    min: 0,
    max: 10000,
    width: "100%",
    tooltipText: "systemd IOWriteBandwidthMax on /. 0 = unlimited.",
    group: "resource",
  },
  {
    name: "max_tasks",
    labelKey: "max_tasks",
    min: 0,
    max: 100000,
    width: "100%",
    tooltipText: "systemd TasksMax — upper bound on concurrent processes. 0 = unlimited.",
    group: "resource",
  },
  // Feature quotas. 0 = unlimited (or feature not included) on every field.
  {
    name: "bandwidth_quota_mb",
    labelKey: "bandwidth_quota_mb",
    min: 0,
    width: "100%",
    required: true,
    requiredMsg: "Bandwidth quota is required",
    tooltipText: "0 = unlimited",
    group: "quota",
  },
  {
    name: "max_domains",
    labelKey: "max_domains",
    min: 0,
    width: "100%",
    required: true,
    requiredMsg: "Max domains is required",
    tooltipText: "0 = unlimited",
    group: "quota",
  },
  {
    name: "max_email_accounts",
    labelKey: "max_email_accounts",
    min: 0,
    width: "100%",
    required: true,
    requiredMsg: "Max email accounts is required",
    tooltipText: "0 = unlimited",
    group: "quota",
  },
  {
    name: "max_databases",
    labelKey: "max_databases",
    min: 0,
    width: "100%",
    required: true,
    requiredMsg: "Max databases is required",
    tooltipText: "0 = unlimited",
    group: "quota",
  },
  {
    name: "max_database_users",
    labelKey: "max_database_users",
    min: 0,
    width: "100%",
    required: true,
    requiredMsg: "Max database users is required",
    tooltipText: "0 = unlimited",
    group: "quota",
  },
  {
    name: "max_docker_apps",
    labelKey: "max_docker_apps",
    min: 0,
    width: "100%",
    tooltipText: "0 = Docker apps not included in this package",
    group: "quota",
  },
  {
    name: "max_python_apps",
    labelKey: "max_python_apps",
    min: 0,
    width: "100%",
    tooltipText: "0 = Python apps not included in this package",
    group: "quota",
  },
  {
    // JAB-328: the FTP account limit must render everywhere the entitlement set
    // does — omitting it once submitted the backend default 0 and silently
    // disabled FTP on every panel-created package. 0 stays an explicit opt-out.
    name: "max_ftp_accounts",
    labelKey: "max_ftp_accounts",
    min: 0,
    width: "100%",
    tooltipText: "0 = FTP/SFTP accounts not included in this package",
    group: "quota",
  },
  // Backups (GH #454). The switch and the two Selects are rendered explicitly
  // between these numerics to preserve the row's order.
  {
    name: "max_backups",
    labelKey: "max_backups",
    min: 0,
    width: "100%",
    tooltipKey: "retention_cap_most_snapshots_a_tenant_on_thi",
    group: "backup",
  },
  {
    name: "max_backup_schedules",
    labelKey: "max_backup_schedules",
    min: 1,
    width: "100%",
    tooltipKey: "how_many_scheduled_backups_a_tenant_may_own",
    group: "backup",
  },
  // PHP-FPM performance caps (standalone, width 160).
  {
    name: "fpm_max_children_cap",
    labelKey: "max_children_per_user_fpm_cap",
    min: 1,
    max: 2000,
    width: 160,
    group: "fpm",
  },
  {
    name: "fpm_worker_mem_mb",
    labelKey: "est_ram_per_worker_mb_drives_the_memory_budg",
    min: 8,
    max: 2048,
    width: 160,
    group: "fpm",
  },
] as const satisfies readonly LimitFieldDef[];

// Create-mode initial values. Every numeric entitlement has a default here, so a
// field can never submit `undefined`; the parity test ties this to the rendered
// field set (a listed field with no default, or a numeric default with no field,
// fails the build/tests).
export const PACKAGE_DEFAULTS: PackageFormValues = {
  name: "",
  ssh_enabled: false,
  cgi_enabled: false,
  php_exec_enabled: false,
  fpm_user_can_edit: false,
  fpm_advanced_mode: false,
  fpm_max_children_cap: 20,
  fpm_worker_mem_mb: 64,
  disk_quota_mb: 0,
  cpu_quota_percent: 0,
  memory_limit_mb: 0,
  io_read_mbps: 0,
  io_write_mbps: 0,
  max_tasks: 0,
  bandwidth_quota_mb: 0,
  max_domains: 0,
  max_email_accounts: 0,
  max_databases: 0,
  max_database_users: 0,
  max_docker_apps: 0,
  max_python_apps: 0,
  max_ftp_accounts: 0,
  max_backups: 0,
  max_backup_schedules: 1,
  scheduled_backups_enabled: false,
  allowed_backup_destination_kinds: [],
  backup_retention_policy: "reject",
};

// --- CSV codecs (AC2). docker_app_slugs and allowed_backup_destination_kinds are
// CSV strings on the wire but arrays in the Form. ---

export function encodePackagePayload(values: PackageFormValues): PackageWirePayload {
  return {
    ...values,
    docker_app_slugs: Array.isArray(values.docker_app_slugs)
      ? values.docker_app_slugs.join(",")
      : (values.docker_app_slugs ?? ""),
    allowed_backup_destination_kinds: Array.isArray(values.allowed_backup_destination_kinds)
      ? values.allowed_backup_destination_kinds.join(",")
      : (values.allowed_backup_destination_kinds ?? ""),
  };
}

export function decodePackageForm(record: PackageRecord): PackageFormValues {
  const { id: _id, ...rest } = record;
  void _id;
  const csv = typeof rest.docker_app_slugs === "string" ? rest.docker_app_slugs : "";
  const bkCsv =
    typeof rest.allowed_backup_destination_kinds === "string"
      ? rest.allowed_backup_destination_kinds
      : "";
  return {
    ...rest,
    docker_app_slugs: csv ? csv.split(",").filter(Boolean) : [],
    allowed_backup_destination_kinds: bkCsv ? bkCsv.split(",").filter(Boolean) : [],
  };
}

// --- Compile-time parity (AC4). Every numeric field on PackageFormValues must be
// rendered exactly once — via PACKAGE_LIMIT_FIELDS or SPECIAL_LIMIT_FIELDS. Adding
// a numeric entitlement to the type without listing it (or listing a name that is
// not a numeric field) turns PACKAGE_FIELD_PARITY's type into an object and the
// `= true` assignment below fails `tsc -b` (the CI build), naming the drifted
// field. This is the JAB-328 guard, now at the model boundary. ---

type NumericLimitKey = {
  [K in keyof PackageFormValues]-?: PackageFormValues[K] extends number ? K : never;
}[keyof PackageFormValues];

type CoveredLimitKey =
  | (typeof PACKAGE_LIMIT_FIELDS)[number]["name"]
  | (typeof SPECIAL_LIMIT_FIELDS)[number];

type UncoveredNumeric = Exclude<NumericLimitKey, CoveredLimitKey>;
type PhantomCovered = Exclude<CoveredLimitKey, NumericLimitKey>;

type ParityResult = [UncoveredNumeric] extends [never]
  ? [PhantomCovered] extends [never]
    ? true
    : { error: "covered field is not a numeric entitlement"; field: PhantomCovered }
  : { error: "numeric entitlement not rendered"; field: UncoveredNumeric };

export const PACKAGE_FIELD_PARITY: ParityResult = true;
