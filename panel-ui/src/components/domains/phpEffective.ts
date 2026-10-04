// What a domain's PHP pool really runs with (GH #1701): the shape of GET
// /domains/:id/php-settings/effective, and the function table built from it.
import { PHP_LOCKDOWN_FUNCTIONS } from "../packages/phpDisabledFunctions";

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
};

export type FunctionRow = {
  name: string;
  status:
    | "disabled_package"
    | "disabled_server"
    | "blocked_defense"
    | "logged_defense"
    | "allowed"
    | "allowed_unavailable";
};

// One row per function worth showing: the command-execution functions (the
// ones a package decides on) plus anything disabled or banned. The status a
// call really meets wins: disabled before PHP Defense, blocked before logged.
// GH #1701: a function the package allows but this PHP-FPM build does not
// provide is "allowed_unavailable". That qualifies only an allowed (or
// allowed-but-logged) function: disabled and blocked are permission states
// and keep their status. Once a PHP build provides it, the same package shows
// it as allowed.
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
    if (d === "logged") return { name, status: "logged_defense" };
    return { name, status: "allowed" };
  });
}
