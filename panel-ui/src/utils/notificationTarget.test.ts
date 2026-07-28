// GH #726: every notification click must land somewhere; alerts without a
// dedicated deeplink fall back to the focused History tab.
import { describe, expect, it } from "vitest";

import { notificationTarget } from "./notificationTarget";

describe("notificationTarget (GH #726)", () => {
  it("uses the dedicated deeplink when present", () => {
    expect(notificationTarget("01ABC", "/jabali-admin/updates")).toBe("/jabali-admin/updates");
  });

  it("falls back to focused History when deeplink is missing", () => {
    expect(notificationTarget("01ABC")).toBe("/jabali-admin/notifications/history?focus=01ABC");
  });

  it("treats an empty-string deeplink as missing", () => {
    expect(notificationTarget("01ABC", "")).toBe("/jabali-admin/notifications/history?focus=01ABC");
  });

  it("treats null (server-omitted) deeplink as missing", () => {
    expect(notificationTarget("01ABC", null)).toBe("/jabali-admin/notifications/history?focus=01ABC");
  });
});
