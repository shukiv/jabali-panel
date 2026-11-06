// GH #726: every notification click must land somewhere; alerts without a
// dedicated deeplink fall back to the focused History tab.
import { describe, expect, it } from "vitest";

import { notificationTarget, panelPath } from "./notificationTarget";

const panelLinks = [
  "/",
  "/jabali-admin/backups",
  "/admin/security?tab=malware&sub=exec_audit",
  "/jabali-admin/users/5b0e8c1e-3f7a-4c1d-9f8e-2a6b7c8d9e0f",
  "/jabali-admin/notifications/history?focus=01ABC#row",
];

const notPanelLinks = [
  "https://example.test/x",
  "//example.test/x",
  "/\\example.test/x",
  "javascript:alert(1)",
  "/\t/example.test",
  "/a b",
  '/a"b',
  "jabali-admin/backups",
];

describe("panelPath", () => {
  it.each(panelLinks)("keeps the panel link %s", (link) => {
    expect(panelPath(link)).toBe(link);
  });

  it.each(notPanelLinks)("drops %s", (link) => {
    expect(panelPath(link)).toBeNull();
  });

  it("drops a link over 2048 bytes", () => {
    expect(panelPath("/" + "a".repeat(2047))).toBe("/" + "a".repeat(2047));
    expect(panelPath("/" + "a".repeat(2048))).toBeNull();
  });
});

describe("notificationTarget with a link outside the panel", () => {
  it.each(notPanelLinks)("falls back to focused History for %s", (link) => {
    expect(notificationTarget("01ABC", link)).toBe("/jabali-admin/notifications/history?focus=01ABC");
  });
});

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
