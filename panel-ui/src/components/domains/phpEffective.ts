// What a domain's PHP pool really runs with (GH #1701): the shape of GET
// /domains/:id/php-settings/effective, and the function table built from it.
import { PHP_DEFENSE_EXEC_FUNCTIONS, PHP_LOCKDOWN_FUNCTIONS } from "../packages/phpDisabledFunctions";

export type EffectiveFunction = { name: string; source: "pool" | "php.ini" };
export type DefenseFunction = { name: string; state: "blocked" | "logged" };
export type EffectiveIni = { value: string; source: "pool" | "php.ini" };

export type PHPEffective = {
  php_version: string;
  pool_found: boolean;
  disabled_functions: EffectiveFunction[];
  php_defense: {
    active: boolean;
    mode: "enforce" | "simulation" | "off" | "";
    pool_rules: boolean;
    functions: DefenseFunction[];
  };
  include_path: EffectiveIni;
  session_save_path: EffectiveIni;
  ini_read_error?: string;
  // Functions this PHP version's FPM build does not provide at all (pcntl_*
  // without the pcntl extension, dl). Optional: an older agent omits it.
  unavailable_functions?: string[];
  availability_error?: string;
  // GH #2001: the PHP-FPM AppArmor profile is enforced, so the functions that
  // start programs can start only the shell and cat. Optional: an older agent
  // omits it.
  exec_confined?: boolean;
  // GH #2001: what those functions can start on this server. "enforce": only
  // the shell and cat; "complain" (the profile only logs) or "none" (not
  // loaded, or AppArmor off): any program; "unknown": could not be read.
  // Optional: older agents omit it.
  exec_confinement?: ExecConfinement;
};

export type ExecConfinement = "enforce" | "complain" | "none" | "unknown";

// The server's exec confinement, falling back to exec_confined for an agent
// that predates exec_confinement (it could only say "enforced").
export function execConfinement(e: PHPEffective): ExecConfinement {
  if (e.exec_confinement) return e.exec_confinement;
  return e.exec_confined ? "enforce" : "unknown";
}

// The functions that start another program: the ones PHP Defense bans. Under
// an enforced PHP-FPM AppArmor profile they can start only the shell and cat
// (GH #2001).
export const PHP_PROGRAM_FUNCTIONS: ReadonlySet<string> = new Set(PHP_DEFENSE_EXEC_FUNCTIONS);

export type FunctionRow = {
  name: string;
  status:
    | "disabled_package"
    | "disabled_server"
    | "blocked_defense"
    | "logged_defense"
    | "allowed"
    | "allowed_unavailable";
  // GH #2001: for an allowed function that starts programs, what it can
  // start: "confined" (only the shell and cat, the PHP-FPM AppArmor profile is
  // enforced) or "unconfined" (any program, the profile is not enforcing).
  exec?: "confined" | "unconfined";
};

// One row per function worth showing: the command-execution functions (the
// ones a package decides on) plus anything disabled or banned. The status a
// call really meets wins: disabled before PHP Defense, blocked before logged.
// GH #1701: a function the package allows but this PHP-FPM build does not
// provide is "allowed_unavailable". That qualifies only an allowed (or
// allowed-but-logged) function: disabled and blocked are permission states
// and keep their status. Once a PHP build provides it, the same package shows
// it as allowed. GH #2001: an allowed function that starts programs is marked
// with what it can start under the PHP-FPM AppArmor profile; that qualifies
// the status rather than replacing it.
const execTag: Record<ExecConfinement, FunctionRow["exec"]> = {
  enforce: "confined",
  complain: "unconfined",
  none: "unconfined",
  unknown: undefined,
};

export function functionRows(e: PHPEffective): FunctionRow[] {
  const unavailable = new Set(e.unavailable_functions ?? []);
  const disabled = new Map(e.disabled_functions.map((f) => [f.name, f.source]));
  const defense = new Map<string, DefenseFunction["state"]>();
  if (e.php_defense.active && e.php_defense.mode !== "off") {
    for (const f of e.php_defense.functions) defense.set(f.name, f.state);
  }
  const lockdown: string[] = [...PHP_LOCKDOWN_FUNCTIONS];
  const others = [...new Set([...disabled.keys(), ...defense.keys()])].filter((n) => !lockdown.includes(n)).sort();
  return [...lockdown, ...others].map((name): FunctionRow => {
    const src = disabled.get(name);
    if (src === "php.ini") return { name, status: "disabled_server" };
    if (src === "pool") return { name, status: "disabled_package" };
    const d = defense.get(name);
    if (d === "blocked") return { name, status: "blocked_defense" };
    if (unavailable.has(name)) return { name, status: "allowed_unavailable" };
    const status = d === "logged" ? "logged_defense" : "allowed";
    // GH #2001: callable, so what it can start depends on the AppArmor
    // profile: only the shell and cat when enforced, anything when not.
    const exec = PHP_PROGRAM_FUNCTIONS.has(name) ? execTag[execConfinement(e)] : undefined;
    return exec ? { name, status, exec } : { name, status };
  });
}
