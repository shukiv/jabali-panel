// Per-package PHP settings policy (GH #1701). A hosting package says, per
// php.ini directive, who may set it on a domain's PHP Settings page: only an
// admin, or the tenant too. The backend is the authority (models.
// PHPSettingCatalog + NormalizePHPSettingsPolicy); this file is the package
// editor's view of it.
//
// The directive list mirrors the Go catalog in order; the Go test
// TestPHPSettingCatalogTSInSync fails CI when the two drift.

export const PHP_SETTING_DIRECTIVES = [
  "memory_limit",
  "upload_max_filesize",
  "post_max_size",
  "max_input_vars",
  "max_execution_time",
  "max_input_time",
  "display_errors",
  "error_reporting",
  "date.timezone",
  "log_errors",
  "file_uploads",
  "short_open_tag",
] as const;

export type PHPSettingDirective = (typeof PHP_SETTING_DIRECTIVES)[number];

// tenant_privileged is the opt-in level for the security-sensitive directives
// (open_basedir, disable_functions, …) a later slice adds; the catalog
// directives above take admin_only or tenant_allowed.
export type PHPSettingLevel =
  "admin_only" | "tenant_allowed" | "tenant_privileged";

// Every catalog directive defaults to tenant_allowed: tenants could already set
// each one before policies existed, and a policy only ever restricts that.
export const PHP_SETTING_DEFAULT_LEVEL: PHPSettingLevel = "tenant_allowed";

// Form shape: directive -> level. Keys outside the catalog (a sensitive
// directive set through the CLI) are kept so a save from the editor does not
// drop them.
export type PHPSettingsPolicyForm = Record<string, PHPSettingLevel>;

export function defaultPHPSettingsPolicy(): PHPSettingsPolicyForm {
  const out: PHPSettingsPolicyForm = {};
  for (const d of PHP_SETTING_DIRECTIVES) out[d] = PHP_SETTING_DEFAULT_LEVEL;
  return out;
}

// decodePHPSettingsPolicy turns the stored JSON string into the form map:
// every catalog directive present (stored level or the default), plus any
// other stored key as-is. A value that is not a JSON object of strings loads
// as the defaults.
export function decodePHPSettingsPolicy(
  stored: unknown,
): PHPSettingsPolicyForm {
  const out = defaultPHPSettingsPolicy();
  if (typeof stored !== "string" || stored.trim() === "") return out;
  try {
    const parsed: unknown = JSON.parse(stored);
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
        if (typeof v === "string") out[k] = v as PHPSettingLevel;
      }
    }
  } catch {
    /* malformed stored value: show the defaults */
  }
  return out;
}

// encodePHPSettingsPolicy writes only what differs from the default, so an
// untouched policy stays "" (the canonical empty value the API and CLI store)
// and a later default change still reaches untouched directives. Non-catalog
// keys pass through unchanged.
export function encodePHPSettingsPolicy(
  value: PHPSettingsPolicyForm | string | undefined,
): string {
  if (value === undefined) return "";
  if (typeof value === "string") return value.trim();
  const catalog = new Set<string>(PHP_SETTING_DIRECTIVES);
  const out: Record<string, string> = {};
  for (const k of Object.keys(value).sort()) {
    const level = value[k];
    if (!level) continue;
    if (catalog.has(k) && level === PHP_SETTING_DEFAULT_LEVEL) continue;
    out[k] = level;
  }
  return Object.keys(out).length ? JSON.stringify(out) : "";
}
