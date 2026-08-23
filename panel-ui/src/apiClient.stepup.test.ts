// JAB-380: recent-auth step-up redirect. A `stepup_required` 403 must route the
// browser through a Kratos *refresh* login that returns to the current page.
import { afterEach, describe, expect, it, vi } from "vitest";

import { stepUpRedirect } from "./apiClient";

describe("stepUpRedirect (JAB-380)", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("stashes the current path for post-login return and kicks the Kratos refresh flow", () => {
    const assign = vi.fn();
    // jsdom exposes a real location; override for the assertion.
    Object.defineProperty(window, "location", {
      configurable: true,
      value: {
        href: "https://panel.example.com/jabali-admin/files?path=/etc",
        pathname: "/jabali-admin/files",
        search: "?path=/etc",
        assign,
      },
    });
    sessionStorage.clear();

    stepUpRedirect();

    // The return path is carried in sessionStorage (Kratos return_to is dropped
    // on the login-browser → /login hop), and it must be a path so Login.tsx's
    // same-origin guard (startsWith "/" && !"//") accepts it.
    const stashed = sessionStorage.getItem("post_login_return_to");
    expect(stashed).toBe("/jabali-admin/files?path=/etc");
    expect(stashed?.startsWith("/")).toBe(true);
    expect(stashed?.startsWith("//")).toBe(false);

    expect(assign).toHaveBeenCalledTimes(1);
    const target = assign.mock.calls[0][0] as string;
    expect(target).toContain("/.ory/self-service/login/browser");
    expect(target).toContain("refresh=true");
    expect(target).toContain(
      "return_to=" + encodeURIComponent("/jabali-admin/files?path=/etc"),
    );
  });
});
