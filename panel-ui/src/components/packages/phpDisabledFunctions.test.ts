// GH #1701: the package's disabled PHP functions, as the editor and the package
// list see them. Must agree with the backend's reading of the same fields.
import { describe, expect, it } from "vitest";
import {
  PHP_LOCKDOWN_FUNCTIONS,
  decodeDisabledFunctions,
  disabledFunctionsSummary,
  effectiveDisabledFunctions,
  encodeDisabledFunctions,
} from "./phpDisabledFunctions";

const LOCKDOWN = [...PHP_LOCKDOWN_FUNCTIONS];

describe("effectiveDisabledFunctions", () => {
  it("reads a package with no list through php_exec_enabled, like the backend", () => {
    expect(effectiveDisabledFunctions(null, false)).toEqual(LOCKDOWN);
    expect(effectiveDisabledFunctions(null, true)).toEqual([]);
    expect(effectiveDisabledFunctions("", false)).toEqual([]);
    expect(effectiveDisabledFunctions("exec,MAIL", true)).toEqual(["exec", "mail"]);
  });
});

describe("decode/encode round trip", () => {
  it("the default stays null so the package keeps following it", () => {
    const form = decodeDisabledFunctions(null, false);
    expect(form).toEqual({ lockdown: LOCKDOWN, extra: [] });
    expect(encodeDisabledFunctions(form)).toBeNull();
  });

  it("an allowed function and extra functions encode canonically", () => {
    const form = { lockdown: LOCKDOWN.filter((f) => f !== "shell_exec"), extra: ["mail", "Curl_Exec", "mail"] };
    expect(encodeDisabledFunctions(form)).toBe(
      "exec,passthru,system,proc_open,popen,pcntl_exec,pcntl_fork,proc_nice,dl,curl_exec,mail",
    );
    expect(decodeDisabledFunctions(encodeDisabledFunctions(form), false)).toEqual({
      lockdown: LOCKDOWN.filter((f) => f !== "shell_exec"),
      extra: ["curl_exec", "mail"],
    });
  });

  it("nothing disabled encodes to an empty string, not null", () => {
    expect(encodeDisabledFunctions({ lockdown: [], extra: [] })).toBe("");
    expect(decodeDisabledFunctions(null, true)).toEqual({ lockdown: [], extra: [] });
  });

  it("an extra that names a lockdown function is not duplicated", () => {
    expect(encodeDisabledFunctions({ lockdown: LOCKDOWN, extra: ["exec"] })).toBeNull();
  });
});

describe("disabledFunctionsSummary", () => {
  it("tells locked, partly and fully allowed packages apart", () => {
    expect(disabledFunctionsSummary(null, false)).toBe("locked");
    expect(disabledFunctionsSummary(null, true)).toBe("exec allowed");
    expect(disabledFunctionsSummary("exec,passthru,system,proc_open,popen,pcntl_exec,pcntl_fork,proc_nice,dl", false)).toBe(
      "1 exec allowed",
    );
    expect(disabledFunctionsSummary(LOCKDOWN.join(",") + ",mail", false)).toBe("locked, +1 disabled");
  });
});
