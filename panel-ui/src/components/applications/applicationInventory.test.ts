// applicationInventory.test.ts — pins the shared Application Inventory login
// capability, invalidation keys, and error extraction (JAB-334 AC2/AC3) so the
// admin and tenant lists can't drift from each other or from the backend.
import { afterEach, describe, it, expect, vi } from "vitest";

vi.mock("../../lib/feedback", () => ({
  feedback: { message: { warning: vi.fn(), error: vi.fn(), success: vi.fn() } },
}));

import { feedback } from "../../lib/feedback";
import {
  canApplicationLogin,
  APPLICATION_LOGIN_APP_TYPES,
  APPLICATION_INVALIDATION_KEYS,
  extractApiError,
  openApplicationLogin,
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

describe("openApplicationLogin", () => {
  function fakeTab() {
    return { opener: {} as unknown, location: { href: "" }, close: vi.fn() };
  }
  afterEach(() => {
    vi.restoreAllMocks();
    vi.mocked(feedback.message.warning).mockReset();
    vi.mocked(feedback.message.error).mockReset();
    vi.mocked(feedback.message.success).mockReset();
  });

  it("opens the tab before minting, then sends it to the link", async () => {
    const tab = fakeTab();
    const open = vi.spyOn(window, "open").mockReturnValue(tab as unknown as Window);
    let resolve!: (v: { url: string }) => void;
    const mint = vi.fn(() => new Promise<{ url: string }>((r) => (resolve = r)));

    const done = openApplicationLogin(mint);
    // The tab exists, with its opener cut, before the mint has answered.
    expect(open).toHaveBeenCalledWith("about:blank", "_blank");
    expect(tab.opener).toBeNull();
    expect(mint).toHaveBeenCalledTimes(1);

    resolve({ url: "https://site.example/wp-login.php?magic=abc" });
    await done;
    expect(tab.location.href).toBe("https://site.example/wp-login.php?magic=abc");
    expect(feedback.message.success).toHaveBeenCalled();
  });

  it("does not mint when the browser blocks the tab", async () => {
    vi.spyOn(window, "open").mockReturnValue(null);
    const mint = vi.fn();
    await openApplicationLogin(mint);
    expect(mint).not.toHaveBeenCalled();
    expect(feedback.message.warning).toHaveBeenCalled();
    expect(feedback.message.success).not.toHaveBeenCalled();
  });

  it("closes the tab and shows the reason when the mint fails", async () => {
    const tab = fakeTab();
    vi.spyOn(window, "open").mockReturnValue(tab as unknown as Window);
    await openApplicationLogin(() => Promise.reject(new Error("site is suspended")), "stale");
    expect(tab.close).toHaveBeenCalled();
    expect(feedback.message.error).toHaveBeenCalledWith("site is suspended");
  });

  it("closes the tab for a link that is not a web address", async () => {
    const tab = fakeTab();
    vi.spyOn(window, "open").mockReturnValue(tab as unknown as Window);
    await openApplicationLogin(() => Promise.resolve({ url: "javascript:alert(1)" }));
    expect(tab.close).toHaveBeenCalled();
    expect(tab.location.href).toBe("");
    expect(feedback.message.success).not.toHaveBeenCalled();
    expect(feedback.message.error).toHaveBeenCalled();
  });
});
