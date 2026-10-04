// Disabled PHP functions per hosting package (GH #1701). The package's
// php_disabled_functions is the disable_functions list its sites run with:
// null = the default command-exec lockdown below. The backend is the authority
// (models.NormalizePHPDisabledFunctions); this file is the package editor's and
// the package list's view of it.
//
// The two lists mirror the Go ones in order; the Go test
// TestPHPLockdownTSInSync fails CI when they drift.

// The default lockdown: every command-execution function.
export const PHP_LOCKDOWN_FUNCTIONS = [
  "exec",
  "passthru",
  "shell_exec",
  "system",
  "proc_open",
  "popen",
  "pcntl_exec",
  "pcntl_fork",
  "proc_nice",
  "dl",
] as const;

// The functions PHP Defense also bans outright. Allowing one on a package
// lifts that ban for the package's sites too.
export const PHP_DEFENSE_EXEC_FUNCTIONS = [
  "system",
  "exec",
  "shell_exec",
  "passthru",
  "popen",
  "proc_open",
  "pcntl_exec",
] as const;

// A PHP function name as the backend accepts it (after lowercasing).
export const PHP_FUNCTION_NAME = /^[a-z_][a-z0-9_]{0,63}$/;

// The package editor's form shape: which lockdown functions stay disabled
// (checked), and the extra functions the admin disables on top.
export type PHPDisabledFunctionsForm = { lockdown: string[]; extra: string[] };

const LOCKDOWN_SET: ReadonlySet<string> = new Set(PHP_LOCKDOWN_FUNCTIONS);

function splitList(value: string): string[] {
  return value
    .split(/[\s,]+/)
    .map((f) => f.trim().toLowerCase())
    .filter(Boolean);
}

// The list a package's sites run with. A package saved before the list existed
// (null list) follows php_exec_enabled, exactly as the backend reads it.
export function effectiveDisabledFunctions(stored: string | null | undefined, execEnabled: boolean): string[] {
  if (stored === null || stored === undefined) {
    return execEnabled ? [] : [...PHP_LOCKDOWN_FUNCTIONS];
  }
  return [...new Set(splitList(stored))];
}

export function defaultDisabledFunctionsForm(): PHPDisabledFunctionsForm {
  return { lockdown: [...PHP_LOCKDOWN_FUNCTIONS], extra: [] };
}

export function decodeDisabledFunctions(stored: string | null | undefined, execEnabled: boolean): PHPDisabledFunctionsForm {
  const eff = effectiveDisabledFunctions(stored, execEnabled);
  return {
    lockdown: PHP_LOCKDOWN_FUNCTIONS.filter((f) => eff.includes(f)),
    extra: eff.filter((f) => !LOCKDOWN_SET.has(f)).sort(),
  };
}

// The wire value: null when the form is exactly the default (so the package
// keeps following it), else the canonical comma-separated list.
export function encodeDisabledFunctions(form: PHPDisabledFunctionsForm | undefined): string | null {
  if (!form) return null;
  const lockdown = PHP_LOCKDOWN_FUNCTIONS.filter((f) => form.lockdown?.includes(f));
  const extra = [...new Set((form.extra ?? []).flatMap(splitList))].filter((f) => !LOCKDOWN_SET.has(f)).sort();
  if (lockdown.length === PHP_LOCKDOWN_FUNCTIONS.length && extra.length === 0) return null;
  return [...lockdown, ...extra].join(",");
}

// What the package list shows for a package.
export function disabledFunctionsSummary(stored: string | null | undefined, execEnabled: boolean): string {
  const form = decodeDisabledFunctions(stored, execEnabled);
  const allowed = PHP_LOCKDOWN_FUNCTIONS.filter((f) => !form.lockdown.includes(f));
  const parts: string[] = [];
  if (allowed.length === PHP_LOCKDOWN_FUNCTIONS.length) parts.push("exec allowed");
  else if (allowed.length > 0) parts.push(`${allowed.length} exec allowed`);
  else parts.push("locked");
  if (form.extra.length > 0) parts.push(`+${form.extra.length} disabled`);
  return parts.join(", ");
}
