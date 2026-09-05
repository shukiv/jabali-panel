// applicationInventory.test.ts — pins the shared Application Inventory login
// capability, invalidation keys, and error extraction (JAB-334 AC2/AC3) so the
// admin and tenant lists can't drift from each other or from the backend.
import { describe, it, expect } from "vitest";
import {
  canApplicationLogin,
  APPLICATION_LOGIN_APP_TYPES,
  APPLICATION_INVALIDATION_KEYS,
  extractApiError,
} from "./applicationInventory";

describe("canApplicationLogin", () => {
  it("allows the CMS types the SSO flow drives, when ready", () => {
    expect(canApplicationLogin({ status: "ready", app_type: "wordpress" })).toBe(true);
    expect(canApplicationLogin({ status: "ready", app_type: "drupal" })).toBe(true);
    expect(canApplicationLogin({ status: "ready", app_type: "joomla" })).toBe(true);
  });

  it("defaults a missing app_type to wordpress", () => {
    expect(canApplicationLogin({ status: "ready", app_type: undefined })).toBe(true);
    expect(canApplicationLogin({ status: "ready" })).toBe(true);
  });

  it("denies a CMS with no SSO handler", () => {
    expect(canApplicationLogin({ status: "ready", app_type: "magento" })).toBe(false);
  });

  it("denies any non-ready row", () => {
    expect(canApplicationLogin({ status: "installing", app_type: "wordpress" })).toBe(false);
    expect(canApplicationLogin({ status: "failed", app_type: "wordpress" })).toBe(false);
    expect(canApplicationLogin({ status: "deleting", app_type: "wordpress" })).toBe(false);
    expect(canApplicationLogin({ status: "pending", app_type: "wordpress" })).toBe(false);
  });
});

describe("APPLICATION_LOGIN_APP_TYPES", () => {
  it("mirrors panel-api ssoAgentCommandFor (wordpress/drupal/joomla)", () => {
    // If the backend gains an SSO-file handler for another CMS, widen the set
    // here too — this assertion is the guard against the button drifting from
    // the backend capability.
    expect([...APPLICATION_LOGIN_APP_TYPES].sort()).toEqual([
      "drupal",
      "joomla",
      "wordpress",
    ]);
  });
});

describe("APPLICATION_INVALIDATION_KEYS", () => {
  it("invalidates both the applications and databases lists", () => {
    expect(APPLICATION_INVALIDATION_KEYS.map((k) => [...k])).toEqual([
      ["list", "applications"],
      ["list", "databases"],
    ]);
  });
});

describe("extractApiError", () => {
  it("prefers detail, then error, then transport message, then fallback", () => {
    expect(
      extractApiError({ response: { data: { detail: "d", error: "e" } }, message: "m" }, "fb"),
    ).toBe("d");
    expect(extractApiError({ response: { data: { error: "e" } }, message: "m" }, "fb")).toBe("e");
    expect(extractApiError({ message: "m" }, "fb")).toBe("m");
    expect(extractApiError({}, "fb")).toBe("fb");
    expect(extractApiError(null, "fb")).toBe("fb");
  });
});
