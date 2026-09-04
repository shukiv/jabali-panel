// sslState.test.ts — the JAB-300 "one tested SSL state matrix" that normalizes
// BOTH the tenant flat `ssl_state` wire and the admin nested `ssl: { status,
// issuer }` wire onto one color/label rendering. The parity block is the point:
// the two wire shapes for the same underlying state must render identically.
import { describe, it, expect } from "vitest";
import {
  getSSLTagColor,
  getSSLTagLabel,
  normalizeSSLBadge,
  getSSLTag,
} from "./sslState";

describe("getSSLTagColor (flat ssl_state matrix)", () => {
  const cases: Array<[string | undefined, string]> = [
    ["active_le", "gold"],
    ["active", "green"],
    ["provisioning", "orange"],
    ["self_signed", "orange"],
    ["pending", "green"],
    ["issuing", "green"],
    ["renewing", "green"],
    ["pending_acme_retry", "green"],
    ["custom", "green"],
    ["failed", "red"],
    ["error", "red"],
    ["revoked", "red"],
    ["none", "default"],
    ["off", "default"],
    ["", "default"],
    [undefined, "default"],
    ["something_new", "default"],
  ];
  it.each(cases)("%s → %s", (state, color) => {
    expect(getSSLTagColor(state)).toBe(color);
  });
});

describe("getSSLTagLabel (flat ssl_state matrix)", () => {
  const cases: Array<[string | undefined, string]> = [
    ["active_le", "Let's Encrypt"],
    ["active", "Active"],
    ["none", "None"],
    ["provisioning", "Self-signed…"],
    ["self_signed", "Self-signed"],
    ["pending", "Issuing…"],
    ["issuing", "Issuing…"],
    ["renewing", "Issuing…"],
    ["pending_acme_retry", "Issuing…"],
    ["custom", "Custom"],
    ["failed", "Failed"],
    ["error", "Failed"],
    ["revoked", "Revoked"],
    ["off", "Off"],
    ["", "Off"],
    [undefined, "Off"],
    // Unknown states fall through to the raw string so an unexpected backend
    // state is visible rather than silently rendered "Off".
    ["brand_new_state", "brand_new_state"],
  ];
  it.each(cases)("%s → %s", (state, label) => {
    expect(getSSLTagLabel(state)).toBe(label);
  });
});

// The AC's teeth: pin the matrix to what the two backends ACTUALLY emit, so a
// new backend state can't slip through to the raw-string passthrough unnoticed.
describe("backend SSL vocabularies are fully pinned (no raw passthrough)", () => {
  const KNOWN_LABELS = new Set([
    "Let's Encrypt",
    "Active",
    "None",
    "Self-signed…",
    "Self-signed",
    "Issuing…",
    "Failed",
    "Revoked",
    "Off",
    "Custom",
  ]);
  // Flat wire — internal/repository/domain_repository.go computeSSLState.
  const FLAT_STATES = ["off", "pending", "active_le", "self_signed", "failed"];
  // Admin nested wire — internal/api/domains.go sslBadgeForDomain +
  // sslBadgeFromCert set status = cert.Status (models/ssl_certificate.go) plus
  // the literal "none"/"provisioning".
  const ADMIN_STATUSES = [
    "pending",
    "issuing",
    "issued",
    "failed",
    "revoked",
    "renewing",
    "self_signed",
    "custom",
    "pending_acme_retry",
    "none",
    "provisioning",
  ];

  it.each(FLAT_STATES)("flat ssl_state %s resolves to a known label", (s) => {
    expect(KNOWN_LABELS.has(getSSLTag(s).label)).toBe(true);
  });
  it.each(ADMIN_STATUSES)("admin nested status %s resolves to a known label", (s) => {
    // issuer mirrors what sslBadgeFromCert hardcodes for issued/renewing certs.
    expect(KNOWN_LABELS.has(getSSLTag({ status: s, issuer: "Let's Encrypt" }).label)).toBe(true);
  });
});

describe("normalizeSSLBadge (nested admin wire → canonical flat state)", () => {
  it("folds the admin 'issued' spelling onto active_le, keeping the issuer", () => {
    expect(normalizeSSLBadge({ status: "issued", issuer: "Let's Encrypt" })).toEqual({
      state: "active_le",
      issuer: "Let's Encrypt",
    });
  });
  it("passes a non-diverging status through unchanged", () => {
    expect(normalizeSSLBadge({ status: "self_signed" })).toEqual({
      state: "self_signed",
      issuer: undefined,
    });
  });
  it("treats a null badge or a badge with no status as Off ('')", () => {
    expect(normalizeSSLBadge(null)).toEqual({ state: "" });
    expect(normalizeSSLBadge(undefined)).toEqual({ state: "" });
    expect(normalizeSSLBadge({ status: "" })).toEqual({ state: "" });
    expect(normalizeSSLBadge({ status: null })).toEqual({ state: "" });
  });
});

describe("getSSLTag (single entry point for both wire shapes)", () => {
  it("renders a flat ssl_state string", () => {
    expect(getSSLTag("active_le")).toEqual({ color: "gold", label: "Let's Encrypt" });
    expect(getSSLTag("self_signed")).toEqual({ color: "orange", label: "Self-signed" });
    expect(getSSLTag("failed")).toEqual({ color: "red", label: "Failed" });
    expect(getSSLTag("")).toEqual({ color: "default", label: "Off" });
    expect(getSSLTag(undefined)).toEqual({ color: "default", label: "Off" });
  });

  it("renders a nested admin badge, using the issuer as the label on a good cert", () => {
    expect(getSSLTag({ status: "issued", issuer: "Let's Encrypt" })).toEqual({
      color: "gold",
      label: "Let's Encrypt",
    });
    // A custom-uploaded cert's issuer/CN overrides the generic label.
    expect(getSSLTag({ status: "issued", issuer: "DigiCert Inc" })).toEqual({
      color: "gold",
      label: "DigiCert Inc",
    });
    // Issued but no issuer supplied → falls back to the canonical label.
    expect(getSSLTag({ status: "issued" })).toEqual({ color: "gold", label: "Let's Encrypt" });
    expect(getSSLTag({ status: "failed" })).toEqual({ color: "red", label: "Failed" });
    // The issuer is ignored for non-active states.
    expect(getSSLTag({ status: "failed", issuer: "whoever" })).toEqual({
      color: "red",
      label: "Failed",
    });
    expect(getSSLTag(null)).toEqual({ color: "default", label: "Off" });
    expect(getSSLTag({ status: "" })).toEqual({ color: "default", label: "Off" });
  });

  it("PARITY: the nested and flat wire cases for the same state render identically", () => {
    // A valid Let's Encrypt cert: admin sends { status: 'issued' }, tenant
    // sends 'active_le'. Both must produce the same badge (the drift this slice
    // removes — admin used to render this green, tenant gold).
    expect(getSSLTag({ status: "issued", issuer: "Let's Encrypt" })).toEqual(
      getSSLTag("active_le"),
    );
    expect(getSSLTag({ status: "self_signed" })).toEqual(getSSLTag("self_signed"));
    expect(getSSLTag({ status: "revoked" })).toEqual(getSSLTag("revoked"));
    expect(getSSLTag(null)).toEqual(getSSLTag(""));
  });
});
