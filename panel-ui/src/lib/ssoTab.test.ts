// ssoTab — the tab for a one-time sign-in link is opened before the link is
// minted, and it only ever goes to an http(s) address.
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("./feedback", () => ({
  feedback: { message: { warning: vi.fn(), error: vi.fn(), success: vi.fn() } },
}));

import { feedback } from "./feedback";
import { openSsoTab, sendSsoTab } from "./ssoTab";

const warn = feedback.message.warning as unknown as ReturnType<typeof vi.fn>;

function fakeTab() {
  return { opener: {} as unknown, location: { href: "" }, close: vi.fn() };
}

afterEach(() => {
  vi.restoreAllMocks();
  warn.mockReset();
});

describe("openSsoTab", () => {
  it("opens a blank tab and severs its opener", () => {
    const tab = fakeTab();
    const open = vi.spyOn(window, "open").mockReturnValue(tab as unknown as Window);
    expect(openSsoTab("blocked")).toBe(tab);
    expect(open).toHaveBeenCalledWith("about:blank", "_blank");
    expect(tab.opener).toBeNull();
    expect(warn).not.toHaveBeenCalled();
  });

  it("warns and returns null when the browser blocks the tab", () => {
    vi.spyOn(window, "open").mockReturnValue(null);
    expect(openSsoTab("allow popups")).toBeNull();
    expect(warn).toHaveBeenCalledWith("allow popups");
  });
});

describe("sendSsoTab", () => {
  it.each([
    ["https://pma.example/sso.php?token=abc", "https://pma.example/sso.php?token=abc"],
    ["http://host.example/x", "http://host.example/x"],
    ["/phpmyadmin/sso.php?token=abc", new URL("/phpmyadmin/sso.php?token=abc", window.location.href).href],
  ])("follows %s", (url, want) => {
    const tab = fakeTab();
    expect(sendSsoTab(tab as unknown as Window, url)).toBe(true);
    expect(tab.location.href).toBe(want);
    expect(tab.close).not.toHaveBeenCalled();
  });

  it.each([
    "javascript:alert(document.cookie)",
    " JavaScript:alert(1)",
    "data:text/html,<script>alert(1)</script>",
    "blob:https://x/1",
    "vbscript:x",
    "",
    null,
    undefined,
    "http://[",
  ])("closes the tab instead of following %s", (url) => {
    const tab = fakeTab();
    expect(sendSsoTab(tab as unknown as Window, url as string | null | undefined)).toBe(false);
    expect(tab.location.href).toBe("");
    expect(tab.close).toHaveBeenCalled();
  });
});
