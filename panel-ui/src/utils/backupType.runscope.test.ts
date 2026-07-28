// GH #502: the collapsed Full Server run row summarizes its scope so an admin
// sees "one server backup", not hundreds of account rows.
import { describe, expect, it } from "vitest";

import { runScopeSummary } from "./backupType";

describe("runScopeSummary (GH #502)", () => {
  it("full server: system + N accounts", () => {
    expect(runScopeSummary(13, 12)).toBe("system + 12 accounts");
  });

  it("singular account is not pluralized", () => {
    expect(runScopeSummary(2, 1)).toBe("system + 1 account");
  });

  it("accounts-only run (no system job)", () => {
    expect(runScopeSummary(5, 5)).toBe("5 accounts");
  });

  it("system-only run -> empty (the type tag already says it)", () => {
    expect(runScopeSummary(1, 0)).toBe("");
  });
});
