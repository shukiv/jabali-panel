import { describe, expect, it } from "vitest";

import { loginHostHint } from "./loginHostHint";

describe("loginHostHint", () => {
  it("warns with a one-click link when reached by IP but a hostname is configured", () => {
    const hint = loginHostHint({
      configuredHostname: "panel.example.com",
      brandingSettled: true,
      currentHostname: "203.0.113.10",
      currentHref: "https://203.0.113.10:8443/login?flow=abc123",
    });
    expect(hint).toEqual({
      show: true,
      kind: "mismatch",
      hostname: "panel.example.com",
      // scheme + port + path preserved; flow id stripped (origin-bound).
      targetHref: "https://panel.example.com:8443/login",
    });
  });

  it("warns on a stale FQDN too (not just IP), the case the old regex missed", () => {
    const hint = loginHostHint({
      configuredHostname: "new.example.com",
      brandingSettled: true,
      currentHostname: "old.example.com",
      currentHref: "https://old.example.com/login",
    });
    expect(hint.show).toBe(true);
    if (hint.show && hint.kind === "mismatch") {
      expect(hint.targetHref).toBe("https://new.example.com/login");
    } else {
      throw new Error("expected mismatch hint");
    }
  });

  it("stays silent when the current host matches the configured hostname (case-insensitive)", () => {
    expect(
      loginHostHint({
        configuredHostname: "Panel.Example.com",
        brandingSettled: true,
        currentHostname: "panel.example.com",
        currentHref: "https://panel.example.com/login",
      }),
    ).toEqual({ show: false });
  });

  it("stays silent for an IP-only install reached at its own IP (no false positive)", () => {
    expect(
      loginHostHint({
        configuredHostname: "203.0.113.10",
        brandingSettled: true,
        currentHostname: "203.0.113.10",
        currentHref: "https://203.0.113.10/login",
      }),
    ).toEqual({ show: false });
  });

  it("does not flash the IP fallback before branding settles", () => {
    expect(
      loginHostHint({
        configuredHostname: undefined,
        brandingSettled: false,
        currentHostname: "203.0.113.10",
        currentHref: "https://203.0.113.10/login",
      }),
    ).toEqual({ show: false });
  });

  it("falls back to the IP warning when branding settled with no hostname", () => {
    expect(
      loginHostHint({
        configuredHostname: "",
        brandingSettled: true,
        currentHostname: "203.0.113.10",
        currentHref: "https://203.0.113.10/login",
      }),
    ).toEqual({ show: true, kind: "ip" });
  });

  it("no warning on a plain FQDN when no hostname is configured yet", () => {
    expect(
      loginHostHint({
        configuredHostname: "",
        brandingSettled: true,
        currentHostname: "panel.example.com",
        currentHref: "https://panel.example.com/login",
      }),
    ).toEqual({ show: false });
  });
});
