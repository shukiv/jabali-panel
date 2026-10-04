// GH #1701: the read-only function table on PHP Settings shows the status a
// call really meets.
import { describe, expect, it } from "vitest";
import { functionRows, type PHPEffective } from "./phpEffective";

const base: PHPEffective = {
  php_version: "8.4",
  pool_found: true,
  disabled_functions: [],
  php_defense: { active: true, mode: "enforce", pool_rules: false, functions: [] },
  include_path: { value: ".:/usr/share/php", source: "php.ini" },
  session_save_path: { value: "", source: "php.ini" },
};

const statusOf = (e: PHPEffective, name: string) => functionRows(e).find((r) => r.name === name)?.status;

describe("functionRows", () => {
  it("disabled beats PHP Defense, blocked beats logged, the rest is allowed", () => {
    const e: PHPEffective = {
      ...base,
      disabled_functions: [
        { name: "exec", source: "pool" },
        { name: "pcntl_alarm", source: "php.ini" },
      ],
      php_defense: {
        ...base.php_defense,
        functions: [
          { name: "exec", state: "blocked" },
          { name: "shell_exec", state: "blocked" },
          { name: "phpinfo", state: "blocked" },
        ],
      },
    };
    expect(statusOf(e, "exec")).toBe("disabled_package");
    expect(statusOf(e, "pcntl_alarm")).toBe("disabled_server");
    expect(statusOf(e, "shell_exec")).toBe("blocked_defense");
    expect(statusOf(e, "phpinfo")).toBe("blocked_defense");
    expect(statusOf(e, "proc_open")).toBe("allowed");
    // The command-execution functions always come first.
    expect(functionRows(e)[0].name).toBe("exec");
  });

  it("simulation logs; off or inactive PHP Defense bans nothing", () => {
    const fns = [{ name: "shell_exec", state: "logged" as const }];
    expect(statusOf({ ...base, php_defense: { ...base.php_defense, mode: "simulation", functions: fns } }, "shell_exec")).toBe(
      "logged_defense",
    );
    const blocked = [{ name: "shell_exec", state: "blocked" as const }];
    expect(statusOf({ ...base, php_defense: { ...base.php_defense, mode: "off", functions: blocked } }, "shell_exec")).toBe(
      "allowed",
    );
    expect(
      statusOf({ ...base, php_defense: { ...base.php_defense, active: false, functions: blocked } }, "shell_exec"),
    ).toBe("allowed");
  });

  it("GH #1701: a function this PHP-FPM build lacks is allowed but unavailable; permission states win", () => {
    const e: PHPEffective = {
      ...base,
      disabled_functions: [{ name: "dl", source: "pool" }],
      php_defense: {
        ...base.php_defense,
        mode: "simulation",
        functions: [
          { name: "pcntl_fork", state: "logged" },
          { name: "pcntl_exec", state: "blocked" },
        ],
      },
      unavailable_functions: ["dl", "pcntl_exec", "pcntl_fork", "proc_nice"],
    };
    expect(statusOf(e, "proc_nice")).toBe("allowed_unavailable");
    // Logged by PHP Defense but missing from the build: still unavailable.
    expect(statusOf(e, "pcntl_fork")).toBe("allowed_unavailable");
    // Disabled and blocked are permission states and keep their status.
    expect(statusOf(e, "dl")).toBe("disabled_package");
    expect(statusOf(e, "pcntl_exec")).toBe("blocked_defense");
    expect(statusOf(e, "exec")).toBe("allowed");
    // An agent that sends no list leaves every allowed function allowed.
    expect(statusOf(base, "proc_nice")).toBe("allowed");
  });

  it("GH #2001: an enforced AppArmor profile marks allowed program functions confined", () => {
    const e: PHPEffective = {
      ...base,
      exec_confined: true,
      disabled_functions: [{ name: "passthru", source: "pool" }],
      php_defense: { ...base.php_defense, mode: "simulation", functions: [{ name: "system", state: "logged" }] },
      unavailable_functions: ["pcntl_exec"],
    };
    const row = (name: string) => functionRows(e).find((r) => r.name === name);
    expect(row("shell_exec")).toEqual({ name: "shell_exec", status: "allowed", confined: true });
    // The qualifier keeps the status it qualifies.
    expect(row("system")).toEqual({ name: "system", status: "logged_defense", confined: true });
    // Disabled or missing functions are not callable, so nothing to qualify.
    expect(row("passthru")?.confined).toBeUndefined();
    expect(row("pcntl_exec")?.confined).toBeUndefined();
    // Functions that start no program are never confined.
    expect(row("proc_nice")?.confined).toBeUndefined();
    // Complain mode, or an older agent, leaves exec unconfined.
    expect(functionRows({ ...e, exec_confined: false }).some((r) => r.confined)).toBe(false);
    expect(functionRows(base).some((r) => r.confined)).toBe(false);
  });
});
