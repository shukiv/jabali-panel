// JAB-177: a demo write-guard rejection (403 {"error":"demo_mode"}) must be
// rewritten to a friendly sentence so no rendering path — err.message,
// response.data.error, or response.data.detail — ever shows the raw code.
import { describe, expect, it } from "vitest";

import { friendlyDemoError } from "./apiClient";

describe("friendlyDemoError (GH JAB-177)", () => {
  it("rewrites a bare demo_mode 403 to a friendly sentence", () => {
    const data: { error?: string; detail?: string } = { error: "demo_mode" };
    expect(friendlyDemoError(403, data)).toBe(true);
    expect(data.error).not.toBe("demo_mode");
    expect(data.error).toMatch(/read-only demo/i);
    expect(data.detail).toBe(data.error);
  });

  it("prefers the backend-supplied banner detail when present", () => {
    const data = { error: "demo_mode", detail: "Demo — nothing you do here persists." };
    friendlyDemoError(403, data);
    expect(data.error).toBe("Demo — nothing you do here persists.");
    expect(data.detail).toBe("Demo — nothing you do here persists.");
  });

  it("ignores a detail that is itself the raw code", () => {
    const data = { error: "demo_mode", detail: "demo_mode" };
    friendlyDemoError(403, data);
    expect(data.error).toMatch(/read-only demo/i);
  });

  it("leaves other errors untouched", () => {
    const data = { error: "domain_already_exists", detail: "taken" };
    expect(friendlyDemoError(403, data)).toBe(false);
    expect(data.error).toBe("domain_already_exists");
  });

  it("no-ops on a non-403 or missing body", () => {
    expect(friendlyDemoError(500, { error: "demo_mode" })).toBe(false);
    expect(friendlyDemoError(403, undefined)).toBe(false);
  });
});
