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
});
